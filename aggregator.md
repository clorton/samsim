# Aggregator endpoint reference

A practical reference for exercising the running aggregator from a browser, `curl`, or `wget` — no client code required. For the normative behavior behind these endpoints, see [AGGREGATOR_SPEC.md](AGGREGATOR_SPEC.md) (`AG-07` for reads, `AG-06` for closure, `AG-08` for the WebSocket feed, `AG-09` for the simulator proxy).

Start the server first:

```bash
go run ./cmd/aggregator
# or: go run ./cmd/aggregator -config config.example.yaml
```

By default it listens on `http://127.0.0.1:8080` and binds to loopback only (no auth, not for network exposure).

All examples below assume `BASE=http://127.0.0.1:8080`.

## Quick start: GET endpoints in a browser

Every `GET` endpoint below returns JSON and can be opened directly in a browser address bar, e.g.:

```
http://127.0.0.1:8080/api/v1/health
http://127.0.0.1:8080/api/v1/vehicles
```

Browsers pretty-print JSON by default in most cases, or install a JSON-viewer extension. `POST`/`PUT` endpoints (ingestion and closure) need `curl`, `wget --post-data`, or a REST client — a browser URL bar can't send a request body.

## Response and error shape

Every error response (any non-2xx) uses the same envelope:

```json
{"error":{"code":"NOT_FOUND","message":"vehicle \"x\" not found","details":{}}}
```

`details` is omitted or empty for most errors; some codes (`TRIP_INCOMPLETE`, `FIX_CONFLICT`, `REVISION_CONFLICT`) include extra structured fields there.

| HTTP status | Codes you may see |
|---|---|
| 400 | `INVALID_REQUEST`, `INVALID_TELEMETRY` |
| 404 | `NOT_FOUND` |
| 405 | `METHOD_NOT_ALLOWED` (also sets an `Allow` response header) |
| 409 | `PACKET_CONFLICT`, `FIX_CONFLICT`, `REVISION_CONFLICT`, `TRIP_INCOMPLETE`, `TRIP_SUPERSEDED` |
| 413 | `BODY_TOO_LARGE`, `SNAPSHOT_TOO_LARGE` |
| 415 | `UNSUPPORTED_MEDIA_TYPE`, `UNSUPPORTED_CONTENT_ENCODING` |
| 502 | `SIMULATOR_UNAVAILABLE` |
| 503 | `STORAGE_UNAVAILABLE` |
| 504 | `SIMULATOR_TIMEOUT` |

All non-telemetry JSON (everything except the body of `POST /api/v1/telemetry`) uses `snake_case` field names and plain numeric timestamps/revisions.

---

## Health

### `GET /api/v1/health`

No parameters. Returns 200 while the database/writer are usable, otherwise the connection will simply fail (the process reports unhealthy by refusing to serve, per spec; there is currently no separate always-up health probe since the whole server is in one process).

```bash
curl -s $BASE/api/v1/health
```

```json
{"healthy":true,"database_id":"84da7393-...","trip_gap_seconds":600}
```

---

## Telemetry ingestion

### `POST /api/v1/telemetry`

The only write endpoint for GPS data. Accepts either JSON (`Content-Type: application/json`) or binary Protobuf (`Content-Type: application/x-protobuf`) — same URL, same semantics, selected by header.

