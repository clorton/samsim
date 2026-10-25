package inspectoutput

import (
	"encoding/json"
	"io"
)

// NDJSON record types used by watch (INSPECTOR_SPEC.md section 7).
const (
	RecordSnapshot     = "snapshot"
	RecordEvent        = "event"
	RecordResync       = "resync"
	RecordWaiting      = "waiting"
	RecordDisconnected = "disconnected"
	RecordError        = "error"
)

// SnapshotRecord/EventRecord carry full vehicle-scoped data; ResyncRecord,
// WaitingRecord, DisconnectedRecord and ErrorRecord are lighter-weight
// status records. All share schema_version/record_type so a consumer can
// dispatch on record_type alone.
type SnapshotRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	DatabaseID    string `json:"database_id"`
	VehicleID     string `json:"vehicle_id"`
	Revision      int64  `json:"revision"`
	Data          any    `json:"data"`
}

type EventRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	DatabaseID    string `json:"database_id"`
	VehicleID     string `json:"vehicle_id"`
	Revision      int64  `json:"revision"`
	Data          any    `json:"data"`
}

type ResyncRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	Reason        string `json:"reason"`
}

type WaitingRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	VehicleID     string `json:"vehicle_id"`
	Message       string `json:"message"`
}

type DisconnectedRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	Message       string `json:"message"`
	Attempt       int    `json:"attempt,omitempty"`
}

type ErrorRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	Message       string `json:"message"`
}

// NDJSONWriter writes one JSON value per line to w, flushing after each
// write so a consumer sees records promptly.
type NDJSONWriter struct {
	w io.Writer
}

func NewNDJSONWriter(w io.Writer) *NDJSONWriter { return &NDJSONWriter{w: w} }

func (n *NDJSONWriter) Write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = n.w.Write(data)
	return err
}
