package aggregator

import (
	"context"
	"database/sql"
	"errors"

	"gpsdemo/internal/events"
	"gpsdemo/internal/storage"
)

// SubscribeReady implements the server side of AG-08's subscribe-ready
// handshake. It determines the ready revision for vehicleID (0 if the
// vehicle does not exist yet) and registers the Hub subscription, both
// while holding the storage writer coordinator lock, so that no commit can
// land between the revision read and the subscription registration: the
// caller is guaranteed either to see a change in the ready revision, or to
// receive it via the returned subscriber, never neither.
func (s *Service) SubscribeReady(ctx context.Context, vehicleID string) (int64, *events.Subscriber, error) {
	var revision int64
	var sub *events.Subscriber
	err := s.Store.WithWriterLock(func() error {
		r, err := storage.ReadUnderWriterLock(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (int64, error) {
			v, err := storage.GetVehicle(ctx, tx, vehicleID)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return 0, nil
				}
				return 0, err
			}
			return v.Revision, nil
		})
		if err != nil {
			return err
		}
		revision = r
		sub = s.Hub.Subscribe(vehicleID)
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return revision, sub, nil
}
