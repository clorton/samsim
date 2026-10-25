package simulator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testPacket(t *testing.T, encoding Encoding) *Packet {
	t.Helper()
	fixes := []RouteRecord{
		{TimestampMs: 1000, Latitude: 1, Longitude: 2, SpeedMps: 3, HeadingDegrees: 4},
		{TimestampMs: 2000, Latitude: 5, Longitude: 6, SpeedMps: 7, HeadingDegrees: 8},
	}
	pkt, err := BuildPacket("pkt-test", "sim-test", "veh-test", fixes, encoding)
	if err != nil {
		t.Fatalf("BuildPacket: %v", err)
	}
	return pkt
}

func validReceiptBody(pkt *Packet) string {
	return fmt.Sprintf(
		`{"simulator_id":%q,"packet_id":%q,"duplicate_packet":false,"accepted_fixes":%d,"duplicate_fixes":0,"quarantined_fixes":0,"trip_updates":1,"revision":1}`,
		pkt.SimulatorID, pkt.PacketID, pkt.FixCount,
	)
}

func TestSendSuccess(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validReceiptBody(pkt)))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if outcome.Failed {
		t.Fatalf("expected success, got failure: %+v", outcome)
	}
	if outcome.Receipt == nil {
		t.Fatal("expected non-nil Receipt")
	}
	if outcome.Receipt.SimulatorID != pkt.SimulatorID {
		t.Errorf("Receipt.SimulatorID = %q, want %q", outcome.Receipt.SimulatorID, pkt.SimulatorID)
	}
	if outcome.Receipt.PacketID != pkt.PacketID {
		t.Errorf("Receipt.PacketID = %q, want %q", outcome.Receipt.PacketID, pkt.PacketID)
	}
	if outcome.Receipt.AcceptedFixes != pkt.FixCount {
		t.Errorf("Receipt.AcceptedFixes = %d, want %d", outcome.Receipt.AcceptedFixes, pkt.FixCount)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
	}
}

func TestSendContentTypeProtobuf(t *testing.T) {
	pkt := testPacket(t, EncodingProtobuf)
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validReceiptBody(pkt)))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)
	if outcome.Failed {
		t.Fatalf("expected success, got failure: %+v", outcome)
	}
	if gotContentType != "application/x-protobuf" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/x-protobuf")
	}
}

func TestSendNetworkError(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close() // now the URL points to nothing listening

	s := NewSender(deadURL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindNetworkError {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindNetworkError)
	}
	if outcome.AcceptanceUncertain {
		t.Error("AcceptanceUncertain = true, want false")
	}
	if outcome.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0", outcome.HTTPStatus)
	}
}

func TestSendTimeout(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validReceiptBody(pkt)))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, 50*time.Millisecond)
	outcome := s.Send(context.Background(), pkt)

	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindTimeout {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindTimeout)
	}
	if !outcome.AcceptanceUncertain {
		t.Error("AcceptanceUncertain = false, want true")
	}
	if outcome.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0", outcome.HTTPStatus)
	}
}

func TestSendNon200(t *testing.T) {
	cases := []struct {
		status    int
		uncertain bool
	}{
		{http.StatusNotFound, false},
		{http.StatusServiceUnavailable, true},
		{http.StatusConflict, true},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			pkt := testPacket(t, EncodingJSON)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			s := NewSender(srv.URL, time.Second)
			outcome := s.Send(context.Background(), pkt)

			if !outcome.Failed {
				t.Fatal("expected failure")
			}
			if outcome.FailureKind != FailureKindNon200 {
				t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindNon200)
			}
			if outcome.HTTPStatus != tc.status {
				t.Errorf("HTTPStatus = %d, want %d", outcome.HTTPStatus, tc.status)
			}
			if outcome.AcceptanceUncertain != tc.uncertain {
				t.Errorf("AcceptanceUncertain = %v, want %v", outcome.AcceptanceUncertain, tc.uncertain)
			}
		})
	}
}

func TestSendMalformedReceiptInvalidJSON(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindMalformedReceipt {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindMalformedReceipt)
	}
	if !outcome.AcceptanceUncertain {
		t.Error("AcceptanceUncertain = false, want true")
	}
	if outcome.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d, want %d", outcome.HTTPStatus, http.StatusOK)
	}
}

func TestSendMalformedReceiptSimulatorIDMismatch(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		body := fmt.Sprintf(
			`{"simulator_id":"wrong-sim","packet_id":%q,"duplicate_packet":false,"accepted_fixes":%d,"duplicate_fixes":0,"quarantined_fixes":0,"trip_updates":1,"revision":1}`,
			pkt.PacketID, pkt.FixCount,
		)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindMalformedReceipt {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindMalformedReceipt)
	}
}

func TestSendMalformedReceiptCounterMismatch(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// accepted_fixes + duplicate_fixes != pkt.FixCount (which is 2)
		body := fmt.Sprintf(
			`{"simulator_id":%q,"packet_id":%q,"duplicate_packet":false,"accepted_fixes":0,"duplicate_fixes":0,"quarantined_fixes":0,"trip_updates":0,"revision":1}`,
			pkt.SimulatorID, pkt.PacketID,
		)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindMalformedReceipt {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindMalformedReceipt)
	}
}

func TestSendMalformedReceiptNegativeCounters(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		body := fmt.Sprintf(
			`{"simulator_id":%q,"packet_id":%q,"duplicate_packet":false,"accepted_fixes":-1,"duplicate_fixes":0,"quarantined_fixes":0,"trip_updates":0,"revision":1}`,
			pkt.SimulatorID, pkt.PacketID,
		)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindMalformedReceipt {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindMalformedReceipt)
	}
}

func TestSendNoRedirectFollow(t *testing.T) {
	pkt := testPacket(t, EncodingJSON)
	redirectTargetHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirectTargetHit = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(validReceiptBody(pkt)))
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer srv.Close()

	s := NewSender(srv.URL, time.Second)
	outcome := s.Send(context.Background(), pkt)

	if redirectTargetHit {
		t.Fatal("sender followed redirect, expected it not to")
	}
	if !outcome.Failed {
		t.Fatal("expected failure")
	}
	if outcome.FailureKind != FailureKindNon200 {
		t.Errorf("FailureKind = %q, want %q", outcome.FailureKind, FailureKindNon200)
	}
	if outcome.HTTPStatus != http.StatusFound {
		t.Errorf("HTTPStatus = %d, want %d", outcome.HTTPStatus, http.StatusFound)
	}
}
