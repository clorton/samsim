# Go GPS telemetry aggregator — implementation specification

**Audience:** Claude Code implementing the weekend project. **Status:** normative implementation baseline, 2026-10-24. Companion: [TODO.md](TODO.md). `AG-01` through `AG-10` match its tasks; simulator, inspector, dashboard, and E2E task IDs are defined there. Implement the aggregator first; do not implement later components merely because this spec defines their integration contracts.

MUST denotes a requirement. The decisions below preserve the later agreements in “GPS Telemetry Dashboard Design” and explicitly fill implementation gaps. Earlier suggestions of a 60-second gap, JSON-only ingestion, or adding historical fixes to manually closed trips are superseded.

## Resolved decisions and scope

| Topic | Binding decision |
|---|---|
| Backend | Go; one module shared with future Go simulator and inspector |
| Transport | One HTTP ingestion endpoint, JSON or binary Protobuf by Content-Type; REST/JSON reads and WebSocket/JSON events |
| Persistence | SQLite, synchronous ingestion/reconstruction in one transaction |
| Fix identity | `(vehicle_id, timestamp_ms)`; signed 64-bit Unix epoch milliseconds |
| Packet identity | `(simulator_id, packet_id)`; packet ID `YYYYMMDD-SEQUENCE` |
| Segmentation | Consecutive event-time gaps **≤600 seconds connect**; larger gaps split; configurable |
| Meaning of trip | Telemetry continuity segment, not proof of movement |
| Activity | Latest known fix's nonclosed trip is ACTIVE; no wall-clock inactivity timeout |
| Late data | Update mutable trips; bridge creates a new ID and freezes predecessors as CLOSED/MERGED |
| Manual closure | All closed snapshots are immutable. A manual interval and its boundaries permanently block reconstruction across it |
| Closed historical fix | Preserve valid fix as unassigned/quarantined; never mutate closed geometry |
| Completeness | Detect internal expected-cadence gaps only; no claim of known boundary completeness |
| Closure warning | Recompute atomically; internal gaps require explicit `force`; optimistic revision precondition always required |
| Position | Maximum event timestamp over all valid stored fixes, including unassigned fixes |
| Events | Persistent per-vehicle revision, ephemeral post-commit WebSocket envelopes; no replay |
| Retry scope | Aggregator idempotency now; automatic simulator retries deferred |

The explicit new implementation choices are: protojson conventions, canonical fingerprint rules, inclusive manual interval barriers, full mutable-history recomputation for small datasets, revision preconditions, one event envelope per transaction, static control registry, and the exact API/error contracts. They resolve unanswered details rather than asserting that every field was previously agreed. The simulator consumes CSV files produced by the user’s existing tool that generates GPS fixes for a route between two addresses; route generation is outside this project’s scope (SIM-01/02). No dependency-version recommendation is implied: select compatible maintained Go packages at implementation time and pin the chosen versions in `go.mod`/`go.sum`.

<a id="ag-01"></a>
## AG-01 — Architecture, configuration, invariants

Repository layout:

```text
cmd/aggregator/main.go
cmd/simulator/                 # SIM-* later
cmd/gpsinspect/                # INS-* later
proto/telemetry/v1/telemetry.proto
gen/telemetry/v1/              # generated Go
internal/aggregator/           # transactional services
internal/trips/                # pure component/reconciliation logic
internal/storage/             # SQLite migrations and repositories
internal/httpapi/              # codecs, handlers, DTOs, WebSocket
internal/events/               # ordered broadcaster
internal/control/              # fixed simulator proxy
migrations/
testdata/
```

Use `net/http`, `database/sql` plus one pinned SQLite driver, generated Google Protobuf Go support and `protojson`, and one pinned WebSocket implementation. Trip/query DTOs are ordinary Go structs; only telemetry wire types need Protobuf. Inject clock and opaque unique trip-ID generator. Use integer arithmetic for timestamps/gaps; compare by subtraction only after checking overflow. Do not couple segmentation to coordinates, speed, arrival time, or simulator clock multiplier.

Default configuration:

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

Reject nonpositive/overflowing durations and limits at startup. Persist trip gap and expected interval in database metadata on first initialization; reject later startup with differing values. Changing them requires a future explicit migration/rebuild policy, not silent reinterpretation of frozen history. Bind loopback by default; this is a local demonstration without auth. Body/queue limits remain necessary for bounded memory.

Invariants after every commit:

1. Raw fixes have unique vehicle/timestamp identities and immutable values.
2. Each nonquarantined fix belongs to exactly one **current** trip (ACTIVE, INACTIVE, or CLOSED/MANUAL). Historical MERGED snapshots may also reference it; this is audit membership, not another current assignment.
3. Each current mutable trip is one maximal connected component within a manual-barrier partition. Adjacent fix gaps are ≤ configured gap. No mutable component intersects a manual interval.
4. At most one ACTIVE trip per vehicle; its pointer and status agree. It contains the latest stored fix iff that fix is assigned to a mutable trip. If the latest fix is manually closed or quarantined, active is NULL, even if older mutable trips exist.
5. Latest position is the maximum raw event timestamp and never regresses.
6. CLOSED rows and their snapshot memberships/geometry never change. MERGED successors may themselves later become MERGED; lineage is acyclic.
7. Packet, fix, trip, vehicle, barrier, lineage and revision mutations are atomic. No success acknowledgment or event precedes commit.

