# GPS Telemetry Demo

**Dashboard implemented:** see [dashboard/README.md](dashboard/README.md) for local startup, CSV uploads, simulator controls, live map, history and the database explorer. Start the aggregator with `config.dashboard.example.yaml` to enable its simulator registry.

A weekend project demonstrating GPS telemetry ingestion, event-time trip reconstruction, and live vehicle visualization. The central scenario is a communications outage: current telemetry resumes first, then delayed fixes fill in history and merge provisional trips.

The backend uses Go and SQLite. The planned dashboard uses React, TypeScript, Vite, and MapLibre GL JS.

## Project status

**The aggregator (AG-01 through AG-10), the GPS simulator (SIM-01 through SIM-07), and the inspector CLI `gpsinspect` (INS-01 through INS-06) are implemented and runnable**, including a real, automated end-to-end integration test (the simulator driving a real aggregator through a >600-second outage, producing two trips that later merge with correct lineage) and a second real end-to-end test exercising `gpsinspect` itself against a real aggregator through that same outage/merge scenario plus a forced close and a saved immutability baseline. The dashboard is not yet implemented.

Implementation order:

1. **Aggregator — done** (see "Running the aggregator" below)
2. **GPS simulator — done** (see "Running the simulator" below)
3. **Inspector CLI — done** (see "Using the inspector" below)
4. Minimal dashboard — not started

Track work in [TODO.md](TODO.md). The normative contracts are [specs/AGGREGATOR_SPEC.md](specs/AGGREGATOR_SPEC.md), [specs/SIMULATOR_SPEC.md](specs/SIMULATOR_SPEC.md), and [specs/INSPECTOR_SPEC.md](specs/INSPECTOR_SPEC.md); their `AG-*`/`SIM-*`/`INS-*` IDs match the checklist. If this README and a detailed spec differ, follow the spec and update this README.

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
├── aggregator.md             # curl/wget/browser endpoint reference
├── inspector.md              # aggregator + simulator + gpsinspect walkthrough
├── specs/
│   ├── AGGREGATOR_SPEC.md
│   ├── SIMULATOR_SPEC.md
│   └── INSPECTOR_SPEC.md
├── go.mod
├── go.sum
├── config.example.yaml       # aggregator config
├── simulator.example.json    # simulator config
├── cmd/
│   ├── aggregator/            # main.go wires config/storage/service/HTTP
│   ├── simulator/             # main.go wires config/identity store/engine/HTTP
│   └── gpsinspect/            # main.go wires the Cobra command tree (INS-01)
├── proto/telemetry/v1/telemetry.proto
├── gen/telemetry/v1/telemetry.pb.go   # generated; do not hand-edit
├── internal/
│   ├── aggregator/            # transactional services: ingest, trip reconstruction, close, queries
│   ├── trips/                 # pure event-time segmentation/merge + completeness logic (no I/O)
│   ├── storage/               # SQLite migrations, writer coordinator, repositories
│   ├── wire/                  # AG-02 wire codec: strict JSON/protobuf decode, validate, normalize, fingerprint
│   ├── httpapi/               # aggregator net/http handlers, DTOs, router, WebSocket endpoint
│   ├── events/                # ordered post-commit WebSocket broadcaster (Hub)
│   ├── control/               # AG-09 simulator control-plane proxy
│   ├── apierr/                # aggregator's standard {"error":{...}} envelope and codes
│   ├── config/                # aggregator YAML config loading + validation
│   ├── clock/                 # injectable wall clock (System / Fixed), used by the aggregator
│   ├── idgen/                 # injectable trip-ID generator (UUID / Sequential)
│   ├── simulator/             # the whole SIM-01..07 engine: CSV loader, virtual clock, packet
│   │                          # encoder/sender, identity store, state machine, scheduler, HTTP control API
│   ├── inspectclient/         # gpsinspect's REST client: DTOs, errors, pagination, coherent-read protocol
│   ├── inspector/             # gpsinspect command services: reports, close workflow, shared App wiring
│   ├── inspectvalidate/       # gpsinspect's independent V01..V10 reference model + baseline file IO
│   ├── inspectwatch/          # gpsinspect's AG-08 watch state machine (ready/snapshot/reconnect)
│   └── inspectoutput/         # gpsinspect's JSON/NDJSON envelopes, tables, exit codes
├── migrations/                # embedded ordered *.sql migrations (aggregator)
├── routes/
│   ├── fixes.csv              # real, runnable sample route (300 1-second rows)
│   └── sample-route.json      # unrelated placeholder from early scaffolding; unused
├── testdata/
│   ├── cannonball.csv, bendtobothell.csv, crosscountry.csv, tennessee.csv  # real route fixtures
│   ├── simulator/
│   └── inspector/
└── dashboard/                 # not yet implemented
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