**This cannot be exercised from a browser URL bar** (it's a POST with a body). Use `curl` or `wget`.

JSON body shape (note: **lowerCamelCase** field names here, unlike every other endpoint — this one mirrors the Protobuf schema's `protojson` convention):

```json
{
  "simulatorId": "sim-001",
  "vehicleId": "vehicle-001",
  "packetId": "20261024-000001",
  "fixes": [
    {
      "timestampMs": "1792845600000",
      "latitude": 40.744008,
      "longitude": -73.980173,
      "speedMps": 4.09,
      "headingDegrees": 29.6
    }
  ]
}
```

Field notes:
- `timestampMs` is a **string** (int64-as-string is accepted and is how protojson represents it; a plain JSON number also works).
- `packetId` must match `YYYYMMDD-SEQUENCE` (e.g. `20261024-000001`); the date need not match the fix timestamps — it describes when the packet was created.
- `simulatorId`/`vehicleId`: 1–128 chars, `[A-Za-z0-9._-]` only.
- Up to 10000 fixes per packet by default (`max_fixes_per_packet` in config).

curl:

```bash
curl -s -X POST $BASE/api/v1/telemetry \
  -H 'Content-Type: application/json' \
  --data-binary '{
    "simulatorId":"sim-001",
    "vehicleId":"vehicle-001",
    "packetId":"20261024-000001",
    "fixes":[{
      "timestampMs":"1792845600000",
      "latitude":40.7440080,
      "longitude":-73.9801730,
      "speedMps":4.09,
      "headingDegrees":29.6
    }]
  }'
```

wget equivalent:

```bash
wget -qO- --header='Content-Type: application/json' \
  --post-data='{"simulatorId":"sim-001","vehicleId":"vehicle-001","packetId":"20261024-000001","fixes":[{"timestampMs":"1792845600000","latitude":40.744008,"longitude":-73.980173,"speedMps":4.09,"headingDegrees":29.6}]}' \
  $BASE/api/v1/telemetry
```

Response (200):

```json
{"simulator_id":"sim-001","packet_id":"20261024-000001","duplicate_packet":false,"accepted_fixes":1,"duplicate_fixes":0,"quarantined_fixes":0,"trip_updates":1,"revision":1}
```

Submitting the *exact same* packet again returns `"duplicate_packet":true` with the original counters — it's safe to retry.

---

## Vehicles

### `GET /api/v1/vehicles`

List all known vehicles.

| Query param | Meaning |
|---|---|
| `active_only=true` | Only vehicles with a non-null active trip |
| `limit=N` | Page size, default 100, max 1000 |
| `cursor=...` | Opaque pagination cursor from a previous response's `next_cursor` |

```bash
curl -s $BASE/api/v1/vehicles
curl -s "$BASE/api/v1/vehicles?active_only=true&limit=10"
```

```json
{"items":[{"vehicle_id":"vehicle-001","active_trip_id":"2b659be1-...","revision":1}],"next_cursor":null,"database_id":"84da7393-..."}
```

### `GET /api/v1/vehicles/{id}`

Latest known fix, active trip pointer, revision, and effective segmentation config for one vehicle.

| Query param | Meaning |
|---|---|
| `at_revision=N` | Precondition: 409 `REVISION_CONFLICT` if current revision ≠ N (not time travel — just a consistency check) |

```bash
curl -s $BASE/api/v1/vehicles/vehicle-001
```

```json
{"vehicle_id":"vehicle-001","latest_fix":{"timestamp_ms":1792845600000,"latitude":40.744008,"longitude":-73.980173,"speed_mps":4.09,"heading_degrees":29.6,"current_trip_id":"2b659be1-...","assignment_reason":null},"active_trip_id":"2b659be1-...","revision":1,"database_id":"84da7393-...","trip_gap_seconds":600,"expected_fix_interval_ms":1000}
```

Unknown vehicle → 404 (reads never create vehicles).

### `GET /api/v1/vehicles/{id}/snapshot`

One coherent read-transaction view: the vehicle, every current trip summary, and all manual barriers, all at one consistent revision. **This is the best starting point** for exploring a vehicle's state in a browser.

```bash
curl -s $BASE/api/v1/vehicles/vehicle-001/snapshot
```

```json
{
  "vehicle": {"vehicle_id":"vehicle-001", "...": "..."},
  "trips": [{"trip_id":"2b659be1-...","start_ms":1792845600000,"end_ms":1792845600000,"fix_count":1,"bounds":{"min_latitude":40.744008,"max_latitude":40.744008,"min_longitude":-73.980173,"max_longitude":-73.980173},"status":"ACTIVE","close_reason":"NONE","superseded_by_trip_id":null,"created_revision":1,"updated_revision":1,"closed_at_ms":null,"forced_close":false}],
  "manual_barriers": [],
  "revision": 1,
  "database_id": "84da7393-..."
}
```

### `GET /api/v1/vehicles/{id}/trips`

List the vehicle's trips.

| Query param | Meaning |
|---|---|
| `include_merged=true` | Also include CLOSED/MERGED predecessor trips (audit history); default excludes them |
| `status=ACTIVE\|INACTIVE\|CLOSED` | Filter by status |
| `limit=N`, `cursor=...` | Pagination (cursor is bound to the vehicle's revision; becomes stale if ingestion advances it mid-page) |
| `at_revision=N` | Consistency precondition |

```bash
curl -s $BASE/api/v1/vehicles/vehicle-001/trips
curl -s "$BASE/api/v1/vehicles/vehicle-001/trips?include_merged=true"
curl -s "$BASE/api/v1/vehicles/vehicle-001/trips?status=ACTIVE"
```

### `GET /api/v1/vehicles/{id}/fixes`

All raw fixes for a vehicle, including quarantined ones, each with `current_trip_id` (null if unassigned) and `assignment_reason` (`"MANUAL_CLOSED_INTERVAL"` or null).

| Query param | Meaning |
|---|---|
| `from_ms=N`, `to_ms=N` | Inclusive timestamp range filter |
| `assignment=assigned\|quarantined\|all` | Default `all` |
| `limit=N`, `cursor=...` | Pagination |
| `at_revision=N` | Consistency precondition |

```bash
curl -s $BASE/api/v1/vehicles/vehicle-001/fixes
curl -s "$BASE/api/v1/vehicles/vehicle-001/fixes?assignment=quarantined"
```

---

## Trips

### `GET /api/v1/trips/{id}`

Full trip detail: everything in the trip summary (see snapshot above) plus lineage and a completeness assessment.

```bash
curl -s $BASE/api/v1/trips/<trip-id>
```

```json
{
  "trip_id": "2b659be1-...", "...": "(all TripSummary fields)",
  "immediate_predecessor_trip_ids": [],
  "immediate_successor_trip_id": null,
  "terminal_successor_trip_id": null,
  "completeness": {"trip_id":"2b659be1-...","revision":1,"expected_fix_interval_ms":1000,"missing_fix_count":0,"missing_intervals":null,"complete":true,"boundary_completeness":"UNKNOWN","assessment":"NO_INTERNAL_GAPS"}
}
```

### `GET /api/v1/trips/{id}/fixes`

That trip's own frozen chronological fix list (for a CLOSED/MERGED trip, this is its original predecessor geometry, unaffected by the merge).

```bash
curl -s $BASE/api/v1/trips/<trip-id>/fixes
```

### `GET /api/v1/trips/{id}/completeness`

Just the completeness assessment (same object embedded in the trip detail above). `"complete":true` means "no detected internal gaps" — it does **not** certify that all telemetry has arrived (no boundary guarantee).

```bash
curl -s $BASE/api/v1/trips/<trip-id>/completeness
```

### `POST /api/v1/trips/{id}/close`

Manually close a trip. Requires the vehicle's **current revision** (get it from a snapshot or ingest response first) as an optimistic-concurrency precondition.

```bash
curl -s -X POST $BASE/api/v1/trips/<trip-id>/close \
  -H 'Content-Type: application/json' \
  --data '{"expected_revision":1,"force":false}'
```

wget:

```bash
wget -qO- --header='Content-Type: application/json' \
  --post-data='{"expected_revision":1,"force":false}' \
  $BASE/api/v1/trips/<trip-id>/close
```

Possible outcomes:
- **200** — closed. Response includes the completeness assessment used for the decision.
- **409 `TRIP_INCOMPLETE`** — the trip has internal gaps and `force` was `false`. Response `details` includes the completeness assessment and `"requires_confirmation":true`. Resubmit with `"force":true` after reviewing it.
- **409 `REVISION_CONFLICT`** — `expected_revision` doesn't match the vehicle's current revision (even with `force:true`). Re-fetch the snapshot and retry with the current revision.
- **409 `TRIP_SUPERSEDED`** — the trip was already merged into a successor; `details.successor_trip_id` names it.
- **200 (no-op)** — closing an already-CLOSED/MANUAL trip is idempotent; safe to retry after a lost response.

---

## Live updates: WebSocket events

### `WS /api/v1/events?vehicle_id={id}`

Not fetchable with `curl`/`wget`/a browser URL bar — this is a WebSocket upgrade. Use a WebSocket-capable tool. Two easy options:

**Browser devtools console**, on any page (even `about:blank`, if your browser allows it — otherwise open the aggregator's own origin first, e.g. navigate to `http://127.0.0.1:8080/api/v1/health` then open devtools):

```javascript
const ws = new WebSocket("ws://127.0.0.1:8080/api/v1/events?vehicle_id=vehicle-001");
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

**`websocat`** (install via `brew install websocat` or `cargo install websocat`):

```bash
websocat "ws://127.0.0.1:8080/api/v1/events?vehicle_id=vehicle-001"
```

Protocol:
1. First frame is always `{"type":"ready","database_id":"...","vehicle_id":"vehicle-001","revision":N}` — `N` is the vehicle's revision *right now* (0 if the vehicle doesn't exist yet).
2. Every subsequent frame is `{"event_id":"...","database_id":"...","vehicle_id":"...","revision":N+1,"type":"vehicle.changed","timestamp_ms":...,"changes":[...]}` — one envelope per committed, state-changing ingestion or closure, with revisions strictly contiguous from the ready frame.
3. If you miss a frame, disconnect, or the server closes the socket (code `1013`, queue overflow), reconnect and re-fetch `GET /api/v1/vehicles/{id}/snapshot` to resync — don't try to patch state from a gap.

`changes[].type` is one of: `trip.created`, `trip.updated`, `trip.closed`, `trips.merged`, `vehicle.updated`, `telemetry.quarantined`.

---

## Simulator control proxy (optional)

Only registered if `config.yaml` has a non-empty `simulators:` list (see `config.example.yaml`). With no simulators configured, these routes return 404 (not registered at all).

### `GET /api/v1/simulators`

```bash
curl -s $BASE/api/v1/simulators
```

```json
{"items":[{"simulator_id":"sim-001","vehicle_id":"vehicle-001"}]}
```

### `GET /api/v1/simulators/{id}/status`

Proxies the simulator's own `GET /status`.

```bash
curl -s $BASE/api/v1/simulators/sim-001/status
```

### `PUT /api/v1/simulators/{id}/config`

Proxies `PUT /config` on the simulator, forwarding your request body.

```bash
curl -s -X PUT $BASE/api/v1/simulators/sim-001/config \
  -H 'Content-Type: application/json' --data '{"...": "..."}'
```

### `POST /api/v1/simulators/{id}/{action}`

`{action}` must be one of `start`, `stop`, `pause-transmission`, `resume-transmission`, `reset`. Any other action name → 404.

```bash
curl -s -X POST $BASE/api/v1/simulators/sim-001/start
curl -s -X POST $BASE/api/v1/simulators/sim-001/pause-transmission
```

Proxy-specific errors: `502 SIMULATOR_UNAVAILABLE` (simulator unreachable), `504 SIMULATOR_TIMEOUT` (no response within 3s).

**Note:** the GPS simulator itself is not yet implemented (see `TODO.md` SIM-01..08), so these proxy routes have nothing to talk to yet unless you point `control_url` at your own test HTTP server implementing `/status`, `/config`, `/start`, etc.

---

## Worked example: a complete manual session

```bash
BASE=http://127.0.0.1:8080

# 1. Confirm the server is up.
curl -s $BASE/api/v1/health

# 2. Send two fixes 1 second apart.
curl -s -X POST $BASE/api/v1/telemetry -H 'Content-Type: application/json' --data-binary '{
  "simulatorId":"sim-001","vehicleId":"vehicle-001","packetId":"20261024-000001",
  "fixes":[
    {"timestampMs":"1792845600000","latitude":40.744008,"longitude":-73.980173,"speedMps":4.09,"headingDegrees":29.6},
    {"timestampMs":"1792845601000","latitude":40.744041,"longitude":-73.980149,"speedMps":4.21,"headingDegrees":29.0}
  ]
}'
# -> {"...", "revision":1, ...}

# 3. View the vehicle's current state.
curl -s $BASE/api/v1/vehicles/vehicle-001/snapshot | python3 -m json.tool
# find "trip_id" in the output, e.g. "2b659be1-7422-45c9-9fe7-65b038686071"

# 4. Check completeness before closing.
curl -s $BASE/api/v1/trips/2b659be1-7422-45c9-9fe7-65b038686071/completeness

# 5. Close the trip (using the revision from step 2's response, here 1).
curl -s -X POST $BASE/api/v1/trips/2b659be1-7422-45c9-9fe7-65b038686071/close \
  -H 'Content-Type: application/json' --data '{"expected_revision":1,"force":false}'
```
