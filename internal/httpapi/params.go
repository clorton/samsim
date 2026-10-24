package httpapi

import (
	"net/http"
	"strconv"

	"gpsdemo/internal/apierr"
)

// parseInt64Param parses an optional query parameter as int64, returning nil
// if absent, or an *apierr.Error (INVALID_REQUEST) if present but malformed.
func parseInt64Param(r *http.Request, name string) (*int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, apierr.Newf(apierr.CodeInvalidRequest, "invalid %s: %v", name, err)
	}
	return &n, nil
}

// parseIntParam parses an optional query parameter as int, returning
// defaultVal if absent, or an error if present but malformed.
func parseIntParam(r *http.Request, name string, defaultVal int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, apierr.Newf(apierr.CodeInvalidRequest, "invalid %s: %v", name, err)
	}
	return n, nil
}

func parseBoolParam(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "true" || v == "1"
}
