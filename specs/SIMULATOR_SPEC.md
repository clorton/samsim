# Go GPS simulator — implementation specification

**Audience:** Claude Code implementing the simulator. **Status:** normative baseline, 2026-10-24.

Companions: [TODO.md](TODO.md), [AGGREGATOR_SPEC.md](AGGREGATOR_SPEC.md), and [README.md](README.md). Task IDs SIM-01..SIM-08 match the checklist. AG-02/04 define the telemetry wire and acceptance contracts; AG-09 defines the aggregator's simulator control proxy. Implement this component after the aggregator. Do not build a route generator, inspector, or dashboard as part of this task.

## 1. Objective and resolved choices

Read GPS fixes from the user's existing address-to-address route generator's CSV. Play that route at 10–1000× real time, batch telemetry, simulate loss of cellular coverage while continuing generation, and recover with live-priority interleaved backlog transmission.

The implementation must make it easy to demonstrate two provisional aggregator trips merging when delayed telemetry bridges a gap longer than 600 simulated seconds. The simulator does not construct trips or decide when trips close.

MUST denotes a requirement. The following choices fill details not previously settled:

| Topic | Decision |
|---|---|
| Language/runtime | Go, same module and generated telemetry types as aggregator |
| CSV time | `ts` is elapsed seconds, converted to epoch milliseconds using a fixed run start |
| Concurrency | One serialized state owner; at most one HTTP telemetry request in flight |
| Recovery queues | FIFO live and backlog queues; pre-outage unsent live fixes move to backlog |
| Scheduling | Default live:backlog weights 3:1, guaranteed backlog service; wall-clock packet spacing |
| Fresh recovery | First recovery request uses post-outage live fixes when future route points remain |
| Batch completion | Allow partial packets on flush deadline, EOF and recovery; do not wait forever for 10 points |
| Stop/start | Stop freezes playback and sending; start resumes the same run without jumping event time |
| EOF | Stop generation, continue draining queues until acknowledged |
| Failed requests | Retain exact packet, block sending, report error; explicit retry only in P0 |
| Persistence | Persist packet counters; P0 route/queues/run state are in memory, not crash-durable |
| Configuration | Local JSON file plus JSON control API; explicit allowlisted route IDs |
| Deferred | Automatic retry/backoff, crash recovery of queues, looping routes, arbitrary CSV adapters |

The in-memory queue durability limit is explicit: aggregator acceptance is durable, but unacknowledged simulator work can be lost on process failure. P0 provides retention during normal operation and controlled shutdown protection, not end-to-end crash-durable delivery. Packet IDs must still never be reused after restart. SIM-08 adds automatic retransmission later; a durable outbox is separate future work.

## 2. SIM-01 — Package layout and configuration

Suggested layout within the existing module:

```text
cmd/simulator/main.go
internal/simulator/
  config.go
  csv.go
  clock.go
  state.go
  scheduler.go
  packet.go
  sender.go
  control.go
  identity.go
  *_test.go
testdata/simulator/
```

Use the shared generated `TelemetryPacket`/`GPSFix` types. Use `encoding/csv`, `net/http`, injectable monotonic clock/timer interfaces, injected HTTP sender and counter store. Do not import aggregator reconstruction or storage internals. A separate local simulator SQLite file using the project's selected driver is suitable for counters; do not share or write the aggregator database.

Implement:

```bash
go run ./cmd/simulator --config simulator.json
```

No generation starts automatically. Listen on loopback by default. Example complete configuration:

```json
{
  "listen":"127.0.0.1:8090",
  "simulator_id":"sim-001",
  "vehicle_id":"vehicle-001",
  "aggregator_url":"http://127.0.0.1:8080",
  "routes":{"demo":"./routes/fixes.csv"},
  "route_id":"demo",
  "run_start_ms":"now",
  "clock_multiplier":100,
  "expected_fix_interval_ms":1000,
  "packet_size":10,
  "encoding":"json",
  "max_packets_per_second":50,
  "live_weight":3,
  "backlog_weight":1,
  "partial_flush_wall_ms":200,
  "request_timeout_ms":2000,
  "identity_db_path":"./data/simulator-identity.sqlite",
  "max_route_bytes":67108864,
  "max_route_rows":1000000
}
```

