package inspectclient

import (
	"context"
)

// CoherentAttemptFunc performs the reads needed for one report, all pinned
// at revision r of database d, returning the collected report value. It
// must use AtRevision: &r on every applicable request. Returning a
// *Error with Kind==KindAPI and Code=="REVISION_CONFLICT" triggers a
// fresh attempt (see RunCoherent); any other error aborts immediately.
type CoherentAttemptFunc[T any] func(ctx context.Context, d string, r int64) (T, error)

// ErrRevisionAttemptsExhausted is returned by RunCoherent when no
// consistent revision could be collected within the configured attempt
// budget (INS spec section 4: "Exhaustion returns code 5").
type ErrRevisionAttemptsExhausted struct {
	Attempts int
}

func (e *ErrRevisionAttemptsExhausted) Error() string {
	return "could not collect a consistent revision within the configured attempt budget"
}

// RunCoherent implements the shared coherent-read protocol (INSPECTOR_SPEC
// section 4) for a single-vehicle report:
//
//  1. Read the vehicle snapshot to obtain database_id D and revision R.
//  2. Run attempt(ctx, D, R), which performs all further reads pinned at R.
//  3. Reread the vehicle snapshot; if D or R changed, discard the whole
//     attempt and restart from step 1.
//  4. On a REVISION_CONFLICT from within attempt, likewise discard and
//     restart.
//
// At most maxAttempts complete attempts are made; exhaustion returns
// *ErrRevisionAttemptsExhausted. No sleep is performed between attempts, per
// spec ("no sleep is required").
func RunCoherent[T any](ctx context.Context, c *Client, vehicleID string, maxAttempts int, attempt CoherentAttemptFunc[T]) (result T, databaseID string, revision int64, err error) {
	var zero T
	for i := 0; i < maxAttempts; i++ {
		snap, serr := c.GetSnapshot(ctx, vehicleID)
		if serr != nil {
			return zero, "", 0, serr
		}
		d := snap.DatabaseID
		r := snap.Revision

		val, aerr := attempt(ctx, d, r)
		if aerr != nil {
			if IsCode(aerr, "REVISION_CONFLICT") {
				continue // discard entire attempt, restart from a fresh snapshot
			}
			return zero, "", 0, aerr
		}

		// Reread to confirm no commit landed during collection.
		recheck, rerr := c.GetVehicle(ctx, vehicleID, &r)
		if rerr != nil {
			if IsCode(rerr, "REVISION_CONFLICT") {
				continue
			}
			return zero, "", 0, rerr
		}
		if recheck.DatabaseID != d {
			continue // database changed mid-collection; restart
		}

		return val, d, r, nil
	}
	return zero, "", 0, &ErrRevisionAttemptsExhausted{Attempts: maxAttempts}
}

// RunCoherentForTrip implements the trip-id variant of the coherent-read
// protocol: first GET the trip (any revision) to discover its vehicle_id,
// then run the ordinary per-vehicle protocol, then refetch the trip pinned
// at R so the final report's trip detail is consistent with everything
// else. The trip is refetched by ID (not redirected to a successor), per
// spec: "IDs are stable even after merges; never silently redirect a trip
// command to its successor."
func RunCoherentForTrip[T any](ctx context.Context, c *Client, tripID string, maxAttempts int, attempt func(ctx context.Context, d string, r int64, trip *TripDetail) (T, error)) (result T, databaseID string, revision int64, err error) {
	var zero T
	discovery, derr := c.GetTripDetail(ctx, tripID, nil)
	if derr != nil {
		return zero, "", 0, derr
	}
	vehicleID := discovery.VehicleID

	return RunCoherent(ctx, c, vehicleID, maxAttempts, func(ctx context.Context, d string, r int64) (T, error) {
		trip, terr := c.GetTripDetail(ctx, tripID, &r)
		if terr != nil {
			return zero, terr
		}
		return attempt(ctx, d, r, trip)
	})
}

// PageCollector pages through a cursor-based list, pinned at a fixed
// revision, detecting cursor cycles and enforcing a maximum item count
// (INS spec section 4: "Cap finite list collection at 100000 items").
type PageCollector struct {
	MaxItems int
	seen     map[string]bool
}

// NewPageCollector returns a PageCollector bounded at maxItems (if <= 0, a
// default of 100000 is used, per spec).
func NewPageCollector(maxItems int) *PageCollector {
	if maxItems <= 0 {
		maxItems = 100000
	}
	return &PageCollector{MaxItems: maxItems, seen: map[string]bool{}}
}

// CheckCursor records cursor and reports a cycle-detected protocol error if
// it has been seen before in this collection.
func (p *PageCollector) CheckCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if p.seen[cursor] {
		return protocolError("pagination cursor cycle detected")
	}
	p.seen[cursor] = true
	return nil
}

// CheckCount reports a protocol error if the running total exceeds
// MaxItems.
func (p *PageCollector) CheckCount(total int) error {
	if total > p.MaxItems {
		return protocolError("collection exceeded the maximum of %d items", p.MaxItems)
	}
	return nil
}
