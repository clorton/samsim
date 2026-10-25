package inspector

import (
	"context"

	"gpsdemo/internal/inspectclient"
)

// TripReport is the "trip" command's data payload.
type TripReport struct {
	DatabaseID string                   `json:"database_id"`
	Revision   int64                    `json:"revision"`
	Detail     inspectclient.TripDetail `json:"detail"`
}

// RunTrip implements `gpsinspect trip <trip-id>` (INS-02): reads trip
// detail and completeness at a coherent revision via the trip-id variant of
// the shared protocol (first discover vehicle_id, then pin to a revision,
// then refetch the trip at that revision). Never follows successor
// identity silently; the requested trip ID is what gets reported, even if
// it has since been merged (its own CLOSED/MERGED status/successor fields
// convey that).
func RunTrip(ctx context.Context, a *App, tripID string) (*TripReport, error) {
	detail, d, r, err := inspectclient.RunCoherentForTrip(ctx, a.Client, tripID, a.Config.RevisionAttempts,
		func(ctx context.Context, d string, r int64, trip *inspectclient.TripDetail) (inspectclient.TripDetail, error) {
			if trip.Completeness == nil {
				return inspectclient.TripDetail{}, &inspectclient.Error{
					Kind:    inspectclient.KindProtocol,
					Message: "server omitted required completeness detail for trip " + tripID,
				}
			}
			return *trip, nil
		})
	if err != nil {
		return nil, err
	}
	return &TripReport{DatabaseID: d, Revision: r, Detail: detail}, nil
}
