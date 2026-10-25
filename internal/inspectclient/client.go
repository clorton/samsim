package inspectclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is gpsinspect's REST client: a thin, byte-limited, typed wrapper
// around net/http with no automatic retries (INS-01: "No unbounded
// automatic retries on network/5xx errors for finite commands").
type Client struct {
	BaseURL          *url.URL
	HTTPClient       *http.Client // injected for testability; must not follow redirects
	MaxResponseBytes int64
	// Timeout bounds each individual HTTP request (INS-01: "5s per HTTP
	// request/WS connection handshake", not a total command duration). If
	// zero, no per-request deadline is applied beyond the caller's own ctx.
	Timeout time.Duration
}

// errorEnvelope mirrors AG-07's {"error":{...}} response shape.
type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// NoRedirect is a http.Client CheckRedirect func that refuses to follow any
// redirect, per INS-01 ("Do not follow redirects or forward credentials").
func NoRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// buildURL joins path segments onto the base URL, escaping each segment
// (INS-01: "Append fixed API paths and escape IDs/path components/query
// values safely"), and attaches the given query parameters.
func (c *Client) buildURL(segments []string, query url.Values) string {
	u := *c.BaseURL
	var b strings.Builder
	for _, seg := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	u.Path = b.String()
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// getJSON performs a GET request and decodes a successful (2xx) JSON body
// into out, applying the response byte limit and validating the status
// before decoding (INS-01: "Check status before decoding a success body").
func (c *Client) getJSON(ctx context.Context, segments []string, query url.Values, out any) error {
	return c.doJSON(ctx, http.MethodGet, segments, query, nil, out)
}

// postJSON performs a POST request with a JSON body and decodes a
// successful JSON response into out (out may be nil to discard the body).
func (c *Client) postJSON(ctx context.Context, segments []string, body any, out any) error {
	return c.doJSON(ctx, http.MethodPost, segments, nil, body, out)
}

func (c *Client) doJSON(ctx context.Context, method string, segments []string, query url.Values, body any, out any) error {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	u := c.buildURL(segments, query)

	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return protocolError("encode request body: %v", err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return protocolError("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return interruptedError("request to %s was canceled: %v", u, err)
		}
		if ctx.Err() != nil {
			return timeoutError("request to %s timed out: %v", u, ctx.Err())
		}
		return networkError("request to %s failed: %v", u, err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, c.MaxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return networkError("reading response from %s failed: %v", u, err)
	}
	if int64(len(data)) > c.MaxResponseBytes {
		return protocolError("response from %s exceeded max-response-bytes limit", u)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env errorEnvelope
		if err := json.Unmarshal(data, &env); err != nil || env.Error.Code == "" {
			return protocolError("unexpected non-JSON or malformed error response (status %d) from %s", resp.StatusCode, u)
		}
		return apiError(env.Error.Code, env.Error.Message, resp.StatusCode, env.Error.Details)
	}

	if out == nil {
		return nil
	}
	return decodeInto(data, out, u)
}

// decodeInto decodes data into out. Our DTOs declare int64/string fields
// explicitly, so encoding/json decodes timestamps/revisions as integers,
// never float64, as long as callers never route through map[string]any.
// Unknown JSON fields are permitted (ordinary json.Unmarshal/Decode
// behavior) for forward compatibility, per INS-01; only malformed/truncated
// bodies and trailing data are rejected.
func decodeInto(data []byte, out any, sourceURL string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return protocolError("empty or truncated response body from %s", sourceURL)
		}
		return protocolError("malformed response body from %s: %v", sourceURL, err)
	}
	if dec.More() {
		return protocolError("response body from %s contains trailing data", sourceURL)
	}
	return nil
}
