# GPS Telemetry Demo

A weekend project demonstrating GPS telemetry ingestion, event-time trip reconstruction, and live vehicle visualization. The central scenario is a communications outage: current telemetry resumes first, then delayed fixes fill in history and merge provisional trips.

The backend uses Go and SQLite. The planned dashboard uses React, TypeScript, Vite, and MapLibre GL JS.

## Project status

This README describes the agreed design and intended repository layout. Application commands, flags, configuration loading, and scripts still need to be implemented. The Protobuf setup/generation commands below can be used once the schema and Go module exist.

Implementation order:

1. Aggregator
2. GPS simulator
3. Inspector CLI
4. Minimal dashboard

Track work in [TODO.md](TODO.md). The normative aggregator contract is [AGGREGATOR_SPEC.md](AGGREGATOR_SPEC.md); its `AG-01` through `AG-10` IDs match the checklist. If this README and the detailed spec differ, follow the spec and update this README.

## Architecture

```text
Existing address-to-address route generator
                  |
               fixes.csv
                  |
                  v
           GPS simulator (Go)
                  |
         HTTP JSON or Protobuf
                  |
                  v
           Aggregator (Go) <------ Inspector CLI (Go)
                  |
                SQLite
                  |
       REST snapshots + WebSocket events
                  |
                  v
             Web dashboard
                  |
       Simulator control requests
                  |
       Aggregator control proxy ------> Simulator control API
```

The dashboard talks to the aggregator for both telemetry views and simulator controls. The inspector uses the aggregator's public APIs rather than accessing SQLite directly. SQLite is embedded; no database server is required.

## Core behavior

- Both JSON and binary Protobuf telemetry use `POST /api/v1/telemetry`, selected by `Content-Type`.
- Fix identity is `(vehicle_id, timestamp_ms)`. Packet identity is `(simulator_id, packet_id)`.
- Identical retransmissions are idempotent. Conflicting values for an existing identity are rejected; stored telemetry is never silently overwritten.
- GPS event timestamps are signed 64-bit Unix epoch milliseconds. Arrival time does not determine trip segmentation or current position.
- Consecutive fix gaps of **600 seconds or less** connect by default; larger gaps create separate trips. The threshold is configurable.
- The canonical vehicle position comes from the greatest stored event timestamp, so backlog delivery cannot move it backward in time.
- A vehicle has at most one ACTIVE trip. Older mutable trips are INACTIVE and can receive historical fixes. Silence alone does not close or deactivate a trip.
- Historical fixes may extend or connect mutable trips. A merge creates a new trip ID, freezes predecessors as CLOSED/MERGED, and records lineage.
- Manual closure freezes the trip and creates a hard event-time boundary. Later valid fixes within its closed interval are stored as unassigned/quarantined telemetry. Fixes after its end can start a new trip even within the gap threshold.
- Closure checks internal sampling gaps and requires explicit confirmation to override missing fixes. No detected internal gaps is not proof that all boundary telemetry has arrived.
- Ingestion stores telemetry, reconstructs trips, and advances state atomically before acknowledging success.
- Per-vehicle revisions let clients detect missed WebSocket updates and reload coherent snapshots. Persistent event replay is outside the initial scope.

Trips represent telemetry continuity, not verified vehicle movement.

## Local prerequisites

Already required: the Go toolchain and Git. Install the Protobuf compiler and Go generation plugin on macOS:

```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
```

Ensure the plugin installation directory is on your PATH. With the default Go installation location:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

If `GOBIN` is configured, add that directory instead. Persist the appropriate PATH setting in your shell configuration.

Verify installation:

```bash
go version
protoc --version
protoc-gen-go --version
```

The `@latest` command is for initial setup. Once selected, record and pin generator/runtime versions and record the compiler version so generation is reproducible.

Useful additional tools:

| Tool | Purpose |
|---|---|
| `curl` | Submit telemetry and inspect REST APIs |
| `sqlite3` | Inspect database contents during development |
| `jq` | Optional JSON formatting/filtering |
| Node.js LTS and npm | Build the dashboard; choose a version supported by the selected Vite release |
| Apple Command Line Tools | C compiler support if using a cgo-based SQLite driver; also needed for applicable Go race-test builds |

For optional database/JSON command-line tools:

```bash
brew install sqlite jq
```

If compiler tools are missing on macOS:

```bash
xcode-select --install
```