`run_start_ms` accepts a positive epoch-millisecond JSON integer or the literal string `"now"`. Resolve now once, when a new run is first started; expose its resolved numeric value in status. A stopped/resumed run retains it. Other API timestamps use numeric milliseconds.

Validate IDs and telemetry bounds per AG-02; encoding is json or protobuf; multiplier finite in [10,1000]; packet size integer 1..10000; rate finite >0 and ≤10000; weights integers 1..100; positive bounded timeouts/limits. A configured packet must also fit the aggregator's byte limit (default 1 MiB). Validate aggregator URL as absolute http/https, with no credentials/query/fragment and root path only; append `/api/v1/telemetry`. Disable redirects. Startup fails clearly on invalid configuration or inaccessible identity store. Do not automatically install dependencies or discover a network service.

The route map and local file paths are startup-only configuration. Browser/API callers choose route_id from that map, not arbitrary filesystem paths. Limits are intentionally generous for a finite weekend route; validate before loading or mutating run state. Configuration failures must leave the previous valid state intact.

## 3. SIM-01/02 — CSV contract and event timestamps

Expected header and sample:

```csv
ts,lat,lon,speed_mps,heading_deg,dist_m,speed_source
0.0,40.7440080,-73.9801730,4.09,29.6,0.0,osrm_estimate
1.0,40.7440406,-73.9801489,4.21,29.0,4.2,osrm_estimate
2.0,40.7440737,-73.9801247,4.21,29.0,8.4,osrm_estimate
```

| CSV field | Meaning / mapping |
|---|---|
| ts | Nonnegative finite decimal elapsed seconds; event offset in ms |
| lat | Degrees → latitude |
| lon | Degrees → longitude |
| speed_mps | Meters/second → speed_mps |
| heading_deg | Degrees → heading_degrees |
| dist_m | Optional cumulative meters, diagnostic only |
| speed_source | Optional free text, diagnostic only |

Header-based mapping permits reordered columns. Require the five telemetry columns exactly once, with nonempty valid values in every record. Reject duplicate headers (including extras), inconsistent record widths, malformed CSV, empty routes and invalid measurements. Standard CSV quoting and CRLF are supported; a single UTF-8 BOM at file start may be stripped. Ignore additional unknown columns after checking record widths. Use AG-02 coordinate/speed/heading ranges; normalize measurement types to the shared schema (float64 coordinates, float32 speed/heading). Do not silently default missing numeric CSV fields.

Optional diagnostics need not be retained. If retained, validate dist_m as finite, nonnegative and nondecreasing when present; speed_source is text, not an enum. Diagnostics are never sent, hashed as telemetry, or used to determine trips. Errors report record number and field; do not silently skip rows or reorder the route.

Compute:

```text
offset_ms = round_half_up(decimal(ts) × 1000)
timestamp_ms = resolved_run_start_ms + offset_ms
```

Use decimal/rational parsing to avoid floating-point rounding boundary errors. Require source ts strictly increasing and rounded offsets strictly increasing; reject collisions after rounding. Check overflow and AG-02 positive epoch range. Do not subtract the first ts: if a file begins at ts=5, its first fix occurs five simulated seconds after run origin. With ts=0, generate the first point immediately on start. A timestamp-free adapter and resampling are outside P0.

The complete file is validated before first playback and before accepting a changed route. Keep immutable route records in memory and queue references/indexes, avoiding duplicate route payload copies. Enforce byte/row bounds before allocating unbounded memory. Expected interval describes cadence for warnings; it does not replace source timing. Irregular source intervals may yield aggregator completeness warnings and should be reported as a route diagnostic.

## 4. SIM-02 — Clock and run lifecycle

Maintain separate monotonic playback time and actual wall time. Event timestamps are fixed by the route and resolved run start. Packet timestamps must not be rewritten when a fix is transmitted late.

At any running clock segment:

```text
virtual_elapsed = anchor_virtual_elapsed
                + monotonic_elapsed_since_anchor × clock_multiplier
```

Use adequate fixed-point/rational precision or carried fractional remainder so repeated timer wakeups do not accumulate drift. A multiplier change first advances virtual time using the old multiplier, then installs new anchors. Use the next record's virtual deadline rather than one wall timer per simulated millisecond. On late timer wakeups generate all due records once, in order. The sender remains rate-limited during generation catch-up. For large catch-up batches, chunk work and service control requests between chunks (e.g. ≤1000 records per chunk) without duplicating indexes.

Playback states:

| State | Meaning |
|---|---|
| READY | Valid configuration, no run started |
| RUNNING | Clock advances and due fixes are generated |
| STOPPED | Run exists; clock frozen, transmission stopped |
| DRAINING | EOF reached; no future generation, queues still pending |
| COMPLETED | EOF reached and no queued/in-flight/failed packet remains |

Track transmission independently: ENABLED, PAUSED (coverage outage), or BLOCKED_ERROR. Stop additionally prevents sending regardless of transmission mode. No separate thread may mutate these fields outside the state owner. Read/status snapshots are coherent.

Start from READY resolves run start, validates absolute timestamps, initializes run ID and begins playback. Start from STOPPED resumes from saved virtual time, without counting the stopped wall interval. Resume after EOF restores DRAINING rather than generating again. Start while RUNNING/DRAINING is an idempotent no-op. Start after COMPLETED returns 409 RESET_REQUIRED. Stop from READY/STOPPED is a no-op; from RUNNING/DRAINING freezes generation and prevents new send attempts. Stop does not close an aggregator trip. At EOF flush partial packets and drain while enabled; when coverage is paused or an error is blocked, remain DRAINING until explicitly resolved.

For a new run, reset is mandatory. Configuring now again after reset chooses a new run start; explicit reuse of an old start can intentionally reproduce aggregator fix identities and cause duplicates/conflicts. Do not silently shift explicit starts to avoid conflicts.

## 5. SIM-03 — Packet identity, encoding and acceptance

Packet ID format: UTC creation date `YYYYMMDD-SEQUENCE`, sequence formatted with at least six digits and allowed to grow. Use a transactionally persisted per-(simulator_id,UTC date) counter. Reserve and commit the next counter before exposing/sending a packet. Counter gaps after crashes are acceptable; reuse is not. Wall-clock rollback to a previous date uses that date's persisted counter. Exhaustion fails safely. Prevent two processes from concurrently using the same identity store/simulator identity with an exclusive instance lock. Deleting/replacing the identity store while reusing simulator_id is unsupported; document using a new simulator identity if the store is lost.

Packet creation means selecting queue records for a send attempt, not generating fixes. A packet contains one simulator ID and vehicle ID; fixes are ascending in event time. Do not mix live/backlog records in one packet. Snapshot packet size and encoding at creation. Serialize once with protojson or proto using AG-02. Retain immutable packet ID, message/body, encoding, selected records and lane until acknowledgment or explicit discard. Retry sends the same packet; do not re-batch it or allocate a replacement ID.

Only one request is in flight. POST to `/api/v1/telemetry`, with matching Content-Type and bounded request timeout. No redirects or hidden automatic retries. Accept only status 200 with a valid AG-04 JSON receipt matching simulator_id/packet_id and valid nonnegative counters/revision. Unknown extra receipt fields may be ignored. For duplicate_packet=false, accepted_fixes+duplicate_fixes must equal packet fix count; for duplicate_packet=true, do not infer new insertion from original counters. Quarantined fixes count as accepted raw data; remove them from the simulator outbox and expose the count as a diagnostic. The aggregator owns trip assignment.

