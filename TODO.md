# GPS telemetry weekend implementation checklist

Companion contract: [AGGREGATOR_SPEC.md](AGGREGATOR_SPEC.md). Each `AGG-*` task links to its normative section; use these IDs in commits and tests. Check a task only when its acceptance conditions pass. P0 is the weekend demonstration; P1 is follow-up polish; P2 is deferred. The spec explicitly resolves details that were left open in the design conversation.

## Schedule and gates

| Block | Budget | Gate |
|---|---:|---|
| Saturday morning: aggregator foundation | 3 h | G1: both encodings durably ingest |
| Saturday midday: trip engine | 4 h | G2: late-data merges and immutable closure pass |
| Saturday evening: APIs/events | 2 h | G3: aggregator usable independently |
| Sunday morning: simulator | 3.5 h | G4: outage/recovery reproducible |
| Sunday midday: inspector | 2 h | G5: backend inspectable through public APIs |
| Sunday afternoon: dashboard | 2.5 h | G6: minimal live map |
| Sunday final integration | 1.5 h | G7: scripted demonstration and restart pass |

This is an ambitious roughly 18.5-hour budget. Reduce dashboard polish first; preserve correctness gates. Work in the component order below. Do not introduce a broker, cloud deployment, gRPC, GraphQL, authentication, map matching, or movement-based segmentation this weekend.

## 1. Aggregator — Go, SQLite (P0)