Choose and pin a SQLite driver during implementation. A cgo-based driver such as `mattn/go-sqlite3` requires a C compiler; a cgo-free driver has different build requirements. React, Vite, TypeScript, MapLibre, Cobra, the Protobuf runtime, and the database driver belong in project dependency manifests. No separate gRPC toolchain is needed for this design.

Tool documentation: [Go Protobuf](https://protobuf.dev/getting-started/gotutorial/), [Protobuf compiler package](https://formulae.brew.sh/formula/protobuf), [SQLite CLI](https://www.sqlite.org/cli.html), [Vite setup](https://vite.dev/guide/).

## Intended repository layout

```text
gps-telemetry-demo/
├── README.md
├── TODO.md
├── AGGREGATOR_SPEC.md
├── go.mod
├── go.sum
├── cmd/
│   ├── aggregator/
│   ├── simulator/
│   └── gpsinspect/
├── proto/telemetry/v1/telemetry.proto
├── gen/telemetry/v1/telemetry.pb.go
├── internal/
│   ├── aggregator/
│   ├── trips/
│   ├── storage/
│   ├── httpapi/
│   ├── events/
│   └── control/
├── migrations/
├── testdata/
├── routes/
│   └── fixes.csv
└── dashboard/
```

Set the module path and the schema's `go_package` consistently. For example, the spec uses module path `gpsdemo` and `go_package = "gpsdemo/gen/telemetry/v1;telemetryv1"`. Replace both together if using a repository-hosted module path.

## Protobuf generation

The `.proto` file is the source schema. Generate Go types from it; do not hand-edit generated `.pb.go` files. Only telemetry needs Protobuf initially; trip and query models use ordinary Go structures.

From the repository root, after creating the module and schema:

```bash
go get google.golang.org/protobuf
mkdir -p gen
protoc \
  --proto_path=proto \
  --go_out=gen \
  --go_opt=paths=source_relative \
  proto/telemetry/v1/telemetry.proto
```

Expected generated file: `gen/telemetry/v1/telemetry.pb.go`.

Use `protojson` for the telemetry JSON encoding and `proto` for binary serialization. Telemetry JSON examples use lowerCamelCase fields and quoted int64 timestamps; nontelemetry REST APIs use snake_case fields and numeric timestamps. See AG-02 and AG-07 for the complete contracts.

## Simulator route input

Use the existing route generator that takes two addresses and writes a CSV of simulated fixes. Building another route generator is outside this project's scope.

Example `routes/fixes.csv`:

```csv
ts,lat,lon,speed_mps,heading_deg,dist_m,speed_source
0.0,40.7440080,-73.9801730,4.09,29.6,0.0,osrm_estimate
1.0,40.7440406,-73.9801489,4.21,29.0,4.2,osrm_estimate
2.0,40.7440737,-73.9801247,4.21,29.0,8.4,osrm_estimate
```

| Column | Meaning |
|---|---|
| `ts` | Elapsed seconds from route origin, including fractional seconds |
| `lat`, `lon` | Latitude/longitude in degrees |
| `speed_mps` | Speed in meters per second |
| `heading_deg` | Heading in degrees |
| `dist_m` | Optional cumulative distance diagnostic; not sent as telemetry |
| `speed_source` | Optional source annotation; not sent as telemetry |

The simulator converts elapsed time to event time:

```text
timestamp_ms = run_start_ms + round(ts × 1000)
```

`ts=0.0` is valid elapsed time, not Unix epoch zero. Resolve the run start once; do not change fix timestamps when batching, pausing transmission, or draining backlog. Preserve row order and timing. Validate the whole CSV before playback and report malformed rows with their locations. The spec defines rounding, required headers, timestamp validation, and handling of optional diagnostic columns.

Planned simulator defaults:

- Expected sampling interval: 1000 ms
- Packet size: 10 fixes
- Clock acceleration: configurable from 10× to 1000×
- Transmission capacity: 50 packets per **wall-clock** second
- Live/backlog scheduling: default 3:1 with guaranteed backlog opportunities
- Packet IDs: UTC `YYYYMMDD-SEQUENCE`, unique within simulator identity across resets/restarts

Transmission pause simulates a coverage outage while generation continues. Stop halts generation and transmission. At 1000× acceleration, 10 fixes per packet requires 100 packets/s just for live traffic; configure more capacity, such as 200 packets/s, to demonstrate backlog drain. Do not silently discard queued fixes. Automatic retry scheduling is deferred, while aggregator idempotency is required now.

## Aggregator configuration

Proposed baseline values; the configuration file/flag interface remains to be implemented:

```yaml
listen: 127.0.0.1:8080
database_path: ./data/gps.sqlite
trip_gap_seconds: 600
expected_fix_interval_ms: 1000
max_body_bytes: 1048576
max_fixes_per_packet: 10000
sqlite_busy_timeout_ms: 5000
websocket_queue_capacity: 256
simulators: []
```

SQLite uses WAL mode, foreign keys, and durable transactions. Trip gap and expected cadence are persisted with the database; changing them requires an explicit migration/rebuild policy rather than silently reinterpreting existing history. Local database files should be excluded from Git. Keep deterministic test fixtures under version control.

## Planned API surface

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/telemetry` | JSON or binary Protobuf ingestion |
| `GET /api/v1/vehicles` | Known vehicles |
| `GET /api/v1/vehicles/{id}` | Latest position, active trip and revision |
| `GET /api/v1/vehicles/{id}/snapshot` | Coherent vehicle/current-trip snapshot |
| `GET /api/v1/vehicles/{id}/trips` | Current trip history; optional merged predecessors |
| `GET /api/v1/vehicles/{id}/fixes` | Raw history, including quarantine |
| `GET /api/v1/trips/{id}` | Trip metadata and lineage |
| `GET /api/v1/trips/{id}/fixes` | Ordered geometry |
| `GET /api/v1/trips/{id}/completeness` | Internal missing-fix assessment |
| `POST /api/v1/trips/{id}/close` | Revision-checked manual closure |
| `WS /api/v1/events?vehicle_id={id}` | Semantic changes with vehicle revisions |
| `GET /api/v1/health` | Aggregator readiness |
| `/api/v1/simulators/...` | Fixed simulator status/config/control proxy routes |

Example JSON ingestion request, **after the aggregator endpoint is implemented**:

```bash
curl --fail-with-body http://127.0.0.1:8080/api/v1/telemetry \
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

Binary packets use the same URL with `Content-Type: application/x-protobuf`. Successful ingestion acknowledges durable acceptance and completed reconstruction; quarantined fixes are reported separately.

## Development and validation

Once the corresponding commands exist, the intended development entry points are:

```bash
go run ./cmd/aggregator
go run ./cmd/simulator
go run ./cmd/gpsinspect --help
```

Required flags/config paths will be documented when implemented. Do not assume these commands currently start a working system.

Backend checks:

```bash
go test ./...
go test -race ./...
```

Prioritize tests for encoding equivalence, idempotency/conflicts, exact 600-second boundaries, shuffled arrival order, merge lineage, manual closure/quarantine, concurrent ingestion/closure, restart durability, and WebSocket snapshot races. Detailed acceptance tests T01–T18 are in the aggregator spec.

The planned `gpsinspect` CLI supports vehicle/trip inspection, completeness analysis, closure, event watching, and invariant validation. Its validator should independently check the public API's results rather than call the aggregator's reconstruction code.

Dashboard setup and run commands will be added after scaffolding. Keep available trips, visible trips, and the selected trip separate in application state; detailed trip visibility UX remains undecided.

## Demonstration scenario

1. Start the aggregator and simulator with a known CSV route.
2. Observe one growing trip and the latest vehicle position.
3. Pause transmission for more than 600 simulated seconds while generation continues.
4. Resume with enough transmission capacity to serve live traffic and drain backlog.
5. Observe a new current trip before delayed data connects the history.
6. Verify a new merged trip ID, CLOSED/MERGED predecessors, preserved lineage, and no regression of the latest event timestamp.
7. Attempt closure while a trip has internal missing fixes; observe the warning, explicitly confirm, and verify frozen history.
8. Deliver a delayed internal fix and a later fix within 600 seconds of the closed end; verify quarantine and a separate future trip.
9. Restart the aggregator and verify persistence; reconnect event clients through a fresh snapshot.

Use fixtures and controlled queue scheduling so the intermediate two-trip state is reproducible. The checklist's E2E tasks define the final integration gate.

## Scope and limitations

This is a local demonstration, without authentication or production deployment infrastructure. The initial version omits persistent event replay, automatic simulator retransmission, source-completeness watermarks, reopening closed trips, movement-based segmentation, map matching, and large-scale incremental reconstruction. Detailed dashboard interactions will be designed after the backend and inspector work.
