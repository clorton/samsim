# GPS Telemetry Demo

A weekend project demonstrating GPS telemetry ingestion, event-time trip reconstruction, and live vehicle visualization. The central scenario is a communications outage: current telemetry resumes first, then delayed fixes fill in history and merge provisional trips.

The backend uses Go and SQLite. The planned dashboard uses React, TypeScript, Vite, and MapLibre GL JS.

## Project status

**The aggregator (AG-01 through AG-10), the GPS simulator (SIM-01 through SIM-07), and the inspector CLI `gpsinspect` (INS-01 through INS-06) are implemented and runnable**, including a real, automated end-to-end integration test (the simulator driving a real aggregator through a >600-second outage, producing two trips that later merge with correct lineage) and a second real end-to-end test exercising `gpsinspect` itself against a real aggregator through that same outage/merge scenario plus a forced close and a saved immutability baseline. The dashboard is not yet implemented.

Implementation order:

1. **Aggregator — done** (see "Running the aggregator" below)
2. **GPS simulator — done** (see "Running the simulator" below)
3. **Inspector CLI — done** (see "Using the inspector" below)
4. Minimal dashboard — not started

Track work in [TODO.md](TODO.md). The normative contracts are [AGGREGATOR_SPEC.md](AGGREGATOR_SPEC.md) and [SIMULATOR_SPEC.md](SIMULATOR_SPEC.md); their `AG-*`/`SIM-*` IDs match the checklist. If this README and a detailed spec differ, follow the spec and update this README.

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
├── SIMULATOR_SPEC.md
├── aggregator.md             # curl/wget/browser endpoint reference
├── go.mod
├── go.sum
├── config.example.yaml       # aggregator config
├── simulator.example.json    # simulator config
├── cmd/
│   ├── aggregator/            # main.go wires config/storage/service/HTTP
│   └── simulator/             # main.go wires config/identity store/engine/HTTP
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
│   └── simulator/             # the whole SIM-01..07 engine: CSV loader, virtual clock, packet
│                           # encoder/sender, identity store, state machine, scheduler, HTTP control API
├── migrations/                # embedded ordered *.sql migrations (aggregator)
├── routes/
│   ├── fixes.csv              # real, runnable sample route (300 1-second rows)
│   └── sample-route.json      # unrelated placeholder from early scaffolding; unused
├── testdata/
│   └── cannonball-1s-full.csv # a much longer (~50-hour) real route fixture, used by some tests
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
- `clock_multiplier` must be in `[10,1000]`; at the default `packet_size: 10`, a multiplier of 1000 demands ~100 packets/s, so `max_packets_per_second` must be raised (e.g. to 200) to keep up — see the worked example below.
- Binds to loopback by default; no auth. The aggregator's `/api/v1/simulators/*` proxy (AG-09) is the intended way to reach it from a dashboard.

Worked example — start, inspect, and drive an outage/recovery cycle:

```bash
curl -s -X POST http://127.0.0.1:8090/start
curl -s http://127.0.0.1:8090/status

# Simulate a coverage outage: fix generation continues, but nothing is sent.
curl -s -X POST http://127.0.0.1:8090/pause-transmission

# Let more than 600 simulated seconds pass (at 100x, that's just 6 real
# seconds), then restore coverage. The aggregator will show a new ACTIVE
# trip immediately; the pre-outage trip stays INACTIVE until backlog drain
# bridges the gap and the aggregator merges them into one trip with the
# old ones marked CLOSED/MERGED.
curl -s -X POST http://127.0.0.1:8090/resume-transmission

# If a send ever fails (e.g. aggregator briefly down), it blocks here:
curl -s http://127.0.0.1:8090/status   # transmission_state: "BLOCKED_ERROR"
curl -s -X POST http://127.0.0.1:8090/resume-transmission \
  -H 'Content-Type: application/json' --data '{"retry_failed":true}'
```

### Simulator API surface (implemented)