The simulator implements this CSV contract exactly as described above (`internal/simulator/csv.go`), including the decimal-safe half-up rounding, strict monotonic timestamp checks, and header-based column mapping. `routes/fixes.csv` is a real, runnable sample route checked in for convenience; `routes/sample-route.json` is an unrelated placeholder from early scaffolding and is not consumed by anything.

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

## Running the simulator

Build and run against a config file (there is no default; `-config` is required):

```bash
go run ./cmd/simulator -config simulator.example.json
```

See `simulator.example.json` for the complete field list; it points at the checked-in `routes/fixes.csv` and a local identity database at `./data/simulator-identity.sqlite` (a small SQLite file, independent of the aggregator's database, used only to durably allocate packet sequence numbers across restarts — see `internal/simulator/identity.go`). No telemetry generation starts automatically; the simulator comes up in `READY` and waits for `POST /start`.

Key points:

- **Memory-only queues**: live/backlog queues and in-progress run state live in memory only. A clean shutdown waits for any in-flight send and warns if unsent telemetry remains (requiring an explicit `reset`/`resume` before it will exit); a crash or `kill -9` can still lose queued-but-unacknowledged fixes. Only the packet-ID counters are durable. This is explicitly out of scope for P0 (SIM-08 defers a durable outbox).
- **No automatic retry**: a failed send blocks all further sending (`BLOCKED_ERROR`) until an operator explicitly calls `POST /resume-transmission` with `{"retry_failed":true}`.
- `clock_multiplier` must be in `[10,1000]`. Two independent schedules scale with it: `normal_report_interval_simulated_ms` (when live reports are *created*, default 10000ms = one report per 10 simulated seconds) becomes `normal_report_interval_simulated_ms / clock_multiplier` actual milliseconds between reports, while `max_packets_per_simulated_second` (the combined live+backlog send *capacity* ceiling, default 0.2) becomes `max_packets_per_simulated_second * clock_multiplier` actual packets/s. These are deliberately separate: raising capacity (e.g. during backlog recovery) never changes how often live reports are normally created. See `specs/SIMULATOR_CHANGE_REQUEST.md` for the full rationale.
- Binds to loopback by default; no auth. The aggregator's `/api/v1/simulators/*` proxy (AG-09) is the intended way to reach it from a dashboard.

Worked example — start, inspect, and drive an outage/recovery cycle:

```bash
curl -s -X POST http://127.0.0.1:8090/start
curl -s http://127.0.0.1:8090/status

# Simulate a coverage outage: fix generation continues, but nothing is sent.
curl -s -X POST http://127.0.0.1:8090/pause-transmission

# Let more than 600 simulated seconds pass (at 100x, that's just 6 real
# seconds), then restore coverage. One fresh live report is preferred
# first (without blocking backlog if it isn't ready yet); the aggregator
# will show a new ACTIVE trip once it lands. The pre-outage trip stays
# INACTIVE until backlog drain bridges the gap and the aggregator merges
# them into one trip with the old ones marked CLOSED/MERGED.
curl -s -X POST http://127.0.0.1:8090/resume-transmission

# If a send ever fails (e.g. aggregator briefly down), it blocks here:
curl -s http://127.0.0.1:8090/status   # transmission_state: "BLOCKED_ERROR"
curl -s -X POST http://127.0.0.1:8090/resume-transmission \
  -H 'Content-Type: application/json' --data '{"retry_failed":true}'
```

### Simulator API surface (implemented)

| Endpoint | Purpose |
|---|---|
| `GET /status` | Full coherent snapshot: playback/transmission state, report-window and queue counts, target/observed rates, congestion, last error |
| `PUT /config` | Partial, validated, atomically-applied config update (hot fields apply anytime; others require specific lifecycle states) |
| `POST /start` | Start a fresh run (from `READY`) or resume after `/stop` (from `STOPPED`); idempotent while running |
| `POST /stop` | Freeze the virtual clock and stop new send attempts; retains all queues |
| `POST /pause-transmission` | Simulate a coverage outage: move all unsent live fixes to backlog, keep generating into backlog |
| `POST /resume-transmission` | Clear the outage; optional `{"retry_failed":true}` explicitly retries a blocked packet |
| `POST /reset` | Return to `READY`, clearing run state; optional `{"discard_pending":true}` to discard unsent telemetry |

All bodies and responses use `snake_case` JSON with numeric epoch milliseconds, and the same `{"error":{"code":...}}` envelope shape as the aggregator. See `specs/SIMULATOR_SPEC.md` section 8 for the complete request/response contract, or drive it through the aggregator's proxy routes documented above (`GET/PUT/POST /api/v1/simulators/{id}/...`), which forward to exactly these six endpoints.

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

## Using the inspector

`gpsinspect` is a standalone CLI that talks exclusively to the aggregator's public HTTP/WebSocket APIs (never SQLite directly) to inspect vehicles/trips, watch live events, safely perform manual closure, and independently validate reconstruction invariants. For a full step-by-step walkthrough (build, run an aggregator and simulator, reproduce the outage/merge scenario, validate, and close a trip, with real verified output), see [inspector.md](inspector.md).

Build and run:

```bash
go build -o bin/gpsinspect ./cmd/gpsinspect
./bin/gpsinspect --help
```

Or run directly without building:

```bash
go run ./cmd/gpsinspect vehicles
```

Global flags (all optional): `--base-url` (default `http://127.0.0.1:8080`, env `GPSINSPECT_BASE_URL`), `--timeout` (default `5s`, env `GPSINSPECT_TIMEOUT`), `--json` (NDJSON for `watch`), `--page-size`, `--revision-attempts`, `--max-fixes`, `--max-response-bytes`, `--no-color`. See `specs/INSPECTOR_SPEC.md` section 2 for full defaults/validation.

```bash
# List vehicles, inspect one, and browse its trip history.
gpsinspect vehicles
gpsinspect vehicle vehicle-001
gpsinspect trips vehicle-001 --include-merged
gpsinspect trip <trip-id>
gpsinspect gaps <trip-id>

# Watch live semantic events (ready/snapshot handshake, auto-reconnect).
gpsinspect watch vehicle-001
gpsinspect watch vehicle-001 --json   # NDJSON, one record per line

# Manually close a trip (mutating). Trips with no detected internal gaps
# close immediately; trips with gaps require --force or an interactive
# y/yes confirmation.
gpsinspect close <trip-id>
gpsinspect close <trip-id> --force

# Independently validate reconstruction invariants (read-only, V01..V10).
gpsinspect validate vehicle-001
gpsinspect validate vehicle-001 --write-baseline before.json
gpsinspect validate vehicle-001 --baseline before.json
```

`validate` runs ten independent checks (V01 raw identity through V10 observed monotonicity) built from a from-scratch reference model -- it deliberately does not import or call any aggregator reconstruction code, so it cannot reproduce the same defect it is checking for. Without `--baseline`, the two cross-run checks (V09 immutability, V10 monotonicity) report `SKIP` and the overall result is `PASS_WITH_LIMITATIONS` rather than a bare `PASS`, to avoid implying more was proven than was. See `specs/INSPECTOR_SPEC.md` section 8 for the full check table.

Exit codes: `0` success (or validate with no FAIL), `1` API/network/protocol/IO failure, `2` usage error, `3` validate found a FAIL, `4` close canceled/confirmation required, `5` a consistent revision could not be collected (or a close's revision went stale), `130` SIGINT.

## Development and validation

```bash
go build ./...
go test ./...
go test -race ./...
```

As of this writing, both commands pass cleanly across all implemented packages (343 tests). Test coverage by spec section:

| Package | Covers |
|---|---|
| `internal/trips` | AG-05 segmentation/merge algorithm, AG-06 completeness math (pure, no I/O) — T05, T06, T08, T10 |
| `internal/wire` | AG-02 decode/validate/normalize/fingerprint — T01, T02 |
| `internal/storage` | AG-03 migrations, metadata freezing, writer coordinator, restart persistence |
| `internal/events` | AG-08 Hub: delivery, overflow-closes-socket, unsubscribe |
| `internal/control` | AG-09 proxy: allowlist, timeout, upstream errors, no redirects — T17 |
| `internal/aggregator` | AG-04 ingestion/idempotency, AG-05 reconstruction integration, AG-06 closure, AG-07 queries/pagination, concurrency, restart, precommit crash-injection, throughput smoke — T03, T04, T07, T09, T11–T15 |
| `internal/httpapi` | HTTP/WebSocket wiring: content negotiation, error envelope, AG-08 ready/envelope protocol — T16 |
| `internal/simulator` | SIM-01..07: CSV contract and timestamp rebasing (S01-S03), packet identity/encoding (S04-S05), lifecycle (S06-S07), outage/recovery queues and the recovery gate (S08-S09), weighted fairness (S10), rate limiting (S11), retry/failure/quarantine semantics (S12-S13), hot config updates (S14), randomized queue-conservation invariants (S15), overload/congestion (S16), and real end-to-end runs against an actual aggregator instance (S17-S18) |
| `cmd/gpsinspect` | INS-01..04/06: flag precedence and validation, human/JSON output contract, pagination/cursor-cycle detection, the at-revision coherent-read protocol, trip lineage display, and the full close workflow (I01-I08), plus a real end-to-end test (I16) |
| `internal/inspectwatch` | INS-04: AG-08 ready/snapshot handshake, covered-event discarding, revision-gap/database-change resync, reconnect backoff/exhaustion, unknown-vehicle waiting, and prompt SIGINT cancellation (I09-I10) |
| `internal/inspectvalidate` | INS-05: the independent V01..V10 reference model and baseline file format, exercised with hand-built fixtures (not aggregator-generated) covering correct cases, one-at-a-time corruptions, gap-threshold boundaries, lineage cycles, and baseline immutability/monotonicity/IO edge cases (I11-I15) |

**Precommit crash-injection (T14):** `storage.Store.SetPrecommitFailureHook` installs a test-only hook invoked inside `WithWriteTx` after a transaction's writes are staged but before commit. `TestRestart_PrecommitFailureLeavesNoTrace` and `TestClose_PrecommitFailureLeavesNoTrace` (in `internal/aggregator`) use it to verify a failure at that point rolls back completely — no partial packet/fix/trip/barrier/revision — and that a subsequent retry succeeds cleanly as a fresh attempt.

**Throughput smoke test:** `TestSmoke_IngestThroughput` (in `internal/aggregator`) submits 10-fix packets at a target rate and reports actual throughput/latency percentiles; it only fails on an ingestion *error*, never on falling short of the target rate, per the spec's guidance not to sacrifice durability for a number. Default run is 3s at a 1000 fixes/s target (skipped under `go test -short`); override for a heavier run:

```bash
SMOKE_DURATION_SECONDS=30 SMOKE_FIXES_PER_SEC=1000 go test ./internal/aggregator/ -run TestSmoke -v
```

**End-to-end simulator↔aggregator integration (S17/S18, T18, E2E-01):** `TestS17_BothEncodingsWorkAgainstRealAggregator`, `TestS17_RetryAfterLostResponseChangesStateExactlyOnce`, and `TestS18_OutageProducesTwoTripsThenMerge` (in `internal/simulator`) spin up a real aggregator (`httptest.Server` wrapping the actual `storage`/`aggregator`/`httpapi` packages against a temp SQLite file, not a mock) and drive a real `Engine` against it over HTTP. S18 in particular reproduces E2E-01's full scenario end to end: normal playback, a simulated outage past 600 simulated seconds, live-first resume, two current trips observed via the aggregator's own query API, then backlog drain producing a merged trip with both predecessors marked `MERGED` and correct lineage, with canonical position never regressing. This was also verified manually against the built binaries (see "Running the simulator" above).

**End-to-end gpsinspect↔aggregator integration (I16, G5, E2E-01/02):** `TestI16_ListInspectOutageRecoveryValidateMergedForceClose` (in `cmd/gpsinspect`) spins up the same kind of real aggregator and drives it using only `gpsinspect` commands (plus plain HTTP telemetry POSTs standing in for the simulator, exactly as AG-02 defines the wire contract) — never SQLite, never aggregator-internal packages. It walks through: listing/inspecting a vehicle, an outage producing two current trips, a historical bridge producing a `MERGED` lineage, `validate` passing cleanly on the merged state, a second vehicle with an internal completeness gap where an unforced `close` is correctly rejected (exit 4) and an explicit `--force` close succeeds, and finally `validate --write-baseline`/`--baseline` round-tripping with V09/V10 both passing. This satisfies G5 ("inspect and validate outage/merge/forced-close scenarios without opening SQLite") end to end, not just through fixtures.

## Decision log (deviations from/clarifications to specs/AGGREGATOR_SPEC.md)

- **SQLite driver:** `modernc.org/sqlite` (pure Go), not a cgo driver, so `go build`/`go test` need no C compiler and work identically in sandboxed environments.
- **WebSocket library:** `github.com/coder/websocket`, chosen for its context-based API, which maps cleanly onto the per-write deadlines AG-08 requires.
- **Package split:** the spec's `internal/httpapi` ("codecs, handlers, DTOs, WebSocket") was split into `internal/wire` (pure AG-02 codec/validation/fingerprint, no HTTP or storage dependency) and `internal/httpapi` (actual `net/http` handlers/DTOs/router/WebSocket, depends on `internal/aggregator`). This avoids an import cycle, since AG-07 handlers call into the aggregator service, which itself needs the AG-02 codec for ingestion.
- **Additional small packages** not explicitly named in AG-01's layout: `internal/clock` (injectable clock), `internal/idgen` (injectable trip-ID generator), `internal/apierr` (shared error codes/envelope), `internal/config` (YAML loading/validation). These are reusable primitives with no cross-dependencies, not restructured domain logic.
- **Router:** Go 1.22+ `net/http.ServeMux` wildcard path patterns (`{id}`), registered without a method prefix; each handler checks `r.Method` itself and returns the standard JSON error envelope on mismatch (ServeMux's built-in 405 is plain text, which would violate AG-07's consistent-error-shape requirement).
- **AG-08 subscribe-ready race:** implemented via `storage.Store.WithWriterLock` + `ReadUnderWriterLock`, which reads the vehicle's current revision on the *write* connection (not the read pool, to avoid any replication lag) while holding the same mutex `WithWriteTx` uses, and registers the Hub subscription before releasing it. This guarantees no commit can land between "read ready revision" and "start receiving future commits."

## Decision log (simulator, specs/SIMULATOR_SPEC.md)

- **Independent packages, zero aggregator coupling:** `internal/simulator` does not import any aggregator-specific package (`internal/apierr`, `internal/storage`, `internal/aggregator`, `internal/wire`). It depends only on the shared `gen/telemetry/v1` protobuf types, the standard library, `modernc.org/sqlite` (for the identity store), and `google.golang.org/protobuf`. It has its own small `apierr.go` with its own error codes/envelope, deliberately not shared with the aggregator's `internal/apierr`, since the two processes' error vocabularies are independent and coupling them would create an unnecessary cross-component dependency.
- **Dual-mutex `Engine`:** `mu` guards all mutable run state (queues, counters, pending packet) for brief critical sections; a separate `controlMu` serializes whole control-plane operations (`Start`/`Stop`/`PauseTransmission`/`ResumeTransmission`/`Reset`/`PutConfig`) against each other so their own off-lock phases (route parsing, building a new `Sender`) cannot interleave, without ever blocking `GET /status` or the background tick loop.
- **Async send dispatch:** `Tick()` only ever decides *whether* to start a send attempt while holding `mu`; the actual identity allocation and HTTP POST (`dispatchSend`) always run in a separate goroutine with no lock held, so a slow or stuck aggregator can never stall route generation or control-API responsiveness. An earlier version called `dispatchSend` inline from `Tick()`, which blocked the whole tick loop on every send; this was caught and fixed during testing (see `state.go`'s `Tick` doc comment).
- **Fresh-first preference armed at resume, not pause (supersedes the original recovery gate):** `PauseTransmission` only decides whether to move live records (including anything still accumulating in the open report window) into backlog; it does **not** decide whether a post-outage fresh-first preference will be needed. The preference is armed inside `ResumeTransmission`, based on whether backlog is nonempty and the route is not yet exhausted *at that moment*. This mirrors a real bug found via the `TestS18_OutageProducesTwoTripsThenMerge` end-to-end test against the original gate design: arming it only when `PauseTransmission` observed a nonempty live queue meant that if live had already drained to empty by the time an operator called pause (common, since generation and transmission are decoupled), but backlog went on to accumulate while paused, the gate never armed at all, so resume drained backlog seamlessly with no live-first boundary and the aggregator only ever saw one continuous trip instead of two. See `lifecycle.go`.
- **`Reset` counter hygiene:** `Reset` zeroes every counter, including `discardedFixesCount`, each time it runs. An earlier version left `discardedFixesCount` accumulating across repeated resets, which broke the invariant `generated == acknowledged + discarded + unsent` checked by `TestEngine_QueueInvariant_RandomizedOperations`; the randomized test caught it immediately. See `lifecycle.go`.
- **Windowed reporting supersedes packet-size/partial-flush emission (`specs/SIMULATOR_CHANGE_REQUEST.md`):** live report *creation* is now a pure function of simulated time (`normal_report_interval_simulated_ms`, half-open `[start,end)` windows, no empty reports), fully decoupled from transmission *capacity* (`max_packets_per_simulated_second`, scaled by `clock_multiplier` into the actual enforced wall-clock rate). `packet_size` remains only as a safety bound that can split an oversized closed window into multiple packets at closure time. The old `partial_flush_wall_ms` field and the hard recovery gate (`recovery_waiting_for_live`, which blocked all sends including backlog until a post-outage live fix existed) are both removed; the replacement `fresh_first_pending` preference expresses the same "show the freshest position first" intent without ever blocking an eligible backlog send (SIMULATOR_CHANGE_REQUEST.md: "Remove any gate that blocks backlog waiting for a future live fix"). Changing the report interval at runtime requires stopped playback and seals the current partial window once, anchoring the new schedule at the current virtual time, rather than rewriting queued packets. See `state.go`, `sending.go`, `lifecycle.go`, `config_update.go`.

## Decision log (inspector, specs/INSPECTOR_SPEC.md)

- **Independent DTOs, not shared aggregator types:** `internal/inspectclient` declares its own REST DTOs (`TripSummary`, `FixDTO`, `CompletenessDTO`, etc.) rather than importing `internal/aggregator`'s. The inspector is a pure HTTP client with zero dependency on aggregator-internal packages, matching the independence already established for `internal/simulator`. `internal/inspectvalidate` goes further, as the spec requires: it builds its own from-scratch reference model (gap-threshold component splitting, manual-interval derivation, completeness math) rather than importing `internal/trips`, so `validate` cannot reproduce a defect in the code it exists to check.
- **Five focused packages, not one:** `internal/inspectclient` (HTTP/coherent-read protocol), `internal/inspector` (command services: reports, the close workflow, shared `App` wiring), `internal/inspectvalidate` (V01..V10 model + baseline IO), `internal/inspectwatch` (the AG-08 watch state machine), and `internal/inspectoutput` (JSON/NDJSON envelopes, tables, exit codes) — split so the watch protocol and validation model, in particular, are unit-testable with fakes/fixtures with no HTTP server or Cobra command tree involved at all.
- **Per-request timeout, not a blanket `http.Client.Timeout`:** `inspectclient.Client` applies `--timeout` via `context.WithTimeout` on each individual request, not `http.Client.Timeout`, so the same configured duration bounds a WebSocket handshake without silently killing a long-lived `watch` connection afterward (the spec: "A request timeout is not a total watch duration").
- **Close workflow's error-kind bug:** `handleCloseError` initially classified *any* `*inspectclient.Error` (including `KindProtocol`/`KindNetwork`/`KindTimeout`, which carry no `Code`) as if it were an API error, so a lost/unparseable close response fell through to a generic failure instead of triggering the spec's required reconciliation-via-reread step. A test deliberately simulating a lost response (`TestI08_LostResponse_ReconcilesViaGET`) caught this; the fix checks `Kind == KindAPI` before switching on `Code`. See `internal/inspector/close.go`.
- **Human-mode close errors go to stderr, not stdout:** a documented close outcome that is still a failure (e.g. `TRIP_SUPERSEDED`, `CONFIRMATION_REQUIRED`) must render as an error in human mode, per the spec's "Human warnings/prompts/errors go to stderr" — an earlier version printed the same report helper to stdout regardless of outcome; `TestI07_SupersededTarget_NeverRedirected` caught this by asserting the successor ID appears on stderr, not stdout. See `cmd/gpsinspect/close_cmd.go`.
- **Fake-transport mutex bug in watch tests (not production code):** the first `inspectwatch` test double held its mutex while blocking on `<-ctx.Done()` inside `ReadMessage`, which meant a concurrent `Conn.Close()` (needing the same mutex) couldn't return until the context's own deadline — manifesting as tests that "passed" only by timing out at exactly their configured deadline, 2 full seconds each. Timing the debug output exposed the stall; the fix releases the mutex before blocking, mirroring how a real WebSocket `Close()` can interrupt a pending `Read()`. See `internal/inspectwatch/fake_test.go`.
- **`--page-size 0` is invalid, not "use the default":** flag validation originally used Go's int zero value as a sentinel for "flag not passed," which meant explicitly passing `--page-size 0` was silently treated as if the flag were absent and fell back to the default of 100 instead of being rejected as out of range. The fix uses Cobra's `Flags().Changed(...)` to distinguish "not passed" from "passed as zero." See `cmd/gpsinspect/config.go`.

## Scope and limitations

This is a local demonstration, without authentication or production deployment infrastructure. Deliberately deferred per the spec: persistent event replay, automatic simulator retransmission, source-completeness watermarks, reopening closed trips, movement-based segmentation, map matching, and large-scale incremental reconstruction. INS-07's independent Python randomized fixture generator is deferred (P1). The dashboard is not yet implemented (see "Project status" above).
