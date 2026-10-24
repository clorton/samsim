// Package control implements AG-09's simulator control-plane proxy: a
// static, trusted registry of simulator control URLs, with a fixed allowed
// action list, bounded timeouts, and no dynamic registration or arbitrary
// browser-supplied URLs.
package control

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Endpoint is one statically configured simulator (AG-01/AG-09 config).
type Endpoint struct {
	SimulatorID string
	ControlURL  string
	VehicleID   string // startup hint only; simulator /status is authoritative
}

// allowedActions is the fixed allowlist for POST /api/v1/simulators/{id}/{action}.
var allowedActions = map[string]bool{
	"start":               true,
	"stop":                true,
	"pause-transmission":  true,
	"resume-transmission": true,
	"reset":               true,
}

const (
	upstreamTimeout     = 3 * time.Second
	maxUpstreamReqBody  = 64 * 1024
	maxUpstreamRespBody = 256 * 1024
)

// Proxy holds the static simulator registry and an HTTP client configured
// with no redirects.
type Proxy struct {
	endpoints map[string]Endpoint
	client    *http.Client
}

// NewProxy builds a Proxy from the configured simulator endpoints.
func NewProxy(endpoints []Endpoint) *Proxy {
	m := make(map[string]Endpoint, len(endpoints))
	for _, e := range endpoints {
		m[e.SimulatorID] = e
	}
	return &Proxy{
		endpoints: m,
		client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// ErrUnknownSimulator indicates the simulator ID is not in the static
// registry.
var ErrUnknownSimulator = fmt.Errorf("control: unknown simulator")

// ErrUnknownAction indicates the requested action is not in the fixed
// allowlist.
var ErrUnknownAction = fmt.Errorf("control: unknown action")

// SimulatorInfo is the result of GET /api/v1/simulators: configured IDs and
// hints, without blocking on any upstream call.
type SimulatorInfo struct {
	SimulatorID string `json:"simulator_id"`
	VehicleID   string `json:"vehicle_id"`
}

// List returns all configured simulator IDs and hints.
func (p *Proxy) List() []SimulatorInfo {
	out := make([]SimulatorInfo, 0, len(p.endpoints))
	for _, e := range p.endpoints {
		out = append(out, SimulatorInfo{SimulatorID: e.SimulatorID, VehicleID: e.VehicleID})
	}
	return out
}

// UpstreamError distinguishes simulator unavailability/timeout from
// aggregator-side errors, and carries any upstream 4xx body so it can be
// passed through using the normalized error envelope.
type UpstreamError struct {
	Kind       string // "unavailable" | "timeout" | "client_error"
	StatusCode int
	Body       []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("control: upstream %s (status %d)", e.Kind, e.StatusCode)
}

// Status proxies GET /status to the simulator's control URL.
func (p *Proxy) Status(ctx context.Context, simulatorID string) ([]byte, error) {
	return p.do(ctx, simulatorID, http.MethodGet, "/status", nil)
}

// Config proxies PUT /config to the simulator's control URL.
func (p *Proxy) Config(ctx context.Context, simulatorID string, body []byte) ([]byte, error) {
	if len(body) > maxUpstreamReqBody {
		return nil, &UpstreamError{Kind: "client_error", StatusCode: http.StatusRequestEntityTooLarge}
	}
	return p.do(ctx, simulatorID, http.MethodPut, "/config", body)
}

// Action proxies POST /{action} to the simulator's control URL, if action is
// in the fixed allowlist.
func (p *Proxy) Action(ctx context.Context, simulatorID, action string) ([]byte, error) {
	if !allowedActions[action] {
		return nil, ErrUnknownAction
	}
	return p.do(ctx, simulatorID, http.MethodPost, "/"+action, nil)
}

func (p *Proxy) do(ctx context.Context, simulatorID, method, path string, body []byte) ([]byte, error) {
	ep, ok := p.endpoints[simulatorID]
	if !ok {
		return nil, ErrUnknownSimulator
	}

	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.ControlURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("control: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &UpstreamError{Kind: "timeout"}
		}
		return nil, &UpstreamError{Kind: "unavailable"}
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxUpstreamRespBody)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("control: read upstream response: %w", err)
	}

	if resp.StatusCode >= 500 {
		return nil, &UpstreamError{Kind: "unavailable", StatusCode: resp.StatusCode, Body: respBody}
	}
	if resp.StatusCode >= 300 {
		// Includes 3xx: with CheckRedirect returning ErrUseLastResponse, the
		// client never follows a redirect, so a 3xx here means the upstream
		// itself returned one; treat it the same as a 4xx client error rather
		// than passing an opaque redirect body through a JSON control API.
		return nil, &UpstreamError{Kind: "client_error", StatusCode: resp.StatusCode, Body: respBody}
	}

	return respBody, nil
}