Use a per-vehicle lock for ingestion/closure and a process-wide writer coordinator for SQLite. Acquire vehicle lock before writer coordinator; avoid mixed lock ordering. Different vehicles can decode and read concurrently; SQLite commits serialize. Publish/enqueue committed events under the writer coordinator in commit order before releasing it, without doing network writes there. Run one aggregator process per database. Unique constraints remain the final defense; process locks alone are insufficient.

<a id="ag-02"></a>
## AG-02 — Wire schema and decoding

```protobuf
syntax = "proto3";
package telemetry.v1;
option go_package = "gpsdemo/gen/telemetry/v1;telemetryv1";
message GPSFix {
  int64 timestamp_ms = 1;
  double latitude = 2;
  double longitude = 3;
  float speed_mps = 4;
  float heading_degrees = 5;
}
message TelemetryPacket {
  string simulator_id = 1;
  string vehicle_id = 2;
  string packet_id = 3;
  repeated GPSFix fixes = 4;
}
```

Set module path consistently with the generated import. `POST /api/v1/telemetry` accepts `application/json` with `protojson.Unmarshal` and `application/x-protobuf` with `proto.Unmarshal`. Parse media type parameters with `mime.ParseMediaType`; JSON charset UTF-8 is acceptable. Missing/unsupported type → 415; malformed header/body → 400; excess bytes → 413; unsupported Content-Encoding → 415. Responses are JSON for either input format. Reject trailing JSON documents, unknown JSON fields, unknown binary fields, nonfinite numbers and conflicting duplicate JSON keys (prevalidate keys before protojson decoding). Enforce body limit before allocation and fix count after decoding.

Protojson outputs lowerCamelCase and quoted int64s; it accepts proto field names and valid numeric/string int64 input. Publish lowerCamelCase examples. Normalize parsed values before hashing; wire spelling/order is not packet identity. Example:

```json
{
  "simulatorId": "sim-001",
  "vehicleId": "vehicle-001",
  "packetId": "20261024-000001",
  "fixes": [
    {"timestampMs":"1792845600000","latitude":44.0582,"longitude":-121.3153,"speedMps":5,"headingDegrees":90}
  ]
}
```

IDs: ASCII `[A-Za-z0-9._-]`, 1–128 bytes for vehicle/simulator; packet ID is a valid Gregorian UTC date `YYYYMMDD-` followed by 1–20 decimal digits. Sequence formatting is part of packet ID (do not strip leading zeros). Date describes packet creation, not fix timestamps; historical packets need not match fix dates. Simulator ensures uniqueness across resets; aggregator checks identity/content, not arrival sequence monotonicity.

Require 1..max fixes, timestamp >0 and ≤253402300799999 (end of year 9999), latitude in [-90,90], longitude in [-180,180], speed ≥0, heading in [0,360). Coordinates of zero and epoch-adjacent positive timestamps are valid. No wall-clock future cutoff: acceleration intentionally generates future timestamps. Missing proto3 scalar speed/heading means zero; timestamp zero fails validation, while absent coordinates decode as zero by design. Optional measurement presence is out of scope. Canonicalize negative zero to positive zero. Compare speed/heading after conversion to schema float32; coordinates remain float64. Do not use epsilon equality for duplicate detection.

<a id="ag-03"></a>
## AG-03 — SQLite persistence

Use ordered versioned migrations. Enable WAL, foreign keys **on every connection**, busy timeout, and `synchronous=FULL`. Configure a single writer connection/coordinator; read-only connections may run concurrently. Use explicit transactions and coherent read transactions. Busy exhaustion → 503 with retry hint; never return partial success.

Schema below specifies required logical columns/constraints; migration SQL may add implementation columns and composite keys to enforce same-vehicle references. Store timestamps as INTEGER and bounds as REAL. IDs are TEXT. Avoid cascade deletes of history.