Any network error, timeout, non-200, or malformed/mismatched receipt is a failed/uncertain send. Retain the entire packet; transition to BLOCKED_ERROR, send no more packets, and continue route generation while playback remains RUNNING. Record safe error code, HTTP status and whether acceptance is uncertain. A timeout may occur after aggregator commit; explicit retry must preserve identity so AG-04 deduplication handles it. Do not infer rejection from a timeout.

No autonomous retries in P0. `POST /resume-transmission` with `retry_failed:true` explicitly retries the blocked packet once as the next send, subject to normal rate limits. A second failure blocks again. Permanent 4xx conflicts also remain blocked; changing identity/content to bypass a conflict is prohibited. User may fix the cause or explicitly discard pending work via reset. SIM-08 later adds bounded backoff/retry policy.

## 6. SIM-04 — Queues, coverage outage and recovery

Maintain FIFO live queue, FIFO backlog queue, plus zero or one immutable pending packet (in flight or failed). Queued counts exclude pending records; status reports pending separately and total unsent as their sum. Every generated, undiscarded record is in exactly one of: live, backlog, pending, or acknowledged. No duplicate queue membership and no silent loss.

Normal enabled generation appends to live. Pausing coverage atomically moves all unsent live entries to backlog, preserving event order, and future generation while paused appends there. An already pending packet remains its own object. No new HTTP attempts start after pause takes effect. The pending attempt may complete, because bytes may already have reached the aggregator; cancellation cannot undo acceptance. Stop/pause responses explicitly report any in-flight packet ID. Pause does not manufacture network errors, cancel a request, or rewrite its identity.

On coverage resume, future generated records go to live. Preserve backlog contents and FIFO order; a second outage moves newer queued live entries behind existing backlog. An existing failed packet must be resolved before ordinary traffic. Its required retry has precedence over freshness/fairness guarantees.

Recovery gate: when resuming with backlog and future route points remain, wait until at least one post-outage fix has been generated and send a live packet first, even if partial. This makes the >600-second outage scenario visible. Do not reclassify old backlog as current. If EOF is already reached, drain backlog immediately; if the next source point is distant, waiting for it is intentional and visible as recovery_waiting_for_live=true. A repeated resume must not keep resetting this gate. If pause occurred without generating any backlog, no gate is necessary.

After the initial recovery send, use weighted packet selection with defaults L,L,L,B while both queues are eligible. Advance fairness state only on actual ordinary send attempts, not empty timer ticks; skip empty lanes and use spare slots for the other lane. A queued backlog must receive at least one opportunity per four ordinary attempts with default weights whenever live is continuously eligible. Send attempts which fail block all traffic; retrying that pending packet is not a new fairness selection. Runtime weight changes reset the selection cycle at the next packet boundary. Freshness does not override the backlog guarantee; congestion is reported instead of starving backlog.

A lane is eligible when it has packet_size records, its oldest queued record has waited partial_flush_wall_ms in wall time, or EOF/recovery requires flushing. A selected partial packet contains all currently available records up to packet_size. Flush age runs in wall time even while stopped/paused, so partial packets may be immediately eligible on resume. An ineligible lane does not block an eligible other lane, except for the explicit first-live recovery gate.

## 7. SIM-05 — Transmission capacity and status

Rate is packets per wall-clock second, independent of multiplier. Use monotonic spacing with no accumulated burst credits: attempts are at least `1 / max_packets_per_second` apart. Count failed attempts and explicit retries. After an idle/outage period send at most one immediate packet; do not release a token backlog. A single in-flight request further limits actual throughput. Runtime rate changes apply to the next attempt using the last-attempt anchor.

At one source fix/second, multiplier M and full packet size P, approximate live demand is M/P packets per wall second. With M=1000, P=10, demand is 100 packets/s; default 50/s is insufficient even before backlog share. Use 200/s or a lower multiplier for the merge demo. HTTP latency can also make configured capacity unattainable with one in-flight request. Guaranteed backlog opportunities do not guarantee eventual drain if input exceeds capacity.

