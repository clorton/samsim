# Go GPS inspector CLI — implementation specification

**Audience:** Claude Code implementing `gpsinspect`. **Status:** normative baseline, 2026-10-24.

Companions: [TODO.md](TODO.md), [AGGREGATOR_SPEC.md](AGGREGATOR_SPEC.md), [SIMULATOR_SPEC.md](SIMULATOR_SPEC.md), [README.md](README.md). INS-01..INS-07 match checklist tasks. AG-06..08 define the authoritative closure, query and event contracts. Implement after aggregator and simulator; do not build a dashboard or directly access the aggregator database.

## 1. Purpose and resolved scope

Provide a Go command-line tool to inspect vehicles, raw fixes, current trips and merge history; analyze missing fixes; manually close trips with the same safeguards as the future dashboard; watch semantic events; and independently check reconstruction invariants through public APIs.

MUST denotes a requirement. P0 commands:

```text
gpsinspect vehicles
gpsinspect vehicle <vehicle-id>
gpsinspect trips <vehicle-id>
gpsinspect trip <trip-id>
gpsinspect gaps <trip-id>
gpsinspect close <trip-id>
gpsinspect watch <vehicle-id>
gpsinspect validate <vehicle-id>
```

This spec explicitly resolves client output, exit codes, bounded revision retries, closure confirmation, event recovery, and the limits of validation. No authentication, terminal UI framework, direct SQLite mode, telemetry submission, simulator controls, or Python test harness is required. INS-07's Python harness is deferred. `validate` MUST NOT import or call aggregator reconstruction helpers; sharing wire/query DTOs or HTTP primitives is fine, sharing the algorithm being checked is not.

## 2. INS-01 — Structure and build

Same Go module as backend. Use Cobra for command parsing, `net/http` for REST, and a pinned compatible WebSocket client. Prefer reusable ordinary Go packages:

```text
cmd/gpsinspect/main.go
internal/inspectclient/        # REST DTOs, errors, pagination, revision collection
internal/inspector/            # command services, report types, close workflow
internal/inspectvalidate/     # independent reference model and baseline comparison
internal/inspectwatch/        # subscription and snapshot recovery
internal/inspectoutput/       # table/JSON/NDJSON formatting
testdata/inspector/
```

Keep `os.Exit` in main only. Inject IO streams, clock, terminal detection and HTTP/WS clients. Tests must not require an interactive terminal or a running service unless labeled integration. Do not connect to any service for `--help`/`--version`.

Implement:

```bash
go run ./cmd/gpsinspect --help
go build -o bin/gpsinspect ./cmd/gpsinspect
```

The build directory must be created by documented setup if needed. Pin dependencies in go.mod/go.sum; generation tools are not required just to run a built inspector.

### Global flags and configuration

| Flag | Default / meaning |
|---|---|
| `--base-url` | `http://127.0.0.1:8080`; env `GPSINSPECT_BASE_URL` fallback |
| `--timeout` | `5s` per HTTP request/WS connection handshake; env `GPSINSPECT_TIMEOUT` fallback |
| `--json` | Finite commands: one JSON document; watch: NDJSON |
| `--page-size` | 100; integer 1..1000 |
| `--revision-attempts` | 3 complete collection attempts; integer 1..10 |
| `--max-fixes` | 1000000 fetched raw/audit geometry rows total per command |
| `--max-response-bytes` | 16777216 per response/frame |
| `--no-color` | Disable optional ANSI output; color disabled when output not a terminal |
| `--version` | Print injected build version or `dev` |

Precedence: explicit flag > environment > default. No config file is required in P0. Only the named environment variables are recognized. URL must be absolute HTTP/HTTPS with host, no credentials/query/fragment; root path only. Append fixed API paths and escape IDs/path components/query values safely. Do not follow redirects or forward credentials. A request timeout is not a total watch duration. Invalid args/config fail before network IO. Request/response content is REST JSON, not telemetry protojson.

Read each response with a byte limit and validate required fields/types and resource identity. Permit unknown fields for forward compatibility but reject malformed/truncated data, inconsistent pagination, and invalid identities/revisions. Decode timestamps/revisions into integer types, not float64 maps. Check status before decoding a success body. Retain AG-07 error codes/details in a typed client error; avoid dumping HTML or stack traces. No unbounded automatic retries on network/5xx errors for finite commands. Revision conflicts alone trigger the bounded whole-read restart protocol below.

## 3. Output and process contract

Human output: clear tables for lists; labeled fields for details; UTC ISO-8601 timestamps with milliseconds and raw epoch ms in detailed reports. Never infer event time from packet receipt time. Use `-` for null pointers and explicit ACTIVE/INACTIVE/CLOSED and NONE/MANUAL/MERGED labels. Bounds are coordinate extrema, not map-matched geometry. Empty lists succeed and say no matching items.

Finite JSON output has a stable versioned envelope:

```json
{"schema_version":1,"command":"trips","data":{},"warnings":[]}
```

Data is a documented command report (lists include all collected items, database_id and relevant revision). Successful validation with failed checks still uses a report, not an API-error envelope. Finite command failures use:

```json
{"schema_version":1,"command":"trip","error":{"code":"NOT_FOUND","message":"Trip not found.","details":{}}}
```

Write successful reports to stdout. Human warnings/prompts/errors go to stderr. In --json mode, put finite error details only in the stdout error document, with at most a short diagnostic on stderr; do not contaminate stdout with progress or prompts. Buffer bounded finite results until consistency is established; never print partial data from an attempt later discarded for revision conflict. Watch uses the separate NDJSON rules below.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Command completed successfully; validate has no FAIL checks (SKIP/WARN may remain) |
| 1 | API/network/protocol/local IO failure, unknown resource, or incomplete validation collection |
| 2 | Usage/flag/config error |
| 3 | Completed validate report contains at least one invariant FAIL |
| 4 | Close canceled or required confirmation absent |
| 5 | Consistent revision could not be collected after configured attempts, or closure revision became stale |
| 130 | User interruption (SIGINT) |

No matching items is not failure. Never claim validation passed when resource limits, interrupted collection or schema errors prevented it. Cobra must not print usage for ordinary API errors.

## 4. Shared coherent-read and pagination protocol — INS-02/05

Global vehicles list is best effort across pages per AG-07; deduplicate by vehicle_id, preserve sorted order, and warn it is not a global transactional snapshot. Abort cursor cycles or inconsistent database_id; do not invent a global revision.

For any multi-request report concerning one vehicle:

1. Read `/api/v1/vehicles/{id}/snapshot`, obtain database_id D and revision R.
2. Read needed lists/details/geometry/completeness using `at_revision=R` on every request, including cursor pages. Verify every returned database_id/revision when present. Paginate to next_cursor=null, with fixed filters/page size and cycle detection.
3. Before accepting the report, reread vehicle state with at_revision=R; reject changed D or revision. This ensures all accepted responses describe one consistent revision even though no historical snapshot service exists.
4. On REVISION_CONFLICT, discard the entire attempt, obtain a new snapshot and restart. At most revision-attempts total; no sleep is required. Exhaustion returns code 5 with instruction to stop generation/transmission or try again. The inspector never stops the simulator itself.

For trip-id commands, first GET trip to discover vehicle_id, then run the above protocol and refetch the trip at R. IDs are stable even after merges; never silently redirect a trip command to its successor. Resource missing at a pinned revision is an error, not permission to mix newer state. Single-read vehicle command may directly use its coherent state response. Report actual revision, not event revision observed earlier. Respect max-fixes across all geometry/raw pages, including audit copies; exceeding it is an incomplete-collection error, not a partial pass. Cap finite list collection at 100000 items to bound nonfix data too, and report exceeded limits clearly.

## 5. INS-02 — Inspection commands

### vehicles

`gpsinspect vehicles [--active-only]`

GET vehicles, optionally active_only=true, collect pages. Show vehicle ID, latest timestamp, active trip ID, revision. Active means a nonnull active trip, not recent network traffic. Database reset/change during pages aborts.

### vehicle

`gpsinspect vehicle <vehicle-id>`

Show vehicle ID, database ID/revision, canonical latest fix (including coordinates, speed/heading, event timestamp), active trip ID, configured gap and expected cadence. Empty latest fix is represented explicitly. Do not fetch simulator status or claim latest position necessarily belongs to active trip.

### trips

`gpsinspect trips <vehicle-id> [--include-merged] [--status ACTIVE|INACTIVE|CLOSED]`

Default list is current history (ACTIVE, INACTIVE, CLOSED/MANUAL); include-merged adds audit predecessors. Apply optional status server-side. Collect pinned pages sorted by start_ms then trip_id. Show ID, event-time start/end, status/reason, count and immediate successor when present. Clearly label MERGED audit rows rather than counting them as additional current journeys.

### trip

`gpsinspect trip <trip-id>`

Read detail and completeness at a coherent revision. Show bounds, count, status/reason, created/updated revisions, actual server close time, forced flag, closure-time assessment if available, immediate predecessors, immediate successor and resolved terminal successor. Show missing internal count/interval summary and boundary uncertainty. P0 need not print every GPS point; geometry is fetched when validate requires it. Do not silently follow successor identity. If server omitted a required detail, report a protocol error rather than fabricate it.

### gaps

`gpsinspect gaps <trip-id> [--interval-limit N]`

Default displayed interval limit 20, positive maximum 1000; count totals remain authoritative regardless of display truncation. Show expected cadence, missing_fix_count and intervals with inclusive expected-slot endpoints/count. Always label `complete=true` as **No detected internal gaps; boundary completeness unknown**. Completeness is a cadence estimate, not proof of complete receipt. Include displayed/total interval counts and truncation flag in JSON. If server paginates intervals, collect pinned pages before applying display limit. Do not invent a pagination format: use the implemented published AG-06 API; if it needs pagination, coordinate the exact envelope with the aggregator spec before coding that extension.

## 6. INS-03 — Safe manual closure

`gpsinspect close <trip-id> [--force]`

This command is explicit authorization to close a trip with no detected internal gaps. `--force` is explicit acknowledgment of known missing internal fixes. Do not add a general --yes flag or silently infer force from JSON/noninteractive mode.

Workflow:

1. Collect current trip detail and completeness at one revision R. Always warn that boundaries are unknown and closure is immutable. For CLOSED/MANUAL, report already closed, succeed without POST. For CLOSED/MERGED, fail with TRIP_SUPERSEDED and the successor ID; do not close successor.
2. If gaps are known, print the count/interval summary and that late internal fixes will be quarantined. Without --force, if stdin/stderr are terminals and --json is false, prompt `Close this incomplete trip permanently? [y/N]`. Only exact case-insensitive y/yes confirms; blank/no/EOF cancels (code 4). Other input is treated as no. No network write precedes confirmation.
3. In noninteractive or --json mode, missing confirmation produces code 4 CONFIRMATION_REQUIRED and no POST; show the `--force` invocation in the error/report. With --force, no prompt is required. Force does not bypass revision checks or change the target trip.
4. Submit `POST /trips/{id}/close` with `{expected_revision:R,force:<acknowledged>}`. Use force=false for ordinary no-gap closure unless user explicitly supplied --force. The API independently rechecks under lock.
5. REVISION_CONFLICT: fail code 5, show current revision and ask operator to rerun/review. Do not automatically refresh and reapply confirmation; it applied to R. TRIP_SUPERSEDED fails with successor information. TRIP_INCOMPLETE can still occur if implementation returned new assessment; show it and require a fresh explicit review rather than silently forcing/reposting.
6. On 200 print closed/already closed metadata and returned revision. Validate success identity/state. If receipt is ambiguous, query pinned/current detail: if target is CLOSED/MANUAL, report closure observed, noting this does not identify which client performed it. Otherwise return OUTCOME_UNKNOWN (code 1), instruct operator to inspect/rerun. Never blindly repeat a mutation after a timeout. No POST retry loop in P0.

Preflight reads may use bounded revision attempts before presenting a prompt. Once reviewed/confirmed, no automatic retry across revisions. Signal/EOF during a prompt sends nothing. Ctrl-C during POST can leave outcome uncertain; emit a short warning on stderr and code 130 if possible.

## 7. INS-04 — Watch events and reconcile snapshots

`gpsinspect watch <vehicle-id> [--reconnect-attempts N]`

Default reconnect attempts 10 after a disconnect; 0 disables reconnect. Reconnect delay sequence 250ms,500ms,1s,2s, then capped 5s (injectable for tests). Reset failure count only after a ready+snapshot session is established. SIGINT cancels waits/IO immediately. No total watch timeout; per-request/handshake timeout remains active. Use WS for http and WSS for https, same host and escaped vehicle query. Bound buffers to 256 envelopes and frame size to max-response-bytes. Handle protocol ping/pong without blocking state reconciliation.

Implement AG-08 subscribe-first handshake: require ready as first application frame; register D/vehicle, start buffering events; obtain snapshot R and ensure D matches. Unknown vehicle may subscribe before creation: emit waiting state on 404 and fetch on first event. Install snapshot and discard buffered revisions ≤R. Revisions >R must be consecutive; duplicate covered events can be ignored. A gap, database change, out-of-order stream, socket failure or local buffer overflow invalidates state and triggers reconnect+fresh snapshot. Do not request unsupported replay/since features.

Use events as invalidations, not instructions to run reconstruction. Coalesce already covered events when a newer authoritative snapshot is fetched; no need to render every intermediate state. Record merge predecessor/successor IDs from observed changes. Reads must be revision-pinned if additional geometry is fetched; geometry is unnecessary for P0 watch. If mutation outpaces snapshot collection, remain reconnecting/error visibly rather than asserting synchronized state. Never claim the event log is complete; reconnects lose unreplayed history.

Human output: ready/snapshot revision, semantic event timestamp and revision, changes (including merge lineage), and explicit reconnect/resync notices. NDJSON stdout in --json mode uses one versioned record per line:

```json
{"schema_version":1,"record_type":"snapshot","database_id":"db","vehicle_id":"vehicle-001","revision":42,"data":{}}
{"schema_version":1,"record_type":"event","database_id":"db","vehicle_id":"vehicle-001","revision":43,"data":{"changes":[]}}
{"schema_version":1,"record_type":"resync","reason":"REVISION_GAP"}
```

Other record types: waiting, disconnected, error. An observed event covered by a fetched snapshot may be omitted; document it as a reconciled watch, not a durable audit feed. A terminal error emits an error record and nonzero exit; normal SIGINT exits 130. Partial stream output cannot be rolled back and must include resync markers. Do not emit finite-output error JSON into a watch NDJSON stream.

## 8. INS-05 — Independent validation

`gpsinspect validate <vehicle-id> [--baseline PATH] [--write-baseline PATH]`

Read-only with respect to aggregator. Do not POST telemetry, close trips, open SQLite, or stop simulator. Obtain a coherent full observation at D/R using section 4:

- Vehicle/config/current-trip snapshot and manual barriers.
- All raw fixes including quarantined records and current assignment IDs.
- All trips with include_merged=true; details for lineage/closure metadata.
- Every trip's ordered geometry, including frozen merged snapshots.
- Completeness for all trips; compare its internal cadence results independently.

This is deliberately thorough for a bounded weekend dataset. Resource limits fail explicitly; do not silently sample. Represent membership by `(vehicle_id,timestamp_ms)` and compare canonical measurement values too. In Go use exact schema-normalized coordinate/measurement equality; reject NaN/nonfinite values. Use checked integer arithmetic for timestamps/durations. Do not rely on API-provided counts or current_trip_id alone as proof of geometry correctness.

Build a small independent reference model: derive barriers from CLOSED/MANUAL trip geometry, partition eligible raw fixes around their inclusive intervals, sort fixes by event time, and split when adjacent gap is greater than configured gap. Compare sets of timestamp sets, not generated IDs or merge history chosen by arrival order. Do not copy the aggregator's implementation into this package. Current expected components exclude manual members and quarantined points; manually closed snapshots are checked separately.

Every check produces `{check_id,status,message,evidence}` with status PASS, FAIL, SKIP or WARN. Limit evidence examples to 20 entries per check, report total violation count, and make truncation explicit. Summary includes D/R, counts, PASS/FAIL/SKIP/WARN totals, and scope/limitations. Any FAIL gives exit 3; inability to collect/interpret data gives exit 1/5 rather than an apparently successful check report.

Required checks:

| ID | Assertion |
|---|---|
| V01 RAW_IDENTITY | Raw fixes strictly increase in timestamp, belong to requested vehicle, are unique and satisfy AG-02 measurement bounds. Latest position matches max raw timestamp and all measurements. |
| V02 CURRENT_MEMBERSHIP | Each assigned raw fix appears exactly once among non-MERGED trips (ACTIVE/INACTIVE/CLOSED-MANUAL). Current assignment ID agrees. Quarantined fixes have no such membership; every geometry member exists in raw history with matching values. MERGED audit copies do not count as current assignments. |
| V03 TRIP_METADATA | All trip geometries ordered/unique/nonempty; count, first/last timestamps and coordinate extrema match summary. Status/reason/successor combinations and vehicle IDs are valid. created_revision ≤ updated_revision ≤ R. |
| V04 TEMPORAL_COMPONENTS | ACTIVE/INACTIVE memberships equal independently computed maximal components under gap≤configured threshold and manual barriers. No split/extra component or crossing barrier. |
| V05 MANUAL_BARRIERS | Closed manual trip intervals equal frozen geometry endpoints, are disjoint, have no successor, and match snapshot barrier records. All raw fixes inside an interval either belong to that manual snapshot or have MANUAL_CLOSED_INTERVAL quarantine reason; no quarantine outside every manual interval. Closed manual geometry remains a connected component at configured gap. |
| V06 ACTIVITY | At most one ACTIVE; active pointer/status agree. Exactly the mutable trip containing maximum raw timestamp is active, otherwise NULL. An older mutable trip is not activated if latest fix belongs to manual snapshot/quarantine. |
| V07 LINEAGE | MERGED trips have exactly one immediate successor, same vehicle, and matching predecessor lists; graph acyclic; resolved terminal successor correct. Every predecessor snapshot is a subset of its immediate successor geometry with identical measurements. Terminal successor is not CLOSED/MERGED. |
| V08 COMPLETENESS | Independently compute `max(0,ceil(delta/I)-1)` per adjacent fix pair; aggregate counts/intervals match completeness DTO. complete/assessment agrees; boundary status UNKNOWN. Do not enumerate every expected missing point. |
| V09 OBSERVED_IMMUTABILITY | Compare earlier closed snapshots from baseline, if supplied. See below. |
| V10 OBSERVED_MONOTONICITY | For same database/vehicle baseline, revision never decreases and latest event timestamp never regresses. Equal revision requires identical normalized captured state. |

Distinguish invariant failure from expected model behavior: internal sampling gaps ≤trip gap are allowed and may warn about closure completeness, not fail V04. INACTIVE historical trips are mutable. MANUAL closure permits internal missing fixes; no requirement of perfect cadence. MERGED predecessor geometry overlaps successor geometry by design. Source boundaries remain uncertain even when V08 passes. Snapshot validation cannot prove historical packet idempotency, atomic crash behavior or the absence of past transient errors; those require aggregator integration tests.

### Baseline file and temporal checks

Without a baseline, V09 and V10 are SKIP with a clear message. Overall report says `PASS_WITH_LIMITATIONS` if no FAIL and temporal checks skipped; do not say immutable history was proven.

`--write-baseline PATH` writes a normalized coherent observation only after collection completes and checks have no FAIL. Create a new file exclusively (0600 where supported), no silent overwrite; write to same-directory temporary file and install atomically using a no-replace strategy. On IO failure return code 1. Parent must exist. Do not create a baseline on interrupted/partial/error results. `--baseline` may be combined with --write-baseline to compare one observation and save a new distinct one. Never alter the input baseline.

Baseline schema_version=1 includes database_id, vehicle_id, revision, effective gap/cadence, latest fix, and normalized trip metadata + full ordered fix values/lineage sufficient for comparison. It is diagnostic data, not executable configuration. Read with bounds (default 64 MiB baseline file ceiling); reject incompatible versions, missing required fields, malformed values or a different vehicle. If D differs, fail code 1 BASELINE_DATABASE_MISMATCH; do not compare coincidentally reused trip IDs or rewrite baseline automatically. Changed segmentation config for same D is a FAIL because AG-01 forbids silent config changes.

For every trip already CLOSED in baseline, compare immutable metadata (identity/vehicle, geometry, bounds/count, status/reason, successor and closure audit fields, created/updated revisions) with current observation; disappearance/change is FAIL. Resolved terminal successor may legitimately change as later successors merge; exclude that derived field from frozen hash and validate it against current graph instead. Immediate predecessor edges are fixed once a trip is created and must remain stable. Trips previously ACTIVE/INACTIVE may grow/merge/close; do not freeze them. A new closed trip observed now establishes only the next observation baseline, not retrospective evidence of immutability. V10 compares normalized captured domain state, excluding read/server wall timestamps or other volatile diagnostics.

## 9. INS-06 — Tests and acceptance

Use httptest fixtures and injected WebSocket endpoints. Tests cover API bodies and exit codes, not implementation-private functions alone. Capture stdout/stderr independently; no golden test should require current wall time or a real terminal.