| Table | Required fields and constraints |
|---|---|
| `metadata` | key PK, value; schema/config values and random stable `database_id` |
| `vehicles` | vehicle_id PK, latest_fix_timestamp_ms nullable FK to same vehicle fix, active_trip_id nullable FK, revision INTEGER NOT NULL default 0, updated_at_ms |
| `packets` | simulator_id + packet_id composite PK, vehicle_id FK, canonical_sha256, received_at_ms, accepted_revision, stored_result_json |
| `gps_fixes` | vehicle_id + timestamp_ms composite PK, latitude, longitude, speed_mps, heading_degrees, first_simulator_id, first_packet_id, received_at_ms, assignment_reason nullable |
| `trips` | trip_id PK, vehicle_id FK, start_ms, end_ms, fix_count, min/max latitude/longitude, status, close_reason, superseded_by_trip_id nullable FK, created_revision, updated_revision, closed_at_ms nullable, forced_close boolean |
| `trip_fixes` | trip_id + timestamp_ms composite PK, vehicle_id; FKs to trip and raw fix; indexed vehicle_id/timestamp_ms |
| `trip_merges` | predecessor_trip_id PK FK, successor_trip_id FK, vehicle_id, revision, merged_at_ms; one immediate successor per predecessor |
| `manual_barriers` | trip_id PK FK, vehicle_id, start_ms, end_ms, closed_revision; immutable closed snapshot interval |

Checks: start≤end; fix_count>0; status in ACTIVE/INACTIVE/CLOSED; reason NONE/MANUAL/MERGED. Nonclosed requires NONE and no successor; CLOSED/MANUAL requires no successor; CLOSED/MERGED requires successor. Partial unique index on vehicle_id WHERE status='ACTIVE'. Index trips `(vehicle_id,status,start_ms,end_ms)`, raw fixes `(vehicle_id,timestamp_ms)` via PK, lineage successor, packet vehicle. Freeze closed rows/memberships in service logic and test enforcement; optional SQL triggers may strengthen it. Reassigning current membership never deletes frozen predecessor rows.

Assignment reason is `MANUAL_CLOSED_INTERVAL` only for quarantined raw fixes; otherwise NULL. Current assignment is derived via trip_fixes joined to non-MERGED trips. Retain all source fixes and packet fingerprints indefinitely for this weekend. Full raw packet bytes and every packet-to-duplicate-fix receipt are unnecessary. Geometry uses stored raw fixes in timestamp order; antimeridian-aware geographic bounds and computed distances are deferred (bounds are plain coordinate extrema).

<a id="ag-04"></a>
## AG-04 — Ingestion, idempotency, transaction contract

Pipeline:

1. Decode and validate the **entire** packet; no writes yet. Sort fixes by timestamp. Collapse identical repeated fixes in this packet and count them as duplicates. Conflicting repeated timestamp values → 409 `FIX_CONFLICT`.
2. Canonical fingerprint = SHA-256 of deterministic binary Protobuf for normalized packet containing sorted unique fixes. Include simulator, vehicle and packet IDs, all normalized measurement fields; reject unknown fields before this step. Thus JSON/binary, fix order, explicit default scalars and repeated identical fix multiplicity do not change packet content. Preserve original input count separately for the initial response.
3. Acquire vehicle lock and writer coordinator; start transaction. Lookup packet key globally (not just within vehicle). Same fingerprint → return stored original result with `duplicate_packet:true` and no reprocessing. Different fingerprint, including changed vehicle → 409 `PACKET_CONFLICT`.
4. Ensure vehicle exists. Compare all fixes to stored values. Any conflicting fix rolls back the entire packet, including otherwise new fixes. Exact matches count as duplicate fixes. Insert new valid fixes, quarantining those in manual intervals; reconstruct mutable trips synchronously (AG-05).
5. Recompute latest position and active pointer. Increment vehicle revision exactly once if any new fix was stored (quarantined included). A packet containing only already stored fixes adds a packet receipt but does not change revision or emit an event. Store original response and accepted revision in packet row.
6. Commit; enqueue AG-08 event only when state changed. Respond only after durable commit. On failure rollback everything and emit nothing. If process crashes after commit but before response/event, a resend recovers the stored result; disconnected event clients must resnapshot.

Response 200, including first acceptance:

```json
{
  "simulator_id":"sim-001", "packet_id":"20261024-000001",
  "duplicate_packet":false, "accepted_fixes":1, "duplicate_fixes":0,
  "quarantined_fixes":0, "trip_updates":1, "revision":1
}
```

`accepted_fixes` = newly stored raw fixes, including quarantine; `quarantined_fixes` is its subset. `duplicate_fixes` includes repeated input copies and existing identical fixes. For first acceptance accepted+duplicate=input count. `trip_updates` counts distinct trip rows created or changed, including predecessors/status changes. On packet replay, return original counters/revision with duplicate_packet=true; these describe original acceptance, not additional effects. Clients needing current state must GET snapshot. A 2xx receipt acknowledges durable raw acceptance, not that every fix joined a trip. Future automatic retries must preserve packet identity and normalized content.

<a id="ag-05"></a>
## AG-05 — Event-time trips, merging and active selection

For weekend-sized datasets, recompute all **mutable** components for the affected vehicle per packet. Preserve frozen closed snapshots. This is deliberately simpler than an optimized interval engine; benchmark before introducing an incremental implementation. Production-scale indexing/window recomputation is deferred.

Algorithm within transaction:

1. Partition event time around immutable manual intervals [start_ms,end_ms]. Existing closed interval members stay assigned; new fixes inside the interval are quarantined. Fixes outside intervals lie in distinct open partitions before/between/after barriers. Two timestamps on opposite sides of a barrier never connect, even if ≤gap apart.
2. Sort all eligible raw fixes in each partition. Split whenever the difference between consecutive timestamps is **greater than** `trip_gap_seconds * 1000`. Single-fix components are valid trips.
3. Reconcile each new component against pretransaction ACTIVE/INACTIVE trips by **shared fix membership**, not merely overlapping boundaries. No predecessor → allocate a new ID. One predecessor → keep ID and update its membership/bounds/count. Two or more → allocate **one new ID for that final component in this packet**, mark all immediate predecessors CLOSED/MERGED, set each successor pointer, add lineage edges, and copy full union plus new fixes into new trip membership. Old geometry/membership/bounds remain frozen. Compute final packet components before reconciliation to avoid meaningless intermediate merges within one request.
4. Recompute active pointer from maximum raw fix. If its current assignment is a mutable component, that trip is ACTIVE and all other mutable trips INACTIVE. Otherwise all mutable trips INACTIVE and pointer NULL. Do not activate an older trip when the newest trip was manually closed. Never deactivate because wall time elapsed.
5. Persist only changed rows/sets, using one new revision. Emit semantic changes describing the final transaction state.

With immutable config and additions only, mutable components can grow or merge, never split. If reconciliation finds a split, fail/rollback as an invariant violation rather than silently corrupting lineage. Only closure removes one component from mutable reconstruction, and inserts its barrier atomically.

Example using positive base timestamp T and offsets in seconds: ingest {0,600,1800,2400} → two trips; ingest {1200} → one new merged trip, since both joining gaps are exactly 600. A gap of 600001 ms separates components. With no manual operations, any packet/arrival permutation of the same valid fix set yields identical current memberships, boundaries and latest position. IDs, intermediate lineage, receipt times and revisions may differ. Manual closure is an explicit ordered operation, so permutation equivalence applies only within the same barrier/closure history.

Merged trip GET retains its own frozen snapshot plus immediate successor and resolved terminal successor. New fixes are reconciled against current mutable successors, never appended to predecessors. New late fixes can extend a historical INACTIVE trip backward or internally without moving current position. No deletion endpoint is needed; do not emit an undefined `trip.deleted` event.

<a id="ag-06"></a>
## AG-06 — Completeness and manual closure

Completeness examines only that trip's ordered snapshot timestamps (including frozen snapshots when read). For consecutive a,b and expected interval I, missing count is `max(0, ceil((b-a)/I)-1)`, implemented with overflow-safe integer arithmetic. When count>0, report expected slots a+I through a+count*I inclusive. This is a cadence estimate anchored at each observed fix, not a guarantee of a global sampling grid. Example 1000 ms interval: gap 11000 ms → 10 missing; gap 1500 ms → 1 expected slot. Irregular/jittery source timing may trigger warnings; simulator uses a regular cadence. Do not enumerate every missing timestamp; return intervals/counts, paginated if necessary.

Completeness DTO: `trip_id`, `revision` (vehicle revision), `expected_fix_interval_ms`, `missing_fix_count`, `missing_intervals:[{start_ms,end_ms,missing_count}]`, `complete` (=no internal gaps), `boundary_completeness:"UNKNOWN"`, `assessment:"NO_INTERNAL_GAPS"|"INTERNAL_GAPS"`. Single fix has no detected internal gaps. Label complete in clients as “no detected internal gaps”; it does not certify all telemetry arrived. No simulator dependency or authoritative watermark is assumed. Pending backlog status may be shown as additional client context, never used as proof of completeness.

`POST /api/v1/trips/{id}/close` body:

```json
{"expected_revision":42,"force":false}
```

Require expected_revision; missing/invalid → 400. Under the same vehicle/write locks and transaction as ingestion: verify identity and state, compare current vehicle revision, recompute completeness, then close. Stale revision → 409 `REVISION_CONFLICT` with current revision; force does not bypass staleness. CLOSED/MERGED → 409 `TRIP_SUPERSEDED` with resolved successor; do not silently close successor. CLOSED/MANUAL → 200 idempotent no-op returning frozen closure/current revision even if supplied expected revision is older; no new revision/event. This supports retry after a lost close response.

For a mutable trip with internal gaps and force=false, return 409 `TRIP_INCOMPLETE`, requires_confirmation=true, completeness and current revision; no mutation. Client displays warning and resubmits force=true using the reviewed revision. A concurrent update invalidates confirmation; require fresh review. If no internal gaps, force=false succeeds while boundary uncertainty remains visible. Force=true is an explicit user acknowledgment, recorded in forced_close. Store completeness-at-close counts in closure audit metadata (add columns/JSON on trips) so later views can explain the decision.