Status must expose observed generated/acknowledged fixes per wall second (rolling 5-second window), attempted/acknowledged packets, lane counts, pending count, oldest unsent timestamp, latest generated timestamp, latest acknowledged event timestamp (max over receipts, never last-arrived), oldest live queue age, and current virtual route time. latest_generated is a simulator watermark, not proof that the aggregator received all earlier fixes.

Define congested=true when enabled/running and oldest live queue age exceeds 1000 wall ms, or observed queue growth persists across the rolling window; expose the triggering reason. Pause and BLOCKED_ERROR have their own statuses instead of pretending to be successful transmission. Bounded finite route storage prevents unlimited generation memory; if route exceeds configured limits reject before start rather than drop fixes.

## 8. SIM-06 — Control HTTP API

All control/status JSON uses snake_case and numeric epoch milliseconds. Requests have Content-Type application/json when a body is present, reject unknown fields and trailing documents, and are limited to 64 KiB. Empty action bodies mean `{}`. Bind loopback; no auth/CORS solution is required for P0 because the aggregator proxies controls. Control mutations serialize with generation/queue selection. Do not hold the state owner while doing network or file I/O: validate candidates off-thread then atomically apply only if state/config revision still matches.

Use a monotonic `control_revision` for config/lifecycle transitions, separate from aggregator vehicle revisions. It is diagnostic, process-local, and resets on restart. Automatic counters/generation need not increment it. Control responses are bounded operations returning a current coherent status; never wait for a telemetry HTTP request to finish. Route parsing for PUT/reset preparation must complete or time out before the proxy's 3-second deadline (default control preparation deadline 2 seconds), leaving state unchanged on timeout. Reuse prevalidated immutable route data where possible.

### GET /status

Return 200 with at least:

```json
{
  "simulator_id":"sim-001", "vehicle_id":"vehicle-001",
  "run_id":"opaque-run-id", "control_revision":5,
  "playback_state":"RUNNING", "transmission_state":"ENABLED",
  "config":{
    "route_id":"demo", "run_start_ms":"now", "clock_multiplier":100,
    "expected_fix_interval_ms":1000, "packet_size":10, "encoding":"json",
    "max_packets_per_second":200, "live_weight":3, "backlog_weight":1,
    "partial_flush_wall_ms":200, "request_timeout_ms":2000
  },
  "resolved_run_start_ms":1792845600000,
  "route_rows":3600, "generated_fixes":1200,
  "acknowledged_fixes":1100, "discarded_fixes":0,
  "live_queue_count":10, "backlog_queue_count":80,
  "pending_fix_count":10, "total_unsent_fixes":100,
  "pending_packet_id":"20261024-000012",
  "in_flight":false, "recovery_waiting_for_live":false,
  "latest_generated_timestamp_ms":1792846799000,
  "latest_acknowledged_timestamp_ms":1792846790000,
  "virtual_elapsed_ms":1199000,
  "oldest_unsent_timestamp_ms":1792846500000,
  "oldest_live_age_wall_ms":10,
  "congested":false, "congestion_reason":null,
  "last_error":null
}
```

Add the rolling rates and packet/receipt/quarantine counters described above with explicit units. Fields without a value are null; run_id/resolved start are null in READY. Include available route IDs but not filesystem paths or credentials. Counts are per run except persistent identity counters and separately labeled lifetime/process metrics. GENERATED = ACKNOWLEDGED + DISCARDED + TOTAL_UNSENT always, including after EOF. Acknowledgment counts packet records once, even on duplicate-packet receipts; retry attempts do not double-count.

### PUT /config

A partial update to explicitly allowed effective run fields, despite the PUT method chosen in the prior design. Document this merge behavior and return full status/effective config. At least one field required. Optionally accept expected_control_revision; if supplied and stale return 409 CONTROL_REVISION_CONFLICT. No implicit reset.

