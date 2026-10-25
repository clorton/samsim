package simulator

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestHTTPServer(t *testing.T) (*httptest.Server, *Engine, *testEngineDeps) {
	t.Helper()
	e, deps := newTestEngine(t, nil, buildRouteCSV(1000))
	srv := &Server{Engine: e}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts, e, deps
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("invalid JSON response: %s: %v", body, err)
		}
	}
	return resp.StatusCode, result
}

func postJSON(t *testing.T, url string, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(http.MethodPost, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("invalid JSON response: %s: %v", respBody, err)
		}
	}
	return resp.StatusCode, result
}

func TestHTTP_GetStatus(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	status, body := getJSON(t, ts.URL+"/status")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if body["playback_state"] != "READY" {
		t.Fatalf("expected READY, got %+v", body)
	}
}

func TestHTTP_GetStatus_WrongMethod(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("expected Allow: GET, got %q", resp.Header.Get("Allow"))
	}
}

func TestHTTP_PostStart(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	status, body := postJSON(t, ts.URL+"/start", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", status, body)
	}
	if body["playback_state"] != "RUNNING" {
		t.Fatalf("expected RUNNING, got %+v", body)
	}
}

func TestHTTP_PostStart_AfterCompletedReturns409(t *testing.T) {
	ts, e, deps := newTestHTTPServer(t)
	// Large enough packet_size to drain the whole (small) route in one
	// packet once DRAINING is reached; a small report interval so the whole
	// route's single window closes promptly once generated.
	e.PutConfig(ConfigUpdate{PacketSize: intPtr(1000)})
	e.Stop()
	newInterval := int64(1)
	e.PutConfig(ConfigUpdate{NormalReportIntervalSimulatedMs: &newInterval})
	postJSON(t, ts.URL+"/start", "")
	deps.mono.Advance(10 * time.Second) // more than enough virtual time to exhaust the 1000-row route
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)
	for i := 0; i < 50 && e.Status().PlaybackState != PlaybackCompleted; i++ {
		e.Tick()
		time.Sleep(time.Millisecond)
	}
	if e.Status().PlaybackState != PlaybackCompleted {
		t.Fatalf("expected COMPLETED, got %s", e.Status().PlaybackState)
	}

	status, body := postJSON(t, ts.URL+"/start", "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %+v", status, body)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != CodeResetRequired {
		t.Fatalf("expected RESET_REQUIRED, got %+v", errObj)
	}
}

func TestHTTP_PostStop(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	postJSON(t, ts.URL+"/start", "")
	status, body := postJSON(t, ts.URL+"/stop", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if body["playback_state"] != "STOPPED" {
		t.Fatalf("expected STOPPED, got %+v", body)
	}
}

func TestHTTP_PauseAndResumeTransmission(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	postJSON(t, ts.URL+"/start", "")
	status, body := postJSON(t, ts.URL+"/pause-transmission", "")
	if status != http.StatusOK || body["transmission_state"] != "PAUSED" {
		t.Fatalf("expected 200/PAUSED, got %d %+v", status, body)
	}
	status, body = postJSON(t, ts.URL+"/resume-transmission", "")
	if status != http.StatusOK || body["transmission_state"] != "ENABLED" {
		t.Fatalf("expected 200/ENABLED, got %d %+v", status, body)
	}
}

func TestHTTP_ResumeTransmission_RetryConfirmationRequired(t *testing.T) {
	ts, e, deps := newTestHTTPServer(t)
	e.PutConfig(ConfigUpdate{PacketSize: intPtr(1)})
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError}
	})
	postJSON(t, ts.URL+"/start", "")
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	status, body := postJSON(t, ts.URL+"/resume-transmission", "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %+v", status, body)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != CodeRetryConfirmationReq {
		t.Fatalf("expected RETRY_CONFIRMATION_REQUIRED, got %+v", errObj)
	}

	status, body = postJSON(t, ts.URL+"/resume-transmission", `{"retry_failed":true}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200 with retry_failed=true, got %d: %+v", status, body)
	}
}

func TestHTTP_Reset_PendingDataRequires409(t *testing.T) {
	ts, e, deps := newTestHTTPServer(t)
	e.PutConfig(ConfigUpdate{PacketSize: intPtr(1000), NormalReportIntervalSimulatedMs: int64Ptr(999_000_000)})
	postJSON(t, ts.URL+"/start", "")
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	postJSON(t, ts.URL+"/stop", "")

	status, body := postJSON(t, ts.URL+"/reset", "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %+v", status, body)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != CodePendingData {
		t.Fatalf("expected PENDING_DATA, got %+v", errObj)
	}

	status, body = postJSON(t, ts.URL+"/reset", `{"discard_pending":true}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200 with discard, got %d: %+v", status, body)
	}
	if body["discarded_fixes"].(float64) == 0 {
		t.Fatal("expected nonzero discarded_fixes")
	}
}

func TestHTTP_PutConfig(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	status, body := postJSONWithMethod(t, http.MethodPut, ts.URL+"/config", `{"clock_multiplier":500}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", status, body)
	}
	cfg := body["config"].(map[string]any)
	if cfg["clock_multiplier"].(float64) != 500 {
		t.Fatalf("expected clock_multiplier 500, got %+v", cfg)
	}
}

func TestHTTP_PutConfig_NoFields(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	status, body := postJSONWithMethod(t, http.MethodPut, ts.URL+"/config", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %+v", status, body)
	}
}

func TestHTTP_PutConfig_WrongMethod(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	status, _ := getJSON(t, ts.URL+"/config")
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", status)
	}
}

func TestHTTP_UnknownFieldsRejected(t *testing.T) {
	ts, _, _ := newTestHTTPServer(t)
	status, body := postJSON(t, ts.URL+"/resume-transmission", `{"retry_failed":true,"bogus":1}`)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field, got %d: %+v", status, body)
	}
}

func postJSONWithMethod(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("invalid JSON response: %s: %v", respBody, err)
		}
	}
	return resp.StatusCode, result
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }
