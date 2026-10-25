# Using `gpsinspect`: aggregator + simulator + inspector walkthrough

A practical, end-to-end guide to running the aggregator and a simulator locally, then using `gpsinspect` to inspect, watch, validate, and manually close trips. Every command below was run against real local processes while writing this document — not written from the spec alone.

For normative behavior, see [specs/AGGREGATOR_SPEC.md](specs/AGGREGATOR_SPEC.md), [specs/SIMULATOR_SPEC.md](specs/SIMULATOR_SPEC.md), and [specs/INSPECTOR_SPEC.md](specs/INSPECTOR_SPEC.md). For raw HTTP/curl access to the aggregator, see [aggregator.md](aggregator.md).

## 1. Build everything

From the repository root:

```bash
go build -o bin/aggregator ./cmd/aggregator
go build -o bin/simulator ./cmd/simulator
go build -o bin/gpsinspect ./cmd/gpsinspect
```

## 2. Start the aggregator

```bash
./bin/aggregator
```

This listens on `127.0.0.1:8080` with a fresh SQLite database at `./data/gps.sqlite` (created automatically) and the default 600-second trip gap / 1000ms expected fix interval. Logs go to stderr as structured JSON. Leave it running in this terminal, or run it in the background:

```bash
./bin/aggregator > aggregator.log 2>&1 &
```

Confirm it's up:

```bash
curl -s http://127.0.0.1:8080/api/v1/health
# {"healthy":true,"database_id":"...","trip_gap_seconds":600}
```

See `config.example.yaml` for every configuration field, or [aggregator.md](aggregator.md) for the full HTTP API reference.

## 3. Start a simulator

The simulator needs a JSON config file (there is no default). It takes CSV route input — reuse one of the checked-in sample routes. For this walkthrough we use `testdata/cannonball.csv` (a ~50-hour, 180k-row route), which is long enough to demonstrate a multi-minute outage without the simulator reaching end-of-route first. The short `routes/fixes.csv` (300 rows ≈ 5 minutes of route) is fine for simple inspection but too short to safely demonstrate the outage/merge scenario in section 6 below, since playback would reach EOF before 600 simulated seconds of outage have accumulated.

Create `simulator.json`:

```json
{
  "listen": "127.0.0.1:8090",
  "simulator_id": "sim-001",
  "vehicle_id": "vehicle-outage",
  "aggregator_url": "http://127.0.0.1:8080",
  "routes": { "long": "./testdata/cannonball.csv" },
  "route_id": "long",
  "run_start_ms": "now",
  "clock_multiplier": 50,
  "expected_fix_interval_ms": 1000,
  "normal_report_interval_simulated_ms": 10000,
  "packet_size": 10,
  "encoding": "json",
  "max_packets_per_simulated_second": 2,
  "live_weight": 3,
  "backlog_weight": 1,
  "request_timeout_ms": 2000,
  "identity_db_path": "./data/simulator-identity.sqlite",
  "max_route_bytes": 67108864,
  "max_route_rows": 1000000
}
```

Run it:

```bash
./bin/simulator -config simulator.json
# or in the background:
./bin/simulator -config simulator.json > simulator.log 2>&1 &
```

Confirm it's up and the route loaded:

```bash
curl -s http://127.0.0.1:8090/status | python3 -c "import json,sys; d=json.load(sys.stdin); print(d['playback_state'], d['route_rows'])"
# READY 180379
```

The simulator comes up in `READY` and generates/sends nothing until explicitly started:

```bash
curl -s -X POST http://127.0.0.1:8090/start
```

See `simulator.example.json` and [README.md](README.md)'s "Running the simulator" section for the complete field reference and control API (`/status`, `/config`, `/start`, `/stop`, `/pause-transmission`, `/resume-transmission`, `/reset`).

## 4. First look with `gpsinspect`

Let the simulator run for a few seconds, then:

```bash
./bin/gpsinspect vehicles
```

```text
Warning: the global vehicle list is best-effort across pages, not a single transactional snapshot
VEHICLE_ID       LATEST_TIMESTAMP          ACTIVE_TRIP                           REVISION
vehicle-outage   2026-10-25T07:30:51.545Z  7179b43a-...                          42
```

Inspect the vehicle in detail:

```bash
./bin/gpsinspect vehicle vehicle-outage
```

```text
Vehicle ID: vehicle-outage
Database ID: 73cad6b4-aa26-47fa-900c-b5ffb8d3fd98
Revision: 42
Active trip: 9a3df314-c322-4494-bf8f-a440ca443a8d
Trip gap (s): 600
Expected fix interval (ms): 1000

Latest fix timestamp: 2026-10-25T07:30:51.545Z (1792913451545)
Latest fix position: lat=40.75... lon=-74.00... speed_mps=... heading_deg=...
Latest fix current trip: 9a3df314-c322-4494-bf8f-a440ca443a8d
Latest fix assignment reason: -
```

