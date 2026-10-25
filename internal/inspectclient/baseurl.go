// Package inspectclient implements gpsinspect's REST client: base URL
// validation, byte-limited/typed HTTP requests, typed errors, pagination,
// and the shared coherent-read (at_revision) protocol used by INS-02 and
// INS-05. It deliberately reuses the aggregator's own query/wire DTOs
// (internal/aggregator, internal/apierr) rather than hand-transcribing
// field names, since INSPECTOR_SPEC.md explicitly permits sharing DTOs and
// HTTP primitives ("sharing the algorithm being checked is not").
package inspectclient

import (
	"fmt"
	"net/url"
)

// ValidateBaseURL implements INS-01's base URL contract: absolute
// http/https with a host, no credentials, no query string, no fragment,
// and a root path only (so API paths can be appended unambiguously).
func ValidateBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("base URL must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("base URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("base URL %q must include a host", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("base URL %q must not include credentials", raw)
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("base URL %q must not include a query string", raw)
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("base URL %q must not include a fragment", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("base URL %q must use the root path only", raw)
	}
	out := *u
	out.Path = ""
	out.RawQuery = ""
	out.Fragment = ""
	return &out, nil
}