Successful closure freezes membership, bounds/counts/status CLOSED/MANUAL, inserts inclusive barrier [existing start,existing end], clears active pointer if necessary, increments revision once, and emits closure changes. Closing an INACTIVE trip does not deactivate a newer active trip. Event-time end is last member timestamp, **not wall-clock close time**; record actual server close time separately.

After closure:

- Identical retries of existing fixes remain duplicates; conflicts still fail normally.
- A new timestamp inside [start,end] is stored with MANUAL_CLOSED_INTERVAL and no trip membership. No automatic repair/unquarantine or reopen endpoint this weekend.
- A fix after end may start/join a new mutable trip on that side, even within 600 seconds; the closed trip stays unchanged. If there is no mutable trip there, it creates a new ID. A late fix before start similarly belongs only to that earlier partition.
- No mutable trip can bridge from before start to after end. Multiple manual barriers remain disjoint because only existing nonoverlapping components may be closed.

This chooses quarantine, not whole-packet rejection, for otherwise valid closed-interval data. Invalid/conflicting telemetry still rolls back the entire packet per AG-04.

<a id="ag-07"></a>
## AG-07 — REST reads, snapshots, errors

All nontelemetry API JSON uses snake_case, numeric epoch milliseconds and numeric revisions. Values fit JavaScript safe integers within the timestamp bounds above. IDs are opaque strings; nullable references are explicit null. Telemetry retains protojson's AG-02 conventions. Publish these differences in README/examples.

| Endpoint | Required behavior |
|---|---|
| `GET /api/v1/vehicles` | All known vehicles; optional `active_only=true` means active_trip_id is nonnull, not “recently heard from” |
| `GET /api/v1/vehicles/{id}` | Vehicle ID, latest raw fix, active_trip_id, revision, database_id, effective segmentation config |
| `GET /api/v1/vehicles/{id}/snapshot` | One read-transaction snapshot of vehicle + all current trip summaries + immutable manual barriers, with one revision/database_id; initial dashboard/validator baseline |
| `GET /api/v1/vehicles/{id}/trips` | Current trips by default (ACTIVE, INACTIVE, CLOSED/MANUAL); `include_merged=true` includes audit predecessors; optional status filter |
| `GET /api/v1/vehicles/{id}/fixes` | All raw fixes, including quarantine, current_trip_id nullable, assignment_reason; chronological; optional inclusive from_ms/to_ms and `assignment=assigned|quarantined|all` (default all) |
| `GET /api/v1/trips/{id}` | Frozen or current metadata; close/merge details, immediate predecessors, immediate successor and resolved terminal successor |
| `GET /api/v1/trips/{id}/fixes` | That trip's chronological snapshot geometry, including MERGED predecessor geometry |
| `GET /api/v1/trips/{id}/completeness` | AG-06 assessment from a coherent read transaction |
| `POST /api/v1/trips/{id}/close` | AG-06 closure |
| `GET /api/v1/health` | 200 if migrations complete, DB usable and writer operational; otherwise 503; include database_id and configured gap |

Trip summary: `trip_id`, `vehicle_id`, `start_ms`, `end_ms`, `fix_count`, `bounds:{min_latitude,max_latitude,min_longitude,max_longitude}`, `status`, `close_reason`, `superseded_by_trip_id`, `created_revision`, `updated_revision`, `closed_at_ms`, `forced_close`. Trip detail adds lineage IDs and closure assessment. Fix DTO includes timestamp_ms and measurements. Vehicle latest fix includes the same canonical fields even when unassigned. Raw packet receipt diagnostics may be added P1 but are not required for validation.

List envelopes: `{items:[],next_cursor:null,revision:42,database_id:"..."}` for per-vehicle lists; global vehicle list has database_id and per-item revisions (no invented global revision). Default limit 100, maximum 1000, cursor pagination. Fix order timestamp ascending; trip order `(start_ms,trip_id)` ascending; vehicle order ID ascending. An opaque cursor binds query/filter/order and for per-vehicle data its revision. Stale cursor → 409 `REVISION_CONFLICT`, prompting restart. Global vehicle pagination is best effort across changes; use per-vehicle snapshots for correctness. Include optional `at_revision=N` on all per-vehicle/trip GETs; this is a precondition against current revision, not historical time travel. If current revision differs return 409. Thus an inspector can pin multiple paged reads to its snapshot revision and restart if ingestion intervenes.

Snapshot includes all current summaries in one body for the bounded weekend dataset; paginate geometry separately. No cross-endpoint historical MVCC API is promised. Enforce a documented demo-size ceiling or return 413 `SNAPSHOT_TOO_LARGE` if response would exceed configured snapshot size (default 16 MiB); never silently truncate summaries. This limit is separate from ingestion body size.

Unknown resource →404; well-formed nonexistent IDs do not create vehicles via reads. Standard error shape:

```json
{"error":{"code":"TRIP_INCOMPLETE","message":"Trip has missing internal fixes.","details":{"requires_confirmation":true,"revision":42,"missing_fix_count":10}}}
```

