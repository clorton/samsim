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
- [x] **AG-07** [Query APIs](AGGREGATOR_SPEC.md#ag-07): vehicles, coherent snapshots, trips, geometry, raw fixes with assignment reasons, completeness, pagination, health, consistent errors.
- [x] **AG-08** [Events and revisions](AGGREGATOR_SPEC.md#ag-08): persistent vehicle revision, one event envelope per changed vehicle transaction, post-commit ordered WebSocket delivery, subscribe-ready handshake, gap/reconnect resnapshot.
- [x] **AG-09** [Simulator control proxy](AGGREGATOR_SPEC.md#ag-09): static simulator registry, fixed allowed routes, request deadlines and useful upstream errors. Configure a stub before simulator exists.
- [x] **AG-10** [Verification and operations](AGGREGATOR_SPEC.md#ag-10): table/property/integration/race tests, restart and concurrency tests, reproducible fixtures, CLI/config documentation. **G3:** standalone aggregator passes its contract using fixture requests; `go test ./...` and `go test -race ./...` pass. Includes precommit crash-injection tests (ingestion and closure, T14) and a throughput smoke test reporting actual latency/throughput (skipped under `-short`); see README "Development and validation".

Dependencies: AG-01 → AG-02/03 → AG-04 → AG-05 → AG-06; AG-07/08 depend on those state semantics; AG-09 can use a stub. AG-10 accompanies each task, then gates completion.

## 2. GPS simulator — Go (P0)

- [x] **SIM-01** Use the existing address-to-address route generator’s **CSV output** as simulator input; do not build another route generator. Implement the supplied header mapping: `ts` (elapsed seconds), `lat`, `lon`, `speed_mps`, `heading_deg`; retain `dist_m` and `speed_source` only as optional simulator diagnostics. Convert `ts` to millisecond offsets from explicit run start per the spec; validate rows before playback. Configure simulator/vehicle IDs, aggregator URL, route, 10–1000× clock multiplier, default 1000 ms fix interval and 10 fixes/packet. Use an injected clock in tests.
- [x] **SIM-02** Playback generates event timestamps independently of transmission and wall-clock arrival. For the supplied CSV, compute `timestamp_ms = run_start_ms + round(ts * 1000)`; `ts=0.0` is valid elapsed time, not Unix epoch zero. Preserve source timing; the configured fix interval is the expected cadence, not a replacement for `ts`. A separate explicitly selected timestamp-free adapter may generate times by row order. Keep fix identity stable within a run. Reject invalid coordinates, malformed rows, and duplicate/nonincreasing source times with row-specific errors; do not silently skip or reorder route points.
- [x] **SIM-03** Generate packet IDs `YYYYMMDD-SEQUENCE` using UTC creation day and a persisted per-day counter scoped to simulator. Never reuse an ID after reset/restart. Encode JSON or Protobuf using AG-02.
- [x] **SIM-04** Transmission pause continues fix generation into backlog (now accumulating into the open report window); stop halts generation and transmission. Resume prefers fresh/live data first via a non-blocking `fresh_first_pending` preference, interleaving backlog with a default 3:1 weighted schedule. When both queues remain nonempty, guarantee at least one backlog packet per four available transmission slots. Use spare slots for either nonempty queue; do not drop fixes. **Reworked per [specs/SIMULATOR_CHANGE_REQUEST.md](specs/SIMULATOR_CHANGE_REQUEST.md):** the original hard recovery gate (`recovery_waiting_for_live`, which blocked all sends including eligible backlog) is removed and replaced with a one-shot, non-blocking freshness preference that never withholds an eligible backlog send.
- [x] **SIM-05** Rate-limit packets per **wall-clock second**, scaled from a **simulated-time** capacity ceiling (`max_packets_per_simulated_second`, default 0.2) by `clock_multiplier`; report congestion, queue counts, and target/observed throughput separately. **Reworked per SIMULATOR_CHANGE_REQUEST.md:** live report *creation* now runs on an independent simulated-time schedule (`normal_report_interval_simulated_ms`, default 10000ms, half-open windows, no empty reports), fully decoupled from this capacity ceiling, so raising capacity during backlog recovery never changes normal live-reporting cadence. The old `partial_flush_wall_ms` wall-clock timer is removed; `packet_size` remains only a safety bound that can split an oversized closed window into multiple packets.
- [x] **SIM-06** Expose `GET /status`, `PUT /config`, and POST `/start`, `/stop`, `/pause-transmission`, `/resume-transmission`, `/reset` for AG-09. Status includes generated event-time watermark, report-window/queue counts, target/observed rates, playback and transmission state. Vehicle/route changes require stopped state and empty queues; reset with pending data requires an explicit discard flag. `normal_report_interval_simulated_ms` changes require stopped (or not-yet-started) playback and seal the current partial window once, anchoring the new schedule at current virtual time, without rewriting queued packets.
- [x] **SIM-07** Test CSV parsing/mapping (including quoted fields, missing required values and row-specific validation errors), timestamp rebasing or generation, acceleration, packet size/IDs, no transmission during outage, no loss, queue fairness, congestion, windowed reporting (boundary/EOF/runtime-interval-change sealing), multiplier-scaled rate limiting, and both encodings against aggregator. **G4:** a >600-second simulated outage yields two trips followed by a historical merge, with monotonic latest event time. Verified end-to-end against a real running aggregator (`TestS18_OutageProducesTwoTripsThenMerge`, `TestS17_*`); `TestS18` re-verified after the SIMULATOR_CHANGE_REQUEST.md rework with multiplier-scaled capacity and windowed reporting.
- [ ] **SIM-08 — P2** Add automatic retries with backoff and stable packet identity/body. Initial version retains failed packets and reports failure; explicit resend can exercise AG-04 without implementing automated retransmission.

## 3. Inspector — Go CLI `gpsinspect` (P0)

- [x] **INS-01** Add `cmd/gpsinspect` with Cobra, base URL/timeout flags, human output plus `--json`, nonzero failure exit codes. Use public HTTP APIs exclusively.
- [x] **INS-02** Implement `vehicles`, `vehicle <id>`, `trips <vehicle>`, `trip <id>`, `gaps <trip>`; show status, closure reason, boundaries, counts, bounds, lineage, unassigned telemetry, and completeness uncertainty (AG-07).
- [x] **INS-03** Implement `close <trip>`: inspect completeness, send expected revision, display warning, require explicit `--force` or interactive confirmation for internal gaps. Noninteractive mode never silently confirms; stale revision requires reread (AG-06).
- [x] **INS-04** Implement `watch <vehicle>` using ready/subscription, revision checks, reconnect/resnapshot (AG-08).
- [x] **INS-05** Implement `validate <vehicle>` using raw fixes plus snapshot/geometry reads pinned to the same revision. Independently check unique current membership, temporal components within manual barriers, frozen closed snapshots, lineage, latest position, and active pointer. Do not reuse aggregator reconstruction code; closed snapshot immutability needs persisted prior observations or comparison across a test sequence.
- [x] **INS-06** Test commands against fixture server and real aggregator; **G5:** inspect and validate outage/merge/forced-close scenarios without opening SQLite. Verified end-to-end against a real running aggregator (`TestI16_ListInspectOutageRecoveryValidateMergedForceClose`), including outage/merge, validate on merged state, forced close of an incomplete trip, and a saved/reread immutability baseline.
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