- [x] **AG-01** [Architecture and configuration](AGGREGATOR_SPEC.md#ag-01): initialize one Go module, `cmd/aggregator`, shared telemetry schema, package boundaries, config validation, structured logs, injectable clock/ID generator. Document the local run command and defaults.
- [x] **AG-02** [Wire contract](AGGREGATOR_SPEC.md#ag-02): generate Go Protobuf types; implement strict protojson and binary decoding on the same endpoint. Test equivalent semantics and timestamp representation.
- [x] **AG-03** [Persistence](AGGREGATOR_SPEC.md#ag-03): versioned migration, WAL, foreign keys, busy timeout, constraints/indexes, transaction helper; restart preserves all state and revisions.
- [x] **AG-04** [Ingestion and idempotency](AGGREGATOR_SPEC.md#ag-04): validate, normalize, fingerprint, deduplicate, synchronously reconstruct, atomically commit, return summary. Cover duplicate packet/fix conflicts and rollback. **G1:** JSON and Protobuf submissions produce durable equivalent state.
- [x] **AG-05** [Trip reconstruction](AGGREGATOR_SPEC.md#ag-05): event-time components, inclusive 600-second default, active/inactive lifecycle, late extension, new-ID merges, frozen predecessors, lineage; canonical position uses greatest timestamp.
- [x] **AG-06** [Completeness and manual closure](AGGREGATOR_SPEC.md#ag-06): internal-gap analysis, revision preconditions, API-enforced warning and explicit override, immutable manual intervals, quarantined fixes; no merge across closure. **G2:** boundary, shuffle, merge, and closure tests pass.
- [ ] **AG-07** [Query APIs](AGGREGATOR_SPEC.md#ag-07): vehicles, coherent snapshots, trips, geometry, raw fixes with assignment reasons, completeness, pagination, health, consistent errors.
- [ ] **AG-08** [Events and revisions](AGGREGATOR_SPEC.md#ag-08): persistent vehicle revision, one event envelope per changed vehicle transaction, post-commit ordered WebSocket delivery, subscribe-ready handshake, gap/reconnect resnapshot.
- [ ] **AG-09** [Simulator control proxy](AGGREGATOR_SPEC.md#ag-09): static simulator registry, fixed allowed routes, request deadlines and useful upstream errors. Configure a stub before simulator exists.
- [ ] **AG-10** [Verification and operations](AGGREGATOR_SPEC.md#ag-10): table/property/integration/race tests, restart and concurrency tests, reproducible fixtures, CLI/config documentation. **G3:** standalone aggregator passes its contract using fixture requests; `go test ./...` and `go test -race ./...` pass.

Dependencies: AG-01 → AG-02/03 → AG-04 → AG-05 → AG-06; AG-07/08 depend on those state semantics; AG-09 can use a stub. AG-10 accompanies each task, then gates completion.

## 2. GPS simulator — Go (P0)

- [ ] **SIM-01** Use the existing address-to-address route generator’s **CSV output** as simulator input; do not build another route generator. Implement the supplied header mapping: `ts` (elapsed seconds), `lat`, `lon`, `speed_mps`, `heading_deg`; retain `dist_m` and `speed_source` only as optional simulator diagnostics. Convert `ts` to millisecond offsets from explicit run start per the spec; validate rows before playback. Configure simulator/vehicle IDs, aggregator URL, route, 10–1000× clock multiplier, default 1000 ms fix interval and 10 fixes/packet. Use an injected clock in tests.
- [ ] **SIM-02** Playback generates event timestamps independently of transmission and wall-clock arrival. For the supplied CSV, compute `timestamp_ms = run_start_ms + round(ts * 1000)`; `ts=0.0` is valid elapsed time, not Unix epoch zero. Preserve source timing; the configured fix interval is the expected cadence, not a replacement for `ts`. A separate explicitly selected timestamp-free adapter may generate times by row order. Keep fix identity stable within a run. Reject invalid coordinates, malformed rows, and duplicate/nonincreasing source times with row-specific errors; do not silently skip or reorder route points.
- [ ] **SIM-03** Generate packet IDs `YYYYMMDD-SEQUENCE` using UTC creation day and a persisted per-day counter scoped to simulator. Never reuse an ID after reset/restart. Encode JSON or Protobuf using AG-02.
- [ ] **SIM-04** Transmission pause continues fix generation into backlog; stop halts generation and transmission. Resume sends fresh/live data first, interleaving backlog with a default 3:1 weighted schedule. When both queues remain nonempty, guarantee at least one backlog packet per four available transmission slots. Use spare slots for either nonempty queue; do not drop fixes.
- [ ] **SIM-05** Rate-limit packets per **wall-clock second**, default 50/s; report congestion and queue sizes. At 1000× and batch 10, live demand is 100 packets/s, so 50/s cannot keep up. Demonstrate recovery with sufficient capacity, e.g. 200/s. Backlog drain requires spare capacity.
- [ ] **SIM-06** Expose `GET /status`, `PUT /config`, and POST `/start`, `/stop`, `/pause-transmission`, `/resume-transmission`, `/reset` for AG-09. Status includes generated event-time watermark, queue counts, playback and transmission state. Vehicle/route changes require stopped state and empty queues; reset with pending data requires an explicit discard flag.
- [ ] **SIM-07** Test CSV parsing/mapping (including quoted fields, missing required values and row-specific validation errors), timestamp rebasing or generation, acceleration, packet size/IDs, no transmission during outage, no loss, queue fairness, congestion, and both encodings against aggregator. **G4:** a >600-second simulated outage yields two trips followed by a historical merge, with monotonic latest event time.
- [ ] **SIM-08 — P2** Add automatic retries with backoff and stable packet identity/body. Initial version retains failed packets and reports failure; explicit resend can exercise AG-04 without implementing automated retransmission.

## 3. Inspector — Go CLI `gpsinspect` (P0)

- [ ] **INS-01** Add `cmd/gpsinspect` with Cobra, base URL/timeout flags, human output plus `--json`, nonzero failure exit codes. Use public HTTP APIs exclusively.
- [ ] **INS-02** Implement `vehicles`, `vehicle <id>`, `trips <vehicle>`, `trip <id>`, `gaps <trip>`; show status, closure reason, boundaries, counts, bounds, lineage, unassigned telemetry, and completeness uncertainty (AG-07).
- [ ] **INS-03** Implement `close <trip>`: inspect completeness, send expected revision, display warning, require explicit `--force` or interactive confirmation for internal gaps. Noninteractive mode never silently confirms; stale revision requires reread (AG-06).
- [ ] **INS-04** Implement `watch <vehicle>` using ready/subscription, revision checks, reconnect/resnapshot (AG-08).
- [ ] **INS-05** Implement `validate <vehicle>` using raw fixes plus snapshot/geometry reads pinned to the same revision. Independently check unique current membership, temporal components within manual barriers, frozen closed snapshots, lineage, latest position, and active pointer. Do not reuse aggregator reconstruction code; closed snapshot immutability needs persisted prior observations or comparison across a test sequence.
- [ ] **INS-06** Test commands against fixture server and real aggregator; **G5:** inspect and validate outage/merge/forced-close scenarios without opening SQLite.
- [ ] **INS-07 — P1** Add independent Python randomized model/fixture generator after Go inspector works.

## 4. Dashboard — minimal foundation (P0; UX details deferred)

- [ ] **DASH-01** Scaffold React/TypeScript/Vite and MapLibre; vehicle selection, trip fetching/selection, pan/zoom map, latest position and trip geometry.
- [ ] **DASH-02** Keep available trips, visible trip IDs, selected trip ID, and follow-live state separate. Decide the list/visibility interaction later; do not assume all listed trips must be rendered.
- [ ] **DASH-03** Use AG-08 subscribe-first snapshots and revision resync. On merges, refresh current trips and resolve selected predecessor through lineage.
- [ ] **DASH-04** Add basic simulator controls through AG-09 and manual closure through AG-06. Show missing internal fixes and boundary uncertainty; allow explicit override only after warning. Handle stale revisions by refreshing.
- [ ] **DASH-05** **G6:** vehicle selection, geometry, live updates, outage/resume, merge reconciliation, and closure warning work. Defer detailed layouts, trip visibility UX, and elaborate controls.
- [ ] **DASH-06 — P1** Merge highlight, diagnostic event log, detailed congestion visualization.

## Final integration (P0)

- [ ] **E2E-01** Script normal playback → outage longer than 600 simulated seconds → resume live-first → two trips → backlog bridge → new merged ID; verify MERGED predecessors and lineage (AG-05/08, SIM-07, INS-05, DASH-03).
- [ ] **E2E-02** Observe an internal gap → close rejected with warning → explicitly force close → send delayed internal fix and future fix within 600 seconds. Verify quarantine, frozen closed geometry, new future trip, and no crossing merge (AG-06).
- [ ] **E2E-03** Repeat identical packet across encodings, retry after lost response, submit conflicting packet/fix, and restart aggregator. Confirm no double effects or state loss (AG-03/04/10).
- [ ] **E2E-04** Disconnect WebSocket during ingestion; reconnect and recover via snapshot. Verify latest position never regresses during backlog delivery (AG-05/08).
- [ ] **E2E-05** Write README architecture/run instructions and a short demo script with deterministic route/config fixtures. Record known limitations and successful checks. **G7:** demo runs from a clean checkout/database and survives aggregator restart.