| Endpoint | Purpose |
|---|---|
| `GET /status` | Full coherent snapshot: playback/transmission state, queue counts, rates, congestion, last error |
| `PUT /config` | Partial, validated, atomically-applied config update (hot fields apply anytime; others require specific lifecycle states) |
| `POST /start` | Start a fresh run (from `READY`) or resume after `/stop` (from `STOPPED`); idempotent while running |
| `POST /stop` | Freeze the virtual clock and stop new send attempts; retains all queues |
| `POST /pause-transmission` | Simulate a coverage outage: move all unsent live fixes to backlog, keep generating into backlog |
| `POST /resume-transmission` | Clear the outage; optional `{"retry_failed":true}` explicitly retries a blocked packet |
| `POST /reset` | Return to `READY`, clearing run state; optional `{"discard_pending":true}` to discard unsent telemetry |

All bodies and responses use `snake_case` JSON with numeric epoch milliseconds, and the same `{"error":{"code":...}}` envelope shape as the aggregator. See `SIMULATOR_SPEC.md` section 8 for the complete request/response contract, or drive it through the aggregator's proxy routes documented above (`GET/PUT/POST /api/v1/simulators/{id}/...`), which forward to exactly these six endpoints.

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

As of this writing, both commands pass cleanly across all implemented packages (261 tests). Test coverage by spec section:

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

**Precommit crash-injection (T14):** `storage.Store.SetPrecommitFailureHook` installs a test-only hook invoked inside `WithWriteTx` after a transaction's writes are staged but before commit. `TestRestart_PrecommitFailureLeavesNoTrace` and `TestClose_PrecommitFailureLeavesNoTrace` (in `internal/aggregator`) use it to verify a failure at that point rolls back completely — no partial packet/fix/trip/barrier/revision — and that a subsequent retry succeeds cleanly as a fresh attempt.

**Throughput smoke test:** `TestSmoke_IngestThroughput` (in `internal/aggregator`) submits 10-fix packets at a target rate and reports actual throughput/latency percentiles; it only fails on an ingestion *error*, never on falling short of the target rate, per the spec's guidance not to sacrifice durability for a number. Default run is 3s at a 1000 fixes/s target (skipped under `go test -short`); override for a heavier run:

```bash
SMOKE_DURATION_SECONDS=30 SMOKE_FIXES_PER_SEC=1000 go test ./internal/aggregator/ -run TestSmoke -v
```

**End-to-end simulator↔aggregator integration (S17/S18, T18, E2E-01):** `TestS17_BothEncodingsWorkAgainstRealAggregator`, `TestS17_RetryAfterLostResponseChangesStateExactlyOnce`, and `TestS18_OutageProducesTwoTripsThenMerge` (in `internal/simulator`) spin up a real aggregator (`httptest.Server` wrapping the actual `storage`/`aggregator`/`httpapi` packages against a temp SQLite file, not a mock) and drive a real `Engine` against it over HTTP. S18 in particular reproduces E2E-01's full scenario end to end: normal playback, a simulated outage past 600 simulated seconds, live-first resume, two current trips observed via the aggregator's own query API, then backlog drain producing a merged trip with both predecessors marked `MERGED` and correct lineage, with canonical position never regressing. This was also verified manually against the built binaries (see "Running the simulator" above).

## Decision log (deviations from/clarifications to AGGREGATOR_SPEC.md)