| Test | Expected outcome | Tasks |
|---|---|---|
| I01 | Help/version without network, flag/env precedence, invalid URL/flags, escaped IDs and canceled contexts | INS-01 |
| I02 | Human/JSON output, UTC timestamps, nulls, empty lists, errors and exit code mapping; no prompts mixed into JSON | INS-01/02 |
| I03 | Multi-page lists, filters, stable sort, cursor-cycle detection, response/resource bounds and malformed required fields | INS-02 |
| I04 | At-revision on every read/page; conflict discards entire attempt, retries ≤configured attempts; database change never mixes observations | INS-02/05 |
| I05 | trip retains requested merged identity and displays lineage; gaps labels boundary uncertainty and display truncation accurately | INS-02 |
| I06 | Complete-trip close sends expected_revision and force=false; incomplete noninteractive close sends no POST; interactive no/EOF/interrupt sends no POST | INS-03 |
| I07 | Incomplete yes or --force sends force=true only for reviewed revision; stale revision never auto-reconfirmed; superseded target never redirected | INS-03 |
| I08 | Lost close response reconciles manual closure via GET or reports unknown outcome; no blind mutation retry; already closed preflight succeeds | INS-03 |
| I09 | Ready-first buffer/snapshot race; covered events discarded; next revisions consecutive; gap/overflow/database reset triggers resync | INS-04 |
| I10 | Unknown vehicle waiting, reconnect backoff/exhaustion, ping/pong, slow reads, frame limits and SIGINT; NDJSON remains parseable through errors | INS-04 |
| I11 | Correct chronological/shuffled/merged/manual/quarantine fixtures pass V01..V08; intentional one-at-a-time corruptions fail the right checks | INS-05 |
| I12 | 599999/600000/600001 ms boundaries, singleton, late fixes, internal completeness gaps, no activity timeout, latest quarantined fix rules | INS-05 |
| I13 | Audit overlap valid; lineage cycle/missing successor/wrong subset fails; duplicate current membership fails | INS-05 |
| I14 | No baseline yields explicit temporal SKIP; frozen geometry/metadata change fails, mutable growth and terminal-resolution changes allowed | INS-05 |
| I15 | Baseline database/vehicle/schema mismatch, size limit, refused overwrite, interrupted writes; saved observations are coherent and private | INS-05 |
| I16 | End-to-end real aggregator/simulator: list/inspect/watch outage recovery, validate merged state, warn/force close, validate quarantine and saved immutability baseline | INS-06; E2E-01/02/04 |

For I11 build reference fixtures by hand, including deliberately inconsistent API snapshots. Do not generate expected values using aggregator helpers; otherwise validation could reproduce the same defect. Seeded random permutations may supplement fixtures. Validation can establish final component equivalence, but cannot demand identical generated trip IDs or lineage across different ingestion histories.

Run `go test ./...`, `go test -race ./...`, and `go build ./cmd/gpsinspect`. Run I16 against implemented public endpoints. If actual DTO field names differ from spec, resolve the shared published contract explicitly; do not hide drift with ad hoc alternate schemas or fake successful values.

## 10. Deliverables and completion map

- **INS-01:** Cobra command tree, reusable HTTP client, global flags, typed errors/output/exit contract; I01/02.
- **INS-02:** vehicles/vehicle/trips/trip/gaps, coherent pagination, completeness/lineage presentation; I03..05.
- **INS-03:** close workflow, force confirmation and revision safety; I06..08.
- **INS-04:** watch ready/snapshot/reconnect protocol and human/NDJSON output; I09/10.
- **INS-05:** Independent V01..V10 validator and optional baseline comparison/export; I11..15.
- **INS-06 / G5:** End-to-end API-only demonstration, build/race tests, commands and sample outputs in README; I16.
- **INS-07 / P1:** Independent Python randomized harness later; not required for P0 inspector.

Include source, pinned Go dependencies, fixtures, tests, help text, and README examples. Record verification results and any API deviations. Keep existing checklist task IDs stable. Example usage after implementation:

```bash
gpsinspect vehicles
gpsinspect vehicle vehicle-001
gpsinspect trips vehicle-001 --include-merged
gpsinspect trip trip-003
gpsinspect gaps trip-003
gpsinspect watch vehicle-001 --json
gpsinspect validate vehicle-001 --write-baseline before.json
gpsinspect validate vehicle-001 --baseline before.json
```

Closure examples are mutations and must target intentionally selected trips:

```bash
# Ordinary close; missing internal fixes require confirmation.
gpsinspect close trip-003
# Explicitly acknowledge known missing internal fixes.
gpsinspect close trip-003 --force
```

No direct database access, simulator control subcommands, TUI, dashboard implementation or automatic repair is needed to complete this specification.
