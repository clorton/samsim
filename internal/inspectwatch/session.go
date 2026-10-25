package inspectwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gpsdemo/internal/inspectclient"
)

// reconnectReason explains why a fresh snapshot/reconnect is needed,
// surfaced as the NDJSON resync record's "reason" field and in human
// output.
type reconnectReason string

const (
	reasonSocketFailure   reconnectReason = "SOCKET_FAILURE"
	reasonRevisionGap     reconnectReason = "REVISION_GAP"
	reasonDatabaseChanged reconnectReason = "DATABASE_CHANGED"
	reasonOutOfOrder      reconnectReason = "OUT_OF_ORDER"
	reasonBufferOverflow  reconnectReason = "BUFFER_OVERFLOW"
	reasonProtocolError   reconnectReason = "PROTOCOL_ERROR"
)

// sessionEndedError carries the reason a single connection attempt ended,
// so the outer reconnect loop can choose the right resync reason and
// disconnected message without re-parsing a plain error string.
type sessionEndedError struct {
	reason reconnectReason
	err    error
}

func (e *sessionEndedError) Error() string { return fmt.Sprintf("%s: %v", e.reason, e.err) }
func (e *sessionEndedError) Unwrap() error { return e.err }

// Options configures a watch Session.
type Options struct {
	Dialer     Dialer
	RestClient *inspectclient.Client
	VehicleID  string

	// ReconnectAttempts is the number of reconnect attempts permitted after
	// a disconnect; 0 disables reconnect entirely (INS-04 default: 10).
	ReconnectAttempts int
	// BackoffSchedule is the reconnect delay sequence; the last element is
	// reused (capped) for any attempt beyond the schedule's length. Default:
	// 250ms, 500ms, 1s, 2s, 5s.
	BackoffSchedule []time.Duration
	// Sleep is injectable for tests; it must return ctx.Err() promptly if
	// ctx is canceled during the wait (INS-04: "SIGINT cancels waits/IO
	// immediately").
	Sleep func(ctx context.Context, d time.Duration) error
	// BufferSize bounds the pre-snapshot/in-flight envelope buffer (INS-04
	// default: 256).
	BufferSize int
}

func (o *Options) fillDefaults() {
	if len(o.BackoffSchedule) == 0 {
		o.BackoffSchedule = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 5 * time.Second}
	}
	if o.Sleep == nil {
		o.Sleep = defaultSleep
	}
	if o.BufferSize <= 0 {
		o.BufferSize = 256
	}
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Session runs the INS-04 watch state machine.
type Session struct {
	opts Options
}

// NewSession constructs a Session, applying INS-04's documented defaults
// for any zero-valued field in opts except ReconnectAttempts (whose zero
// value, 0, is itself a meaningful "reconnect disabled" setting -- callers
// that want the spec's default of 10 must pass it explicitly).
func NewSession(opts Options) *Session {
	opts.fillDefaults()
	return &Session{opts: opts}
}

// Run drives the watch session, invoking emit for every observable event,
// until ctx is canceled (normal termination, returns ctx.Err()) or
// reconnect attempts are exhausted (returns a non-nil error after emitting
// a terminal Event with Kind==EventError).
func (s *Session) Run(ctx context.Context, emit func(Event)) error {
	attempt := 0
	var pendingResyncReason reconnectReason

	for {
		established, err := s.runOnce(ctx, emit, pendingResyncReason)
		pendingResyncReason = ""

		if established {
			// INS-04: "Reset failure count only after a ready+snapshot session
			// is established."
			attempt = 0
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			// runOnce only returns a nil error when ctx is already done (it
			// checks ctx.Err() on every I/O path before returning nil), so
			// reaching here with a nil ctx.Err() should not happen; treat it
			// defensively as graceful termination anyway.
			return nil
		}

		if e, ok := err.(*sessionEndedError); ok {
			pendingResyncReason = e.reason
		}

		attempt++
		if s.opts.ReconnectAttempts == 0 {
			emit(Event{Kind: EventError, Message: fmt.Sprintf("connection ended and reconnect is disabled: %v", err)})
			return err
		}
		if attempt > s.opts.ReconnectAttempts {
			emit(Event{Kind: EventError, Message: fmt.Sprintf("exhausted %d reconnect attempt(s); last error: %v", s.opts.ReconnectAttempts, err)})
			return err
		}

		emit(Event{Kind: EventDisconnected, Message: err.Error(), Attempt: attempt})

		delay := s.opts.BackoffSchedule[len(s.opts.BackoffSchedule)-1]
		if attempt-1 < len(s.opts.BackoffSchedule) {
			delay = s.opts.BackoffSchedule[attempt-1]
		}
		if serr := s.opts.Sleep(ctx, delay); serr != nil {
			return serr
		}
	}
}

