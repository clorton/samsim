package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProxy_List(t *testing.T) {
	p := NewProxy([]Endpoint{
		{SimulatorID: "sim-1", ControlURL: "http://example.invalid", VehicleID: "vehicle-1"},
		{SimulatorID: "sim-2", ControlURL: "http://example.invalid", VehicleID: "vehicle-2"},
	})
	list := p.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 simulators, got %d", len(list))
	}
}

func TestProxy_Status_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("expected /status, got %s", r.URL.Path)
		}
		w.Write([]byte(`{"state":"running"}`))
	}))
	defer upstream.Close()

	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: upstream.URL}})
	body, err := p.Status(context.Background(), "sim-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"state":"running"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestProxy_UnknownSimulator(t *testing.T) {
	p := NewProxy(nil)
	_, err := p.Status(context.Background(), "nonexistent")
	if err != ErrUnknownSimulator {
		t.Fatalf("expected ErrUnknownSimulator, got %v", err)
	}
}

func TestProxy_UnknownAction(t *testing.T) {
	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: "http://example.invalid"}})
	_, err := p.Action(context.Background(), "sim-1", "not-a-real-action")
	if err != ErrUnknownAction {
		t.Fatalf("expected ErrUnknownAction, got %v", err)
	}
}

func TestProxy_AllowedActions(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: upstream.URL}})
	for _, action := range []string{"start", "stop", "pause-transmission", "resume-transmission", "reset"} {
		_, err := p.Action(context.Background(), "sim-1", action)
		if err != nil {
			t.Fatalf("action %s: %v", action, err)
		}
		if gotPath != "/"+action {
			t.Fatalf("expected path /%s, got %s", action, gotPath)
		}
	}
}

func TestProxy_UpstreamUnavailable(t *testing.T) {
	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: "http://127.0.0.1:1"}}) // nothing listening
	_, err := p.Status(context.Background(), "sim-1")
	ue, ok := err.(*UpstreamError)
	if !ok || ue.Kind != "unavailable" {
		t.Fatalf("expected unavailable UpstreamError, got %v", err)
	}
}

func TestProxy_UpstreamTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(4 * time.Second) // longer than the 3s upstream timeout
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: upstream.URL}})
	_, err := p.Status(context.Background(), "sim-1")
	ue, ok := err.(*UpstreamError)
	if !ok || ue.Kind != "timeout" {
		t.Fatalf("expected timeout UpstreamError, got %v", err)
	}
}

func TestProxy_NoRedirects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer upstream.Close()

	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: upstream.URL}})
	body, err := p.Status(context.Background(), "sim-1")
	// CheckRedirect returns ErrUseLastResponse, so the 302 itself is
	// returned to the caller as a client_error, not followed.
	if err == nil {
		t.Fatalf("expected the redirect status to surface as an error, got body=%s", body)
	}
	ue, ok := err.(*UpstreamError)
	if !ok || ue.StatusCode != http.StatusFound {
		t.Fatalf("expected UpstreamError wrapping 302, got %v", err)
	}
}

func TestProxy_UpstreamClientError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"INVALID_REQUEST","message":"bad config"}}`))
	}))
	defer upstream.Close()

	p := NewProxy([]Endpoint{{SimulatorID: "sim-1", ControlURL: upstream.URL}})
	_, err := p.Config(context.Background(), "sim-1", []byte(`{}`))
	ue, ok := err.(*UpstreamError)
	if !ok || ue.Kind != "client_error" || ue.StatusCode != 400 {
		t.Fatalf("expected client_error 400, got %v", err)
	}
}