- Hot fields: clock_multiplier, max_packets_per_second, live_weight, backlog_weight, partial_flush_wall_ms. Apply atomically at clock/packet boundaries as defined above.
- packet_size, encoding and request_timeout_ms may change only when no pending/in-flight packet exists; queued raw fixes retain identity and use new packet settings at selection.
- vehicle_id, route_id, run_start_ms, aggregator_url, expected_fix_interval_ms require READY with no pending records. To change during an existing stopped/completed run, first reset. This is the concrete meaning of “stopped and empty”: reset transitions that state to READY. Vehicle/route changes while running always fail.
- listen, simulator_id, identity_db_path, route map and resource limits are startup-only; reject API attempts to modify them.

Validate the whole update before changing anything. For an accepted route_id change, load/validate the allowlisted route before applying. Never send previously generated data under a new vehicle ID, target endpoint or timestamp origin.

### POST /start

Empty body. Start/resume as section 4. Does not clear coverage pause or a failed packet. To transmit again, caller explicitly resumes transmission. Return 200 status for success/no-op.

### POST /stop

Empty body. Freeze clock and stop selecting packets; retain queues/pending packet. An already in-flight send may complete. Return 200 status with in_flight/pending ID. Use stop plus waiting for in_flight=false before a safe reset.

### POST /pause-transmission

Empty body. Apply coverage outage semantics. Idempotent if already paused. If an error is blocked, set the coverage pause flag without deleting the error/pending packet; status transmission_state remains BLOCKED_ERROR with coverage_paused=true. Expose coverage_paused in all status responses to preserve this distinction.

### POST /resume-transmission

Body `{ "retry_failed": false }` by default. Clear coverage pause. If failed packet exists and retry_failed is false, return 409 RETRY_CONFIRMATION_REQUIRED with status and retained packet ID; leave state unchanged. With retry_failed=true authorize one retry of that immutable packet; clear the blocking gate for that attempt only. No pending failure plus retry_failed=true is a harmless no-op. A STOPPED run still cannot transmit until /start. Idempotent ordinary resume must not rearm an existing recovery gate. If retry fails, block again with a new error observation.

### POST /reset

Body `{ "discard_pending": false }` by default. Allowed only in READY, STOPPED or COMPLETED and with no HTTP request in flight. Otherwise 409 STOP_REQUIRED or SEND_IN_FLIGHT. If any unsent work (including failed packet) remains, default is 409 PENDING_DATA; discard_pending=true is explicit destructive authorization. Return discarded record count and whether a packet's prior acceptance was uncertain. Reset cannot undo fixes already accepted by aggregator, even if the local packet is discarded.

On success clear run ID, generated cursor, queues, failed packet, counters/errors and virtual time; return READY, coverage enabled, resolved start null. Retain effective config, validated route and **persistent identity counters**. Record explicit discards in response/logs before resetting per-run counters. Reset does not change stored aggregator history or close trips. Repeated reset in READY is a no-op.

### Error contract

Use AG-07 shape:

```json
{"error":{"code":"PENDING_DATA","message":"Reset requires explicit discard of unsent fixes.","details":{"total_unsent_fixes":100}}}
```

400 INVALID_REQUEST/INVALID_CONFIG/INVALID_ROUTE (include field/record), 404 NOT_FOUND/UNKNOWN_ROUTE, 409 for lifecycle/config preconditions above, 413 BODY_TOO_LARGE/ROUTE_TOO_LARGE, 415 UNSUPPORTED_MEDIA_TYPE, 503 IDENTITY_STORE_UNAVAILABLE or CONTROL_PREPARATION_TIMEOUT. Unsupported methods →405 with Allow. Telemetry send failures are reflected in status rather than making GET /status fail. Do not expose SQL errors or full CSV files.