List its trips and inspect one:

```bash
./bin/gpsinspect trips vehicle-outage
./bin/gpsinspect trip <trip-id>
./bin/gpsinspect gaps <trip-id>
```

`gaps` reports the completeness assessment used by closure (see section 7): internal sampling gaps only, never a guarantee that all boundary telemetry has arrived.

`gpsinspect --base-url`, `--timeout`, `--json`, `--page-size`, `--revision-attempts`, `--max-fixes`, and `--max-response-bytes` are all optional; `./bin/gpsinspect --help` documents every flag and default. `--base-url` defaults to `http://127.0.0.1:8080`, matching the aggregator default above, so it's omitted throughout this walkthrough.

## 5. Watch live events

```bash
./bin/gpsinspect watch vehicle-outage
```

```text
[snapshot] vehicle=vehicle-outage revision=221 database=73cad6b4-...
[event] vehicle=vehicle-outage revision=222 at=2026-10-25T07:29:14.333Z
  - trip.updated trip=426e48d7-...
  - vehicle.updated active_trip=426e48d7-...
[event] vehicle=vehicle-outage revision=223 at=2026-10-25T07:29:14.533Z
  ...
```

`watch` performs the ready/snapshot handshake, then streams one line per semantic change (new trip, trip updated, trip closed, trips merged, vehicle updated, telemetry quarantined) as the simulator's packets land. Press Ctrl-C to stop (exits 130). Add `--json` for NDJSON output instead (one JSON record per line: `snapshot`, `event`, `resync`, `waiting`, `disconnected`, or `error`) — convenient for piping to `jq` or a log file:

```bash
./bin/gpsinspect watch vehicle-outage --json | jq -c '.data.changes // empty'
```

If the connection drops, `watch` automatically reconnects (default 10 attempts, exponential backoff) and re-fetches a fresh snapshot; it never tries to patch state across a gap.

## 6. Reproduce an outage and watch the merge

This is the project's centerpiece scenario: a coverage outage longer than the 600-second trip gap produces two trips, and later-arriving backlog data merges them.

Check progress, then simulate an outage by pausing transmission (fix generation continues into backlog; nothing is sent):

```bash
curl -s -X POST http://127.0.0.1:8090/pause-transmission
```

Wait long enough in **wall-clock** time for the simulator's virtual clock to accumulate more than 600 simulated seconds while paused. At `clock_multiplier: 50`, that's `600 / 50 = 12` real seconds minimum — wait a bit longer for a comfortable margin:

```bash
sleep 15
curl -s http://127.0.0.1:8090/status | python3 -c "import json,sys; d=json.load(sys.stdin); print('backlog:', d['backlog_queue_count'])"
# backlog: 759
```

Resume coverage:

```bash
curl -s -X POST http://127.0.0.1:8090/resume-transmission
```

