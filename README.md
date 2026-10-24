# GPS Telemetry Demo

A weekend project demonstrating GPS telemetry ingestion, event-time trip reconstruction, and live vehicle visualization. The central scenario is a communications outage: current telemetry resumes first, then delayed fixes fill in history and merge provisional trips.

The backend uses Go and SQLite. The planned dashboard uses React, TypeScript, Vite, and MapLibre GL JS.

## Project status

**The aggregator (AG-01 through AG-09) is implemented and runnable.** AG-10 (verification/operations) is substantially covered by the test suite below; see "Development and validation" for what remains. The GPS simulator, inspector CLI, and dashboard are not yet implemented.

Implementation order:

1. **Aggregator — done** (this README's "Running the aggregator" section below is accurate and tested)
2. GPS simulator — not started
3. Inspector CLI — not started
4. Minimal dashboard — not started

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

Required: the Go toolchain (1.23+; developed and tested on 1.26). `protoc` and `protoc-gen-go` are only needed if you change `proto/telemetry/v1/telemetry.proto` and must regenerate `gen/telemetry/v1/telemetry.pb.go`; the generated file is already checked in, so a normal build/test/run does not need them.

To regenerate after editing the `.proto` file:

```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
export PATH="$PATH:$(go env GOPATH)/bin"
```

Verify installation:

```bash
go version
protoc --version
protoc-gen-go --version
```

Useful additional tools:

| Tool | Purpose |
|---|---|
| `curl` | Submit telemetry and inspect REST APIs |
| `sqlite3` | Inspect database contents during development |
| `jq` | Optional JSON formatting/filtering |
| Node.js LTS and npm | Build the dashboard (not yet implemented) |

```bash
brew install sqlite jq
```

**Dependency choices actually used** (see `go.mod`): `modernc.org/sqlite` (pure Go, no cgo/C compiler required), `github.com/coder/websocket` (WebSocket server/client), `gopkg.in/yaml.v3` (config file parsing), `google.golang.org/protobuf` (generated code runtime + protojson).

## Repository layout

```text
samsim/
├── README.md
├── TODO.md
├── AGGREGATOR_SPEC.md
├── aggregator.md         # curl/wget/browser endpoint reference
├── go.mod
├── go.sum
├── config.example.yaml
├── cmd/
│   ├── aggregator/        # implemented: main.go wires config/storage/service/HTTP
│   └── simulator/         # stub only; not yet implemented
├── proto/telemetry/v1/telemetry.proto
├── gen/telemetry/v1/telemetry.pb.go   # generated; do not hand-edit
├── internal/
│   ├── aggregator/        # transactional services: ingest, trip reconstruction, close, queries
│   ├── trips/             # pure event-time segmentation/merge + completeness logic (no I/O)
│   ├── storage/           # SQLite migrations, writer coordinator, repositories
│   ├── wire/              # AG-02 wire codec: strict JSON/protobuf decode, validate, normalize, fingerprint
│   ├── httpapi/           # net/http handlers, DTOs, router, WebSocket endpoint
│   ├── events/            # ordered post-commit WebSocket broadcaster (Hub)
│   ├── control/           # AG-09 simulator control-plane proxy
│   ├── apierr/            # standard {"error":{...}} envelope and codes
│   ├── config/            # YAML config loading + validation
│   ├── clock/             # injectable clock (System / Fixed)
│   └── idgen/             # injectable trip-ID generator (UUID / Sequential)
├── migrations/            # embedded ordered *.sql migrations
├── routes/
│   └── sample-route.json  # placeholder; simulator route input format still TBD
└── dashboard/             # not yet implemented
```

Module path is `gpsdemo`, matching the proto's `go_package = "gpsdemo/gen/telemetry/v1;telemetryv1"`.

## Protobuf generation

The `.proto` file is the source schema; `gen/telemetry/v1/telemetry.pb.go` is generated from it and checked in. Do not hand-edit it. Only telemetry wire types use Protobuf; trip/query DTOs are ordinary Go structs (`internal/aggregator/dto*.go`).

To regenerate after editing the schema, from the repository root:

```bash
protoc \
  --proto_path=proto \
  --go_out=gen \
  --go_opt=paths=source_relative \
  proto/telemetry/v1/telemetry.proto
```

Use `protojson` for the telemetry JSON encoding and `proto` for binary serialization (both implemented in `internal/wire/codec.go`). Telemetry JSON uses lowerCamelCase fields and quoted int64 timestamps; nontelemetry REST APIs use snake_case fields and numeric timestamps. See AG-02 and AG-07 for the complete contracts.

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

**Note:** the CSV format and simulator defaults above describe the *planned* SIM-01..08 implementation, which does not exist yet. `routes/sample-route.json` is a placeholder and not consumed by anything currently.

## Running the aggregator

Build and run with built-in defaults (listens on `127.0.0.1:8080`, database at `./data/gps.sqlite`):

```bash
go run ./cmd/aggregator
```

Or with an explicit config file:

```bash
go run ./cmd/aggregator -config config.example.yaml
```

See `config.example.yaml` for every field and its default. Key points:

- `trip_gap_seconds` and `expected_fix_interval_ms` are frozen into the database on first run; starting the same database file again with different values is a startup error (AG-01).
- `simulators: []` (the default) means the `/api/v1/simulators*` control-proxy routes are not registered at all; add entries to enable them (AG-09).
- Binds to loopback by default; there is no authentication. This is a local demo.

The server logs structured JSON to stderr and shuts down gracefully on `SIGINT`/`SIGTERM` (finishes in-flight requests, then closes the database).

## API surface (implemented)

For copy-pasteable `curl`/`wget`/browser examples of every endpoint below (including request/response bodies, query parameters, and a worked end-to-end session), see [aggregator.md](aggregator.md).

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/telemetry` | JSON or binary Protobuf ingestion |
| `GET /api/v1/vehicles` | Known vehicles, paginated |
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
| `GET /api/v1/simulators` | Configured simulator IDs/hints (only if `simulators` is nonempty) |
| `GET /api/v1/simulators/{id}/status` | Proxies simulator `GET /status` |
| `PUT /api/v1/simulators/{id}/config` | Proxies simulator `PUT /config` |
| `POST /api/v1/simulators/{id}/{action}` | Proxies `start`/`stop`/`pause-transmission`/`resume-transmission`/`reset` |

All non-GET methods on a registered path, and GET on a POST-only path, return `405` with an `Allow` header and the standard error envelope.

Example JSON ingestion request:

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

Close a trip (requires the current vehicle revision, obtained from an ingest response or `GET .../snapshot`):

```bash
curl -X POST http://127.0.0.1:8080/api/v1/trips/<trip-id>/close \
  -H 'Content-Type: application/json' \
  --data '{"expected_revision":1,"force":false}'
```

If the trip has internal gaps, this returns `409 TRIP_INCOMPLETE` with a completeness assessment; resubmit with `"force":true` after reviewing it.

## Development and validation

```bash
go build ./...
go test ./...
go test -race ./...
```

As of this writing, both commands pass cleanly across all implemented packages (120+ tests). Test coverage by spec section:

| Package | Covers |
|---|---|
| `internal/trips` | AG-05 segmentation/merge algorithm, AG-06 completeness math (pure, no I/O) — T05, T06, T08, T10 |
| `internal/wire` | AG-02 decode/validate/normalize/fingerprint — T01, T02 |
| `internal/storage` | AG-03 migrations, metadata freezing, writer coordinator, restart persistence |
| `internal/events` | AG-08 Hub: delivery, overflow-closes-socket, unsubscribe |
| `internal/control` | AG-09 proxy: allowlist, timeout, upstream errors, no redirects — T17 |
| `internal/aggregator` | AG-04 ingestion/idempotency, AG-05 reconstruction integration, AG-06 closure, AG-07 queries/pagination, concurrency, restart, precommit crash-injection, throughput smoke — T03, T04, T07, T09, T11–T15 |
| `internal/httpapi` | HTTP/WebSocket wiring: content negotiation, error envelope, AG-08 ready/envelope protocol — T16 |

**Precommit crash-injection (T14):** `storage.Store.SetPrecommitFailureHook` installs a test-only hook invoked inside `WithWriteTx` after a transaction's writes are staged but before commit. `TestRestart_PrecommitFailureLeavesNoTrace` and `TestClose_PrecommitFailureLeavesNoTrace` (in `internal/aggregator`) use it to verify a failure at that point rolls back completely — no partial packet/fix/trip/barrier/revision — and that a subsequent retry succeeds cleanly as a fresh attempt.

**Throughput smoke test:** `TestSmoke_IngestThroughput` (in `internal/aggregator`) submits 10-fix packets at a target rate and reports actual throughput/latency percentiles; it only fails on an ingestion *error*, never on falling short of the target rate, per the spec's guidance not to sacrifice durability for a number. Default run is 3s at a 1000 fixes/s target (skipped under `go test -short`); override for a heavier run:

```bash
SMOKE_DURATION_SECONDS=30 SMOKE_FIXES_PER_SEC=1000 go test ./internal/aggregator/ -run TestSmoke -v
```

T18 (outage/merge/restart through the simulator) is blocked on SIM-01..08, which do not exist yet.

## Decision log (deviations from/clarifications to AGGREGATOR_SPEC.md)

- **SQLite driver:** `modernc.org/sqlite` (pure Go), not a cgo driver, so `go build`/`go test` need no C compiler and work identically in sandboxed environments.
- **WebSocket library:** `github.com/coder/websocket`, chosen for its context-based API, which maps cleanly onto the per-write deadlines AG-08 requires.
- **Package split:** the spec's `internal/httpapi` ("codecs, handlers, DTOs, WebSocket") was split into `internal/wire` (pure AG-02 codec/validation/fingerprint, no HTTP or storage dependency) and `internal/httpapi` (actual `net/http` handlers/DTOs/router/WebSocket, depends on `internal/aggregator`). This avoids an import cycle, since AG-07 handlers call into the aggregator service, which itself needs the AG-02 codec for ingestion.
- **Additional small packages** not explicitly named in AG-01's layout: `internal/clock` (injectable clock), `internal/idgen` (injectable trip-ID generator), `internal/apierr` (shared error codes/envelope), `internal/config` (YAML loading/validation). These are reusable primitives with no cross-dependencies, not restructured domain logic.
- **Router:** Go 1.22+ `net/http.ServeMux` wildcard path patterns (`{id}`), registered without a method prefix; each handler checks `r.Method` itself and returns the standard JSON error envelope on mismatch (ServeMux's built-in 405 is plain text, which would violate AG-07's consistent-error-shape requirement).
- **AG-08 subscribe-ready race:** implemented via `storage.Store.WithWriterLock` + `ReadUnderWriterLock`, which reads the vehicle's current revision on the *write* connection (not the read pool, to avoid any replication lag) while holding the same mutex `WithWriteTx` uses, and registers the Hub subscription before releasing it. This guarantees no commit can land between "read ready revision" and "start receiving future commits."

## Scope and limitations

This is a local demonstration, without authentication or production deployment infrastructure. Deliberately deferred per the spec: persistent event replay, automatic simulator retransmission, source-completeness watermarks, reopening closed trips, movement-based segmentation, map matching, and large-scale incremental reconstruction. The GPS simulator, inspector CLI, and dashboard are not yet implemented (see "Project status" above).