Use the same nesting for all errors, and include complete AG-06 assessment in the incomplete-error details. Codes: 400 INVALID_REQUEST/INVALID_TELEMETRY, 404 NOT_FOUND, 409 PACKET_CONFLICT/FIX_CONFLICT/REVISION_CONFLICT/TRIP_INCOMPLETE/TRIP_SUPERSEDED, 413 BODY_TOO_LARGE/SNAPSHOT_TOO_LARGE, 415 UNSUPPORTED_MEDIA_TYPE/UNSUPPORTED_CONTENT_ENCODING, 503 STORAGE_UNAVAILABLE. Fix-conflict details identify vehicle/timestamp; packet-conflict details identify packet key. Do not leak SQL or stack traces. Non-GET unsupported methods →405 with Allow header.

<a id="ag-08"></a>
## AG-08 — Revisions, semantic events and WebSocket recovery

Persist revision per vehicle starting at zero. Every committed vehicle-state-changing ingestion or first manual closure increments exactly once, regardless of fix/change count; rejected operations, duplicate-only packets and repeat closes do not increment. Revision survives restart; overflow must fail safely. A new database gets a different database_id so clients cannot confuse reset revision numbers with an old database.

Use **one WebSocket event envelope per changed vehicle transaction**, with a changes array. This removes ambiguity about multiple events sharing a revision and allows strict consecutive-gap checks:

```json
{
  "event_id":"database-uuid:vehicle-001:43",
  "database_id":"database-uuid", "vehicle_id":"vehicle-001",
  "revision":43, "type":"vehicle.changed", "timestamp_ms":1792845600000,
  "changes":[
    {"type":"trips.merged","predecessor_trip_ids":["trip-a","trip-b"],"successor_trip_id":"trip-c"},
    {"type":"vehicle.updated","active_trip_id":"trip-c","latest_timestamp_ms":1792845600000}
  ]
}
```

`timestamp_ms` is server commit-associated wall time, not fix event time. Supported semantic changes: `trip.created` (ID), `trip.updated` (ID), `trip.closed` (ID, reason MANUAL), `trips.merged` (all immediate predecessor IDs + new ID), `vehicle.updated` (position/active pointer), `telemetry.quarantined` (count). A merge change covers predecessor closure and successor creation; avoid redundant create/close changes for those IDs. `trip.updated` includes ACTIVE↔INACTIVE status changes on other affected trips. Include only actual changes; diagnostics like raw packet receipt are not revision events. Events are invalidation/reconciliation hints; clients fetch authoritative snapshot/geometry rather than reconstructing trips themselves.

Endpoint: `WS /api/v1/events?vehicle_id=vehicle-001`. Require one vehicle filter for the P0 protocol; unknown vehicle may subscribe (revision zero) before first telemetry. Reject malformed filters before upgrade. Subscription registration and `ready` enqueue must run under the same writer coordinator used by commits; ready is the first frame, and any later commit is delivered after it:

```json
{"type":"ready","database_id":"database-uuid","vehicle_id":"vehicle-001","revision":42}
```

Client protocol (INS-04/DASH-03):

1. Open socket, wait for ready, begin buffering envelopes.
2. GET coherent vehicle snapshot (a not-yet-created vehicle may return 404; wait for its first event and retry).
3. Compare database_id; discard/reconnect on mismatch. Install snapshot revision R, discard buffered revisions ≤R, process >R in increasing order. An event arriving during the fetch remains buffered.
4. Every next envelope must be R+1. For a gap, reconnect, overflow or out-of-order envelope, stop applying updates and obtain a fresh subscribe-first snapshot. Duplicate envelopes ≤R can be ignored. On an ordinary valid event, fetch snapshot; it can jump ahead to the current revision, then discard buffered envelopes covered by that snapshot.
5. Geometry reads pass at_revision for consistency; 409 means refresh baseline. A client need not render every intermediate revision.

The server offers no persistent replay or `since` guarantee. Network writes use per-client bounded queues, deadlines, ping/pong and cancellation. If a queue overflows, close that socket (1013); never silently drop an event and continue. Writer coordinator only enqueues nonblocking. A slow client cannot block ingestion. A process crash can lose committed-but-unpublished envelopes; sockets disconnect and clients resnapshot. On graceful shutdown stop accepting mutations, finish bounded in-flight transactions, close sockets, then close database.

<a id="ag-09"></a>
## AG-09 — Simulator control-plane proxy

Required integration for SIM-06 and DASH-04; it can be implemented/tested with a stub before simulator exists. Load static trusted mapping `{simulator_id,control_url,vehicle_id}` from local config. No arbitrary browser-supplied URLs or dynamic registration this weekend. Config vehicle_id is a startup hint; simulator `/status` is authoritative after reconfiguration. Proxy does not change trip lifecycle or ingest state itself.