The simulator's fresh-first preference sends one live report first — making the outage boundary visible — before draining backlog, without ever blocking backlog behind a live report that isn't ready yet. Check trips immediately (a fast backlog drain may complete before you look, in which case you'll see the merge already finished):

```bash
./bin/gpsinspect trips vehicle-outage --include-merged
```

Mid-drain, this shows two *current* trips (the pre-outage trip, now `INACTIVE`, and a new post-outage `ACTIVE` trip). Once backlog drain finishes and the bridge is received, the aggregator merges them:

```text
TRIP_ID        START                     END                       FIXES  STATUS                 CLOSE_REASON  SUCCESSOR
9a3df314-...   2026-10-25T07:28:18.545Z  2026-10-25T07:40:17.545Z  720    CLOSED/MERGED (audit)  MERGED        b302890c-...
b302890c-...   2026-10-25T07:28:18.545Z  2026-10-25T07:50:52.545Z  1001   ACTIVE                 NONE          -
d46cf7d6-...   2026-10-25T07:50:22.545Z  2026-10-25T07:50:42.545Z  21     CLOSED/MERGED (audit)  MERGED        b302890c-...
```

`--include-merged` adds the `CLOSED/MERGED` audit predecessors; without it, only the single current `ACTIVE` trip shows. Inspect the merged trip's lineage:

```bash
./bin/gpsinspect trip b302890c-ef9d-49d2-a41b-f7c76d600ffd
```

```text
...
Immediate predecessors: [9a3df314-c322-4494-bf8f-a440ca443a8d d46cf7d6-3939-48b0-8eb8-c5702624f6ed]
Immediate successor: -
Terminal successor: -
...
```

## 7. Independently validate the result

`validate` is read-only: it never POSTs telemetry, closes trips, or touches SQLite. It builds its own reference model from scratch (not the aggregator's reconstruction code) and runs ten independent checks (V01 raw identity through V10 observed monotonicity):

```bash
./bin/gpsinspect validate vehicle-outage
```

```text
Database ID: 73cad6b4-aa26-47fa-900c-b5ffb8d3fd98
Vehicle ID: vehicle-outage
Revision: 182
Overall: PASS_WITH_LIMITATIONS
Summary: PASS=8 FAIL=0 SKIP=2 WARN=0

CHECK  STATUS  MESSAGE
V01    PASS    raw fixes are unique, strictly ordered, within bounds, and latest_fix matches the max-timestamp raw fix
V02    PASS    every assigned fix has exactly one current-trip membership matching current_trip_id; quarantined fixes have none
V03    PASS    all trip geometries are ordered/unique/nonempty and match their summary metadata
V04    PASS    mutable trip memberships exactly match independently computed maximal temporal components
V05    PASS    manual closure intervals are disjoint, terminal, match frozen geometry and snapshot barriers, and quarantine is scoped correctly
V06    PASS    at most one ACTIVE trip, and it is exactly the mutable trip containing the maximum eligible raw timestamp
V07    PASS    MERGED lineage is acyclic, consistent, geometry-subset-preserving, and terminal successors resolve correctly
V08    PASS    independently computed completeness aggregates match the API's completeness DTO for every trip
V09    SKIP    no --baseline supplied; immutability across runs was not checked
V10    SKIP    no --baseline supplied; monotonicity across runs was not checked
...
```

`V09`/`V10` (cross-run immutability/monotonicity) need a saved baseline, so they `SKIP` on a first run and the overall result is `PASS_WITH_LIMITATIONS` rather than a bare `PASS` — deliberately, so a clean report never implies more was proven than it was.

Save a baseline, then validate again against it later (e.g. after stopping the simulator, or on a subsequent day):

```bash
./bin/gpsinspect validate vehicle-outage --write-baseline before.json
# ... let more time pass, or stop/restart the simulator ...
./bin/gpsinspect validate vehicle-outage --baseline before.json
```

With a baseline supplied, V09 confirms every already-`CLOSED` trip's frozen geometry/metadata is unchanged, and V10 confirms revision and latest event time never regressed:

```text
Overall: PASS
Summary: PASS=10 FAIL=0 SKIP=0 WARN=0
```

`--write-baseline` only writes after collection completes with zero `FAIL` checks, creates the file exclusively (refuses to silently overwrite), and sets file mode `0600`.

## 8. Manually close a trip

Closing freezes a trip immediately and permanently; late-arriving fixes inside its interval are quarantined rather than reopening it. `close` always warns about boundary uncertainty and shows missing-fix counts before acting.

**No internal gaps** — closes immediately, no confirmation needed:

```bash
./bin/gpsinspect close <trip-id>
```

```text
Trip ID: b302890c-ef9d-49d2-a41b-f7c76d600ffd
Outcome: CLOSED
Status: CLOSED
Close reason: MANUAL
Revision: 220
Forced close: false
Message: trip closed
```

**With internal gaps** — an unforced close is rejected (exit code 4), and running noninteractively (as here) never silently confirms:

```bash
./bin/gpsinspect close <trip-id-with-gaps>
```

```text
Trip ID: dc30ff1d-ba13-46ed-8a23-753b583b7981
Outcome: CONFIRMATION_REQUIRED
Message: trip has missing internal fixes; rerun with --force to acknowledge and close
Error: trip has missing internal fixes; rerun with --force to acknowledge and close
```

Run interactively from a real terminal and it instead prompts `Close this incomplete trip permanently? [y/N]`, accepting only an exact (case-insensitive) `y`/`yes`. Either way, explicitly acknowledge the gap to proceed:

```bash
./bin/gpsinspect close <trip-id-with-gaps> --force
```

```text
Trip ID: dc30ff1d-ba13-46ed-8a23-753b583b7981
Outcome: CLOSED
Status: CLOSED
Close reason: MANUAL
Revision: 2
Forced close: true
Message: trip closed
```

Closing an already-`CLOSED/MANUAL` trip reports `ALREADY_CLOSED` and sends no request (safe to retry). Closing a `CLOSED/MERGED` trip fails with `TRIP_SUPERSEDED` and the successor ID, and never redirects the close to the successor.

## 9. Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (or `validate` with no FAIL) |
| 1 | API/network/protocol/local IO failure, unknown resource |
| 2 | Usage/flag/config error |
| 3 | `validate` report contains at least one FAIL |
| 4 | Close canceled, or confirmation required and not given |
| 5 | A consistent revision could not be collected (or a close's revision went stale) |
| 130 | Interrupted (Ctrl-C) |

## 10. Cleanup

```bash
kill %1 %2   # or: pkill -f './bin/aggregator'; pkill -f './bin/simulator'
```

The aggregator's database (`./data/gps.sqlite`) and the simulator's identity database (`./data/simulator-identity.sqlite`) persist on disk; delete the `./data/` directory to start completely fresh next time.