// watchMsg is one decoded frame read off the socket by the reader
// goroutine.
type watchMsg struct {
	raw []byte
}

// runOnce dials one connection, performs the ready/snapshot handshake, and
// processes frames until the connection ends for any reason. It returns
// whether a snapshot was successfully installed during this attempt
// (regardless of how it later ended), which the caller uses to decide
// whether to reset its reconnect-attempt counter. A nil error return always
// means the caller's ctx was already done; any other termination reason is
// returned as a non-nil error (always *sessionEndedError) so Run's
// reconnect loop can act on it.
func (s *Session) runOnce(ctx context.Context, emit func(Event), pendingResyncReason reconnectReason) (established bool, err error) {
	conn, derr := s.opts.Dialer.Dial(ctx, s.opts.VehicleID)
	if derr != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		return false, &sessionEndedError{reason: reasonSocketFailure, err: derr}
	}
	defer conn.Close()

	msgCh := make(chan watchMsg, s.opts.BufferSize)
	errCh := make(chan error, 1)
	go func() {
		for {
			data, rerr := conn.ReadMessage(ctx)
			if rerr != nil {
				errCh <- rerr
				return
			}
			select {
			case msgCh <- watchMsg{raw: data}:
			default:
				errCh <- errBufferOverflow
				return
			}
		}
	}()

	recv := func() (watchMsg, error) {
		select {
		case <-ctx.Done():
			return watchMsg{}, ctx.Err()
		case m := <-msgCh:
			return m, nil
		case e := <-errCh:
			return watchMsg{}, e
		}
	}

	// Step 1: the first application frame must be "ready".
	first, rerr := recv()
	if rerr != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		return false, &sessionEndedError{reason: reasonSocketFailure, err: rerr}
	}
	var ready readyFrame
	if err := json.Unmarshal(first.raw, &ready); err != nil || ready.Type != "ready" {
		return false, &sessionEndedError{reason: reasonProtocolError, err: fmt.Errorf("expected ready frame first, got: %s", string(first.raw))}
	}

	registeredD := ready.DatabaseID
	registeredVehicle := ready.VehicleID
	expectedNext := ready.Revision + 1

	// Step 2: drain any frames already queued (e.g. an envelope the
	// transport delivered immediately after ready, before we get to fetch
	// the snapshot below) so none are lost to the race between "ready
	// arrived" and "snapshot fetched".
	var pending []envelopeFrame
	drainPending := func() error {
		for {
			select {
			case m := <-msgCh:
				var ef envelopeFrame
				if err := json.Unmarshal(m.raw, &ef); err != nil {
					return &sessionEndedError{reason: reasonProtocolError, err: fmt.Errorf("malformed envelope frame: %w", err)}
				}
				pending = append(pending, ef)
				if len(pending) > s.opts.BufferSize {
					return &sessionEndedError{reason: reasonBufferOverflow, err: errBufferOverflow}
				}
			default:
				return nil
			}
		}
	}

	// Step 3: fetch the initial snapshot. Unknown vehicle (404) emits
	// "waiting" and defers snapshot installation until the first observed
	// event, per spec.
	snap, serr := s.opts.RestClient.GetSnapshot(ctx, s.opts.VehicleID)
	if serr != nil {
		if !inspectclient.IsCode(serr, "NOT_FOUND") {
			if ctx.Err() != nil {
				return false, nil
			}
			return false, &sessionEndedError{reason: reasonSocketFailure, err: serr}
		}
		emit(Event{Kind: EventWaiting, VehicleID: registeredVehicle, Message: "vehicle does not exist yet; waiting for first event"})

		for snap == nil {
			if err := drainPending(); err != nil {
				return false, err
			}
			if len(pending) == 0 {
				m, rerr := recv()
				if rerr != nil {
					if ctx.Err() != nil {
						return false, nil
					}
					return false, &sessionEndedError{reason: reasonSocketFailure, err: rerr}
				}
				var ef envelopeFrame
				if err := json.Unmarshal(m.raw, &ef); err != nil {
					return false, &sessionEndedError{reason: reasonProtocolError, err: fmt.Errorf("malformed envelope frame: %w", err)}
				}
				pending = append(pending, ef)
			}
			// An event arrived; the vehicle must now exist.
			snap, serr = s.opts.RestClient.GetSnapshot(ctx, s.opts.VehicleID)
			if serr != nil {
				if !inspectclient.IsCode(serr, "NOT_FOUND") {
					if ctx.Err() != nil {
						return false, nil
					}
					return false, &sessionEndedError{reason: reasonSocketFailure, err: serr}
				}
				// Still not found; keep waiting for more events.
				continue
			}
		}
	}

	if err := drainPending(); err != nil {
		return false, err
	}

	if snap.DatabaseID != registeredD {
		return false, &sessionEndedError{reason: reasonDatabaseChanged, err: fmt.Errorf("snapshot database_id %q does not match ready frame's %q", snap.DatabaseID, registeredD)}
	}

	installedRevision := snap.Revision
	expectedNext = installedRevision + 1

	if pendingResyncReason != "" {
		emit(Event{Kind: EventResync, Reason: string(pendingResyncReason)})
	}
	emit(Event{
		Kind: EventSnapshot, DatabaseID: snap.DatabaseID, VehicleID: registeredVehicle, Revision: installedRevision,
		Snapshot: snap,
	})

	// Discard buffered revisions <= R, then process the remainder in order,
	// enforcing strict consecutiveness.
	for _, ef := range pending {
		if ok, endErr := s.applyEnvelope(emit, registeredD, registeredVehicle, &expectedNext, ef); !ok {
			return true, endErr
		}
	}

	// Step 4: steady-state processing loop.
	for {
		m, rerr := recv()
		if rerr != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			return true, &sessionEndedError{reason: reasonSocketFailure, err: rerr}
		}
		var ef envelopeFrame
		if err := json.Unmarshal(m.raw, &ef); err != nil {
			return true, &sessionEndedError{reason: reasonProtocolError, err: fmt.Errorf("malformed envelope frame: %w", err)}
		}
		if ok, endErr := s.applyEnvelope(emit, registeredD, registeredVehicle, &expectedNext, ef); !ok {
			return true, endErr
		}
	}
}

// applyEnvelope validates and (if valid) emits one envelope frame, updating
// *expectedNext. Returns ok=false (with endErr set) if the frame requires
// ending the connection (database change, gap, or out-of-order).
func (s *Session) applyEnvelope(emit func(Event), registeredD, vehicleID string, expectedNext *int64, ef envelopeFrame) (ok bool, endErr error) {
	if ef.DatabaseID != registeredD {
		return false, &sessionEndedError{reason: reasonDatabaseChanged, err: fmt.Errorf("event database_id %q does not match registered %q", ef.DatabaseID, registeredD)}
	}
	if ef.Revision < *expectedNext {
		return true, nil // already covered by the installed snapshot; ignore
	}
	if ef.Revision > *expectedNext {
		return false, &sessionEndedError{reason: reasonRevisionGap, err: fmt.Errorf("expected revision %d, got %d", *expectedNext, ef.Revision)}
	}
	emit(Event{
		Kind: EventEnvelope, DatabaseID: ef.DatabaseID, VehicleID: vehicleID, Revision: ef.Revision,
		Envelope: toEnvelopeData(ef),
	})
	*expectedNext = ef.Revision + 1
	return true, nil
}

var errBufferOverflow = errors.New("watch event buffer overflow")