| Aggregator endpoint | Upstream simulator endpoint |
|---|---|
| `GET /api/v1/simulators` | Return configured IDs and hints; do not block on every upstream |
| `GET /api/v1/simulators/{id}/status` | `GET /status` |
| `PUT /api/v1/simulators/{id}/config` | `PUT /config` |
| `POST /api/v1/simulators/{id}/{action}` | POST matching `/start`, `/stop`, `/pause-transmission`, `/resume-transmission`, `/reset` only |

Fixed allowlist, 3-second upstream timeout, bounded request/response sizes, context propagation, no redirects, no implicit retries of controls. Unknown ID→404; unlisted action→404; upstream unavailable→502 SIMULATOR_UNAVAILABLE; timeout→504 SIMULATOR_TIMEOUT; pass upstream validation 4xx using normalized error envelope. Return successful JSON status. Distinguish simulator failures from aggregator readiness. Simulator status includes playback/transmission state, generated watermark, live/backlog queue counts, current vehicle ID and effective config. These diagnostics do not establish authoritative aggregator completeness. No simulator-control events need consume vehicle telemetry revisions; clients can poll status.

<a id="ag-10"></a>
## AG-10 — Acceptance tests, operation and handoff

Implement tests alongside each task, using temporary on-disk SQLite databases for persistence/concurrency and injected clocks/IDs for deterministic domain tests. Use valid positive base timestamps in fixtures (T+offset), not timestamp zero. Small deterministic fixtures are the release gate; randomized seeds must be printed on failure.

| Test ID | Setup/assertion | Tasks |
|---|---|---|
| T01 | Equivalent JSON and binary, int64 strings/numbers, default scalars and fix reorder yield identical normalized meaning/fingerprint | AG-02/04 |
| T02 | Invalid coordinates/time/nonfinite values, unknown fields, duplicate keys, malformed/trailing JSON, wrong media type, oversized body, excessive fixes; no writes | AG-02/04 |
| T03 | Same packet repeated, including alternate encoding → stored result, no revision/event; changed content/vehicle with same key →409; packet keys scoped by simulator | AG-04 |
| T04 | Overlapping packets, in-packet repeated identical fixes → correct duplicate counts; conflicting fix anywhere rolls back all new data and packet receipt | AG-03/04 |
| T05 | Consecutive gaps 599999,600000,600001 ms; single fix; backward extension; internal insertion; latest-first then older data never regresses position | AG-05 |
| T06 | T+{0,600,1800,2400}s then +1200s → fresh merged ID, two frozen predecessors, correct union/lineage; multiple components connected by one packet produce one final successor | AG-05 |
| T07 | Repeated merges resolve predecessor→successor chains; closed predecessor membership/bounds remain unchanged; no cycles | AG-05 |
| T08 | Shuffle same fix set across orders/batches: same current membership/bounds/activity, IDs/history allowed to differ; use ≥50 seeded permutations | AG-05 |
| T09 | New disconnected latest trip deactivates prior; no timeout; closing active clears pointer; late old fix does not reactivate it; closing inactive preserves newer active | AG-05/06 |
| T10 | Completeness regular/irregular cadence, singleton, large gap, configured interval, missing intervals/count totals, boundary UNKNOWN | AG-06 |
| T11 | Incomplete close rejected; explicit force succeeds; stale revision including forced close rejected; repeated closed-manual close is no-op; merged target reports successor | AG-06 |
| T12 | Close then internal late fix → quarantine, frozen snapshot; before/after fixes respect barrier even within gap; no cross-barrier merge; identical closed member stays duplicate | AG-04/06 |
| T13 | Concurrent ingestion/closure serialized; expected revision protects reviewed closure; concurrent same packet stores once; different packets conflict safely on fix identity | AG-01/04/06 |
| T14 | Inject precommit failure → no packet/fix/trip/revision/event; simulate committed request with lost response → retry idempotent; reopen DB retains lineage/barriers/config/revisions | AG-03/04/08 |
| T15 | Coherent snapshots, filters, raw quarantine visibility, frozen geometry, cursor progression, stale cursor/at_revision rejection, unknown IDs, standard errors | AG-07 |
| T16 | One envelope per changed transaction, contiguous revisions, none on duplicates/rollback, ready-first snapshot race, slow-client closure, reconnect resnapshot/database reset | AG-08 |
| T17 | Stub control proxy allowlist, timeout, upstream error, no redirect/retry; no telemetry revision effects | AG-09 |
| T18 | Outage >600 simulated seconds, fresh-first recovery, delayed bridge, manual warning/forced close, postclose late/future fixes, restart | AG-10; E2E-01..04 |

Test raw/current/audit membership separately; “each fix belongs to exactly one trip” without that qualification is incorrect after merges. Add invariant assertions after every operation in randomized tests. For closure model tests compare identical ordered closure histories, not arbitrary permutations across manual operations. Freeze snapshots in test expectations before closing/merging and compare again after later ingestion.

Run `go test ./...` and `go test -race ./...`; compile all implemented commands, run migration/restart integration tests, and record results in README. A modest throughput smoke run (e.g. 1000 fixes/s for 30 seconds in batches of 10) should report actual latency/throughput and errors rather than promise a machine-independent performance target. Do not sacrifice atomic reconstruction or durability to reach a number.