- **SQLite driver:** `modernc.org/sqlite` (pure Go), not a cgo driver, so `go build`/`go test` need no C compiler and work identically in sandboxed environments.
- **WebSocket library:** `github.com/coder/websocket`, chosen for its context-based API, which maps cleanly onto the per-write deadlines AG-08 requires.
- **Package split:** the spec's `internal/httpapi` ("codecs, handlers, DTOs, WebSocket") was split into `internal/wire` (pure AG-02 codec/validation/fingerprint, no HTTP or storage dependency) and `internal/httpapi` (actual `net/http` handlers/DTOs/router/WebSocket, depends on `internal/aggregator`). This avoids an import cycle, since AG-07 handlers call into the aggregator service, which itself needs the AG-02 codec for ingestion.
- **Additional small packages** not explicitly named in AG-01's layout: `internal/clock` (injectable clock), `internal/idgen` (injectable trip-ID generator), `internal/apierr` (shared error codes/envelope), `internal/config` (YAML loading/validation). These are reusable primitives with no cross-dependencies, not restructured domain logic.
- **Router:** Go 1.22+ `net/http.ServeMux` wildcard path patterns (`{id}`), registered without a method prefix; each handler checks `r.Method` itself and returns the standard JSON error envelope on mismatch (ServeMux's built-in 405 is plain text, which would violate AG-07's consistent-error-shape requirement).
- **AG-08 subscribe-ready race:** implemented via `storage.Store.WithWriterLock` + `ReadUnderWriterLock`, which reads the vehicle's current revision on the *write* connection (not the read pool, to avoid any replication lag) while holding the same mutex `WithWriteTx` uses, and registers the Hub subscription before releasing it. This guarantees no commit can land between "read ready revision" and "start receiving future commits."

## Decision log (simulator, SIMULATOR_SPEC.md)

- **Independent packages, zero aggregator coupling:** `internal/simulator` does not import any aggregator-specific package (`internal/apierr`, `internal/storage`, `internal/aggregator`, `internal/wire`). It depends only on the shared `gen/telemetry/v1` protobuf types, the standard library, `modernc.org/sqlite` (for the identity store), and `google.golang.org/protobuf`. It has its own small `apierr.go` with its own error codes/envelope, deliberately not shared with the aggregator's `internal/apierr`, since the two processes' error vocabularies are independent and coupling them would create an unnecessary cross-component dependency.
- **Dual-mutex `Engine`:** `mu` guards all mutable run state (queues, counters, pending packet) for brief critical sections; a separate `controlMu` serializes whole control-plane operations (`Start`/`Stop`/`PauseTransmission`/`ResumeTransmission`/`Reset`/`PutConfig`) against each other so their own off-lock phases (route parsing, building a new `Sender`) cannot interleave, without ever blocking `GET /status` or the background tick loop.
- **Async send dispatch:** `Tick()` only ever decides *whether* to start a send attempt while holding `mu`; the actual identity allocation and HTTP POST (`dispatchSend`) always run in a separate goroutine with no lock held, so a slow or stuck aggregator can never stall route generation or control-API responsiveness. An earlier version called `dispatchSend` inline from `Tick()`, which blocked the whole tick loop on every send; this was caught and fixed during testing (see `state.go`'s `Tick` doc comment).
- **Recovery gate armed at resume, not pause:** `PauseTransmission` only decides whether to move live records into backlog; it does **not** decide whether the post-outage recovery gate (SIM-04) will be needed. The gate is armed inside `ResumeTransmission`, based on whether backlog is nonempty and the route is not yet exhausted *at that moment*. This was a real bug found via the `TestS18_OutageProducesTwoTripsThenMerge` end-to-end test: arming the gate only when `PauseTransmission` observed a nonempty live queue meant that if live had already drained to empty by the time an operator called pause (common, since generation and transmission are decoupled), but backlog went on to accumulate while paused, the gate never armed at all. Resume then drained backlog seamlessly with no live-first boundary, so the aggregator only ever saw one continuous trip instead of two — silently defeating the entire outage/merge demo scenario. See `lifecycle.go`.
- **`Reset` counter hygiene:** `Reset` zeroes every counter, including `discardedFixesCount`, each time it runs. An earlier version left `discardedFixesCount` accumulating across repeated resets, which broke the invariant `generated == acknowledged + discarded + unsent` checked by `TestEngine_QueueInvariant_RandomizedOperations`; the randomized test caught it immediately. See `lifecycle.go`.

## Scope and limitations

This is a local demonstration, without authentication or production deployment infrastructure. Deliberately deferred per the spec: persistent event replay, automatic simulator retransmission, source-completeness watermarks, reopening closed trips, movement-based segmentation, map matching, and large-scale incremental reconstruction. The GPS simulator, inspector CLI, and dashboard are not yet implemented (see "Project status" above).