The aggregator may forward only GET /status, PUT /config and the six agreed POST actions. No retry endpoint is added: retry_failed is carried by the already allowed /resume-transmission route. The aggregator proxy must pass these JSON bodies and normalize errors without automatically retrying controls.

## 9. Operational behavior and reliability boundaries

Use one state-owner goroutine or equivalent serialized service. Sender completion reports immutable packet ID/result back to that owner; validate identity before updating state. Request timeouts use wall time; playback scaling never changes network timeouts. /status and controls remain responsive during a slow aggregator request.

Persist counter allocation atomically before network activity; startup checks counter-store integrity and obtains instance lock. If allocation fails, retain selected fixes, block sending, report unhealthy identity state; do not invent an ID. Unlike an already materialized failed packet, allocation failure has no retryable HTTP body yet; an explicit resume may reattempt allocation once after store recovery. Document this subtype in last_error.

Structured logs: run transitions, packet IDs/lane/count, attempt outcome, queue sizes, explicit retry/discard, route validation errors and identity-store errors. Do not log full packet bodies by default. Simulator unavailable does not imply aggregator unhealthy; aggregator unavailable does not stop source generation unless operator stops it.

On first termination signal, stop generation/new attempts, allow the one in-flight request to finish within its configured timeout plus a small shutdown margin, and exit only if unsent count is zero. If unsent remains, stay stopped with control API active, log a clear warning, and require explicit reset/discard or resume/drain. A second termination signal forcibly exits and may lose memory-only pending work; print remaining count. If the process is killed/crashes/power fails, queue loss remains possible. Do not claim graceful behavior makes the in-memory outbox durable.

Document that route selection paths are administrator-controlled local inputs and that the P0 service is intended for loopback use. Do not add deployment/authentication infrastructure for this task.

## 10. SIM-07 — Acceptance tests

Use fake monotonic/wall clocks, deterministic run IDs, temporary counter databases and an injected HTTP sender for unit tests. Test real HTTP/encoding with httptest and the implemented aggregator in integration tests. Advance fake time rather than relying on long sleeps. Preserve a fixture with the exact supplied CSV sample.

| Test | Required result | Tasks |
|---|---|---|
| S01 | Supplied CSV headers/rows map correctly; quoted fields, CRLF and reordered headers work | SIM-01 |
| S02 | Missing/duplicate headers, malformed rows, invalid/nonfinite values and oversized inputs fail before playback with useful record/field errors | SIM-01 |
| S03 | ts=0 maps to positive run start; fractional half-up rounding, rounded collisions, nonincreasing source ts, overflow and nonzero initial ts behave as specified | SIM-02 |
| S04 | Playback at 10×,100×,1000×; delayed timer catch-up generates each due row once; hot multiplier change has continuous virtual time | SIM-02 |
| S05 | Stop/start excludes stopped wall time; coverage pause continues generation; EOF drains partial packets then completes; no automatic route loop | SIM-02/04 |
| S06 | Packet size/partial-flush timing, queue FIFO order, deterministic JSON/Protobuf values and exact input field mapping | SIM-03/04 |
| S07 | Persisted IDs never repeat across reset/restart, UTC date changes/rollback, and failed allocations; concurrent instance is rejected | SIM-03 |
| S08 | Pause moves unsent live to backlog, prevents new attempts, preserves in-flight identity; repeated pause/resume and multiple outages retain every fix | SIM-04 |
| S09 | After a >600s virtual outage, first new ordinary send is post-outage live (possibly partial); EOF-only recovery drains backlog without waiting for nonexistent live | SIM-04 |
| S10 | With both lanes eligible, 3 live then 1 backlog; empty lanes use spare slots; backlog receives ≥1 of each 4 ordinary attempts under continuous load | SIM-04 |
| S11 | Attempts, including failures/retries, respect wall spacing; no post-outage burst; rate changes and slow HTTP requests obey single-in-flight rule | SIM-05 |
| S12 | HTTP timeout, conflict, malformed receipt and lost response retain exact packet/body; no autonomous retry; explicit retry succeeds idempotently without double acknowledgment | SIM-03/08 boundary |
| S13 | Quarantine receipt counts as durable acceptance; latest acknowledged event time never regresses during backlog sends | SIM-03/05 |
| S14 | Atomic config validation, stale control revisions, immutable identity/run fields, stop/reset/discard guards and idempotent controls match API | SIM-06 |
| S15 | Every state transition satisfies generated = acknowledged + discarded + unsent; no queued fix belongs to two lanes or pending and queue | SIM-04/06 |
| S16 | Controls/status remain responsive during sends; stop/pause races, sender completion, reset guards and shutdown warnings are race-free | SIM-06 |
| S17 | Both encodings work against real aggregator; retry after simulated lost response changes aggregator state exactly once | SIM-07; AG-02/04 |
| S18 | Real aggregator fixture outage creates two current trips then backlog causes new-ID merge, MERGED predecessors and correct lineage; canonical position never regresses | SIM-07; E2E-01 |