Operational minimum: startup config/migration logs; request IDs and structured errors; packet accepted/duplicate/conflict/quarantine counts; ingestion duration and SQLite busy counts; active socket/overflow counts. Never log full packet bodies by default. Health is not based on simulator availability. On unrecoverable invariant/storage corruption stop accepting mutations and report unhealthy; do not commit partial repair.

Implementation deliverables: generated schema plus reproducible generation command, pinned Go dependencies, migrations, source, tests, sample JSON/binary generation instructions, config example, and README commands for running aggregator and querying fixtures. Include a short decision log matching this spec and report deviations explicitly. No unresolved product choice should block AG-01..10; dashboard UX choices intentionally remain outside aggregator scope.

## Simulator CSV integration boundary — SIM-01/02

The existing route generator’s CSV is the simulator’s route input. The aggregator continues to accept only AG-02 JSON/Protobuf packets; it does not parse CSV or accept addresses. No new routing/address-to-fix generation tool is required.

The supplied CSV header is:

```csv
ts,lat,lon,speed_mps,heading_deg,dist_m,speed_source
```

Use the following fixed default adapter; an arbitrary column-mapping UI is unnecessary for the weekend:

| CSV column | Interpretation | Packet field |
|---|---|---|
| `ts` | Nonnegative elapsed seconds from route start, possibly fractional | `timestamp_ms = run_start_ms + round(ts * 1000)` |
| `lat` | Latitude in degrees | `latitude` |
| `lon` | Longitude in degrees | `longitude` |
| `speed_mps` | Speed in meters/second | `speed_mps` |
| `heading_deg` | Heading in degrees | `heading_degrees` |
| `dist_m` | Cumulative route distance in meters | None; optional simulator diagnostic |
| `speed_source` | Source annotation, e.g. `osrm_estimate` | None; optional simulator diagnostic |

Interpretation of `ts` as elapsed seconds is the explicit baseline inferred from the supplied sequence 0.0, 1.0, 2.0, … . It is **not** Unix epoch time. Require an explicit positive epoch-millisecond `run_start_ms` in simulator run configuration; permit a convenience “now” option resolved once at start and reported in status. Do not subtract the first row’s `ts`: zero is the route origin, and a file starting later retains its initial offset. For example `run_start_ms=1792845600000`, `ts=0.0` → `1792845600000`, `ts=1.0` → `1792845601000`. This zero elapsed time is valid even though AG-02 forbids zero epoch timestamps.

Parse `ts` as a finite decimal and convert seconds to integer milliseconds with nearest-millisecond rounding (half upward for nonnegative values). Prefer decimal/rational parsing to avoid binary floating-point boundary errors. Validate both raw `ts` and converted offsets as strictly increasing; reject distinct source times that round to the same millisecond. Check arithmetic overflow and AG-02 timestamp bounds. Preserve CSV row order and the supplied relative timing; do not resample by the configured fix interval. That interval describes expected cadence for completeness checks. Playback scheduling follows offsets divided by clock multiplier; transmission batching and outages do not change fix timestamps. A future timestamp-free adapter must be explicitly selected rather than silently falling back when `ts` is malformed or missing.

Use Go’s CSV reader with a comma delimiter and header-based lookup (allow reordered columns). Require the five mapped telemetry headers and nonempty valid numeric values for every row; do not silently default missing measurements in this adapter. Additional columns may be ignored. `dist_m` and `speed_source` are optional and are not part of telemetry identity, packet hashes or trip reconstruction. If retained as diagnostics, validate `dist_m` as finite/nonnegative/nondecreasing; treat `speed_source` as text without restricting it to `osrm_estimate`.

Validate the complete input before playback; report missing/duplicate headers, inconsistent record lengths, malformed quoting, missing required values, and invalid coordinates/times with record locations. Do not silently drop or reorder rows. CSV tests must include the supplied sample as a fixture, reordered headers, quoted fields, missing values, invalid measurements, fractional-second rounding/collisions, nonincreasing `ts`, and epoch conversion. A 1.0-second source cadence agrees with the default 1000 ms expected interval. The CSV schema is now sufficient to implement SIM-01/02 without requesting another sample.

## Completion map

- **G1:** AG-01..04 and T01..04/T14 storage checks.
- **G2:** AG-05..06 and T05..13.
- **G3:** AG-07..10 and T14..17; independent fixture-driven aggregator operation.
- **G4..G7:** SIM/INS/DASH/E2E tasks in [TODO.md](TODO.md), consuming the same contracts.

Automatic simulator retransmission, durable event replay, incremental large-scale reconstruction, source watermarks, reopening/repairing closed trips, packet payload archives, sophisticated movement detection, auth/cloud deployment, and detailed dashboard interaction design are deferred. Preserve the interfaces needed for future work without building those systems now.
