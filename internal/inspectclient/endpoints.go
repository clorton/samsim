package inspectclient

import (
	"context"
	"net/url"
	"strconv"
)

// ListVehiclesParams holds GET /api/v1/vehicles query parameters.
type ListVehiclesParams struct {
	ActiveOnly bool
	Limit      int
	Cursor     string
}

func (c *Client) ListVehicles(ctx context.Context, p ListVehiclesParams) (*ListEnvelope[VehicleListItem], error) {
	q := url.Values{}
	if p.ActiveOnly {
		q.Set("active_only", "true")
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	var out ListEnvelope[VehicleListItem]
	if err := c.getJSON(ctx, []string{"api", "v1", "vehicles"}, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetVehicle fetches GET /api/v1/vehicles/{id}, optionally pinned at a
// revision.
func (c *Client) GetVehicle(ctx context.Context, vehicleID string, atRevision *int64) (*VehicleDTO, error) {
	q := url.Values{}
	if atRevision != nil {
		q.Set("at_revision", strconv.FormatInt(*atRevision, 10))
	}
	var out VehicleDTO
	if err := c.getJSON(ctx, []string{"api", "v1", "vehicles", vehicleID}, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSnapshot fetches GET /api/v1/vehicles/{id}/snapshot.
func (c *Client) GetSnapshot(ctx context.Context, vehicleID string) (*SnapshotDTO, error) {
	var out SnapshotDTO
	if err := c.getJSON(ctx, []string{"api", "v1", "vehicles", vehicleID, "snapshot"}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTripsParams holds GET /api/v1/vehicles/{id}/trips query parameters.
type ListTripsParams struct {
	IncludeMerged bool
	Status        string
	Limit         int
	Cursor        string
	AtRevision    *int64
}

func (c *Client) ListTrips(ctx context.Context, vehicleID string, p ListTripsParams) (*ListEnvelope[TripSummary], error) {
	q := url.Values{}
	if p.IncludeMerged {
		q.Set("include_merged", "true")
	}
	if p.Status != "" {
		q.Set("status", p.Status)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.AtRevision != nil {
		q.Set("at_revision", strconv.FormatInt(*p.AtRevision, 10))
	}
	var out ListEnvelope[TripSummary]
	if err := c.getJSON(ctx, []string{"api", "v1", "vehicles", vehicleID, "trips"}, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListFixesParams holds GET /api/v1/vehicles/{id}/fixes query parameters.
type ListFixesParams struct {
	FromMs     *int64
	ToMs       *int64
	Assignment string
	Limit      int
	Cursor     string
	AtRevision *int64
}

func (c *Client) ListVehicleFixes(ctx context.Context, vehicleID string, p ListFixesParams) (*ListEnvelope[FixDTO], error) {
	q := url.Values{}
	if p.FromMs != nil {
		q.Set("from_ms", strconv.FormatInt(*p.FromMs, 10))
	}
	if p.ToMs != nil {
		q.Set("to_ms", strconv.FormatInt(*p.ToMs, 10))
	}
	if p.Assignment != "" {
		q.Set("assignment", p.Assignment)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.AtRevision != nil {
		q.Set("at_revision", strconv.FormatInt(*p.AtRevision, 10))
	}
	var out ListEnvelope[FixDTO]
	if err := c.getJSON(ctx, []string{"api", "v1", "vehicles", vehicleID, "fixes"}, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTripDetail fetches GET /api/v1/trips/{id}, optionally pinned at a
// revision.
func (c *Client) GetTripDetail(ctx context.Context, tripID string, atRevision *int64) (*TripDetail, error) {
	q := url.Values{}
	if atRevision != nil {
		q.Set("at_revision", strconv.FormatInt(*atRevision, 10))
	}
	var out TripDetail
	if err := c.getJSON(ctx, []string{"api", "v1", "trips", tripID}, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTripFixes fetches GET /api/v1/trips/{id}/fixes, optionally pinned at a
// revision.
func (c *Client) GetTripFixes(ctx context.Context, tripID string, atRevision *int64) (*ListEnvelope[FixDTO], error) {
	q := url.Values{}
	if atRevision != nil {
		q.Set("at_revision", strconv.FormatInt(*atRevision, 10))
	}
	var out ListEnvelope[FixDTO]
	if err := c.getJSON(ctx, []string{"api", "v1", "trips", tripID, "fixes"}, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCompleteness fetches GET /api/v1/trips/{id}/completeness.
func (c *Client) GetCompleteness(ctx context.Context, tripID string) (*CompletenessDTO, error) {
	var out CompletenessDTO
	if err := c.getJSON(ctx, []string{"api", "v1", "trips", tripID, "completeness"}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseRequest is the POST /api/v1/trips/{id}/close request body.
type CloseRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Force            bool  `json:"force"`
}

// CloseTrip submits POST /api/v1/trips/{id}/close.
func (c *Client) CloseTrip(ctx context.Context, tripID string, req CloseRequest) (*CloseResult, error) {
	var out CloseResult
	if err := c.postJSON(ctx, []string{"api", "v1", "trips", tripID, "close"}, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Health fetches GET /api/v1/health.
func (c *Client) Health(ctx context.Context) (*HealthStatus, error) {
	var out HealthStatus
	if err := c.getJSON(ctx, []string{"api", "v1", "health"}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