For S18 use a regular ≥1800-second route, e.g. ts=0..2400 at 1s cadence. Establish first trip, pause coverage, advance virtual time >600 seconds, resume with future points still available and sufficient capacity. Gate sender in the test after first recovery live receipt and assert two current trips before permitting backlog drain. Then finish draining and verify one current component and preserved lineage. Intermediate IDs/revisions are not fixed constants. Keep the >600 requirement strict; exactly 600 connects and will not create the desired split.

Also test a live backlog overload at multiplier=1000, packet_size=10, rate=50: queue growth/congestion is expected; no dropped fixes. At adequate rate, finite-route EOF must eventually drain if the aggregator responds successfully.

Use table-driven tests and run:

```bash
go test ./...
go test -race ./...
go build ./cmd/simulator
```

Run a short wall-clock smoke test to confirm timer/rate behavior; record actual performance without promising throughput independent of HTTP latency and hardware. Do not weaken queue conservation or packet identity to reach a benchmark number.

## 11. Deliverables and task completion

- **SIM-01:** CSV adapter, validated configuration/CLI, immutable route data and fixture tests S01/02.
- **SIM-02:** Injected accelerated clock, run lifecycle, timestamp conversion and S03..05.
- **SIM-03:** Durable counter store, shared wire encoding, immutable pending packet, receipt validation and S06/07/12/13.
- **SIM-04:** Queues/outage/recovery/fairness and S08..10/15.
- **SIM-05:** Wall-clock limiter, status/rate/congestion diagnostics and S11/13 plus overload test.
- **SIM-06:** HTTP controls, proxy compatibility, state guards, shutdown and S14/16.
- **SIM-07 / G4:** All P0 tests, real aggregator integration S17/18, sample config, run instructions and deterministic outage/recovery script.
- **SIM-08 / P2:** Automatic retries/backoff later; do not mark complete merely because explicit retry exists.

Update [README.md](README.md) with actual commands/config/driver versions, simulator status/control examples and memory-only queue caveat. Keep checklist IDs stable and report contract deviations explicitly. Include fixtures under testdata and use existing CSV generation tooling externally; do not embed routing services.

Example operator flow after implementation:

```bash
# Separate terminal: start the simulator process.
go run ./cmd/simulator --config simulator.json
```

```bash
curl --fail-with-body -X POST http://127.0.0.1:8090/start
curl --fail-with-body http://127.0.0.1:8090/status
curl --fail-with-body -X POST http://127.0.0.1:8090/pause-transmission
# Let more than 600 simulated seconds pass, then restore coverage.
curl --fail-with-body -X POST http://127.0.0.1:8090/resume-transmission
```

If a send failed, explicitly retry the retained packet:

```bash
curl --fail-with-body -X POST http://127.0.0.1:8090/resume-transmission \
  -H 'Content-Type: application/json' --data '{"retry_failed":true}'
```

No details about dashboard layout or inspector implementation are required to complete this simulator.
