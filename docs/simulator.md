# Simulator endpoint reference

A practical reference for exercising a running simulator from a browser, `curl`, or `wget` — no client code required. For the normative behavior behind these endpoints, see [specs/SIMULATOR_SPEC.md](specs/SIMULATOR_SPEC.md) (SIM-06) and [specs/SIMULATOR_CHANGE_REQUEST.md](specs/SIMULATOR_CHANGE_REQUEST.md) (the windowed-reporting/capacity rework that supersedes some details in the base spec — see the note at the end of this document).

Start a simulator first (there is no default config; `-config` is required):

```bash
go build -o bin/simulator ./cmd/simulator
./bin/simulator -config simulator.example.json
```

All examples below assume `BASE=http://127.0.0.1:8090` (the `listen` address in `simulator.example.json`). Every payload shown here was captured from a real running simulator while writing this document.

## Quick start: GET /status in a browser

```
http://127.0.0.1:8090/status
```

Browsers pretty-print JSON by default in most cases, or install a JSON-viewer extension. The other five endpoints are all `POST`/`PUT` and need `curl`, `wget --post-data`, or a REST client.

## Response and error shape

Every error response (any non-2xx) uses the same envelope, independent of the aggregator's own error vocabulary:

```json
{"error":{"code":"INVALID_CONFIG","message":"simulator: this field can only change while playback is STOPPED"}}
```

Some codes include extra structured fields in `details` (shown per-endpoint below).

| HTTP status | Codes you may see |
|---|---|
| 400 | `INVALID_REQUEST`, `INVALID_CONFIG`, `INVALID_ROUTE` |
| 404 | `NOT_FOUND`, `UNKNOWN_ROUTE` |
| 405 | `METHOD_NOT_ALLOWED` (also sets an `Allow` response header) |
| 409 | `RESET_REQUIRED`, `CONTROL_REVISION_CONFLICT`, `RETRY_CONFIRMATION_REQUIRED`, `STOP_REQUIRED`, `SEND_IN_FLIGHT`, `PENDING_DATA` |
| 413 | `BODY_TOO_LARGE`, `ROUTE_TOO_LARGE` |
| 415 | `UNSUPPORTED_MEDIA_TYPE` |
| 503 | `IDENTITY_STORE_UNAVAILABLE`, `CONTROL_PREPARATION_TIMEOUT` |

All request/response JSON uses `snake_case` field names and plain numeric epoch milliseconds (unlike the aggregator's telemetry-ingestion endpoint, the simulator's own control API has no lowerCamelCase/protojson-style body anywhere).

---

## `GET /status`

The complete, coherent engine snapshot. No parameters.

```bash
curl -s $BASE/status
```

Captured from a real simulator immediately after start (`READY`, nothing generated yet):

```json
{
  "simulator_id": "sim-doc", "vehicle_id": "vehicle-doc", "run_id": null,
  "control_revision": 0, "playback_state": "READY", "transmission_state": "ENABLED",
  "coverage_paused": false,
  "config": {
    "route_id": "tenn", "run_start_ms": "now", "clock_multiplier": 10,
    "expected_fix_interval_ms": 1000, "normal_report_interval_simulated_ms": 10000,
    "packet_size": 10, "encoding": "json", "max_packets_per_simulated_second": 2,
    "live_weight": 3, "backlog_weight": 1, "request_timeout_ms": 2000
  },
  "available_route_ids": ["tenn"],
  "resolved_run_start_ms": null, "route_rows": 11614,
  "generated_fixes": 0, "acknowledged_fixes": 0, "discarded_fixes": 0, "quarantined_fixes": 0,
  "live_queue_count": 0, "backlog_queue_count": 0,
  "live_window_pending_count": 0, "backlog_window_pending_count": 0,
  "pending_fix_count": 0, "total_unsent_fixes": 0,
  "pending_packet_id": null, "in_flight": false, "pending_failed": false,
  "fresh_first_pending": false,
  "next_report_window_close_virtual_ms": null,
  "expected_fixes_per_report": 10,
  "actual_report_interval_ms": 1000, "actual_max_packets_per_second": 20,
  "latest_generated_timestamp_ms": null, "latest_acknowledged_timestamp_ms": null,
  "virtual_elapsed_ms": 0,
  "oldest_unsent_timestamp_ms": null, "oldest_live_age_wall_ms": null,
  "packets_attempted": 0, "packets_acknowledged": 0,
  "generated_fixes_per_second": 0, "acknowledged_fixes_per_second": 0,
  "congested": false, "congestion_reason": null,
  "target_packets_per_second": 20, "observed_packets_per_second": 0,
  "last_error": null
}
```

Field notes, grouped by theme:

**Playback/transmission lifecycle**
- `playback_state`: `READY` → `RUNNING` → (`DRAINING` once the route is exhausted) → `COMPLETED`; or `STOPPED` after `/stop`.
- `transmission_state`: `ENABLED`, `PAUSED` (coverage outage via `/pause-transmission`), or `BLOCKED_ERROR` (a failed send is awaiting an explicit retry).
- `coverage_paused`: true while an outage is simulated; independent of a hard stop.
- `run_id`, `resolved_run_start_ms`: set once `/start` actually begins a run; `resolved_run_start_ms` is the literal epoch-ms value `"now"` resolved to, or the configured fixed value.

**Reporting schedule vs. transmission capacity** (the two independent schedules from `specs/SIMULATOR_CHANGE_REQUEST.md`):
- `config.normal_report_interval_simulated_ms` / `expected_fixes_per_report` / `actual_report_interval_ms`: when live reports are **created**. A report closes once every `normal_report_interval_simulated_ms` of simulated time, containing whatever fixes accumulated in that window (no empty reports). `actual_report_interval_ms = normal_report_interval_simulated_ms / clock_multiplier` is the real wall-clock cadence you'll actually observe.
- `config.max_packets_per_simulated_second` / `actual_max_packets_per_second` / `target_packets_per_second` / `observed_packets_per_second`: how fast queued packets (live **and** backlog combined) may be **sent**. `actual_max_packets_per_second = max_packets_per_simulated_second * clock_multiplier` is the real wall-clock ceiling; `observed_packets_per_second` is what's actually being achieved (may be lower, e.g. due to HTTP latency or a single in-flight request).
- `next_report_window_close_virtual_ms`: the virtual-elapsed-ms boundary at which the currently open window will close and become sendable. `null` until a run has started.
- `live_window_pending_count` / `backlog_window_pending_count`: fixes generated but still accumulating in the *open* (not-yet-closed) window — not yet eligible to send. Contrast with `live_queue_count` / `backlog_queue_count`, which are closed windows waiting for capacity.
- `fresh_first_pending`: a one-shot, non-blocking preference armed by `/resume-transmission` when backlog exists. It prefers sending a fresh live report first (to make the outage boundary visible) but — unlike the simulator's original "recovery gate" design — it never blocks an eligible backlog send if live isn't ready yet.

**Counters/throughput**
- `generated_fixes` / `acknowledged_fixes` / `discarded_fixes` / `quarantined_fixes`: lifetime counters; `generated == acknowledged + discarded + total_unsent_fixes` always holds.
- `total_unsent_fixes`: everything not yet durably acknowledged — sum of both queues, both open-window accumulators, and any pending in-flight/retry packet.
- `congested` / `congestion_reason`: true when the oldest live record has waited over 1000ms, or generation has sustainably outpaced acknowledgment — a capacity shortfall signal, independent of whether a coverage outage is in progress.
- `last_error`: the most recent send failure (network/timeout/non-200/malformed-receipt/identity-allocation), cleared on the next success.

---

## `PUT /config`

A partial, validated, atomically-applied config update. Every field is optional; send only what you want to change, plus `expected_control_revision` for optimistic-concurrency safety. Fields fall into four precondition categories:

| Category | Fields | Precondition |
|---|---|---|
| Hot | `clock_multiplier`, `max_packets_per_simulated_second`, `live_weight`, `backlog_weight` | None — applies immediately in any state |
| No pending | `packet_size`, `encoding`, `request_timeout_ms` | No in-flight/pending packet |
| Ready+empty | `vehicle_id`, `route_id`, `run_start_ms`, `aggregator_url`, `expected_fix_interval_ms` | Playback `READY` and zero unsent fixes |
| Stopped | `normal_report_interval_simulated_ms` | Playback `STOPPED` or `READY` (never started) — changing it seals the current partial window once and anchors the new schedule at the current virtual time |

Hot-field example — raise transmission capacity while running (e.g. to drain a backlog faster without touching the reporting cadence):

```bash
curl -s -X PUT $BASE/config -H 'Content-Type: application/json' \
  --data '{"max_packets_per_simulated_second": 5}'
```

```json
{"...": "...", "config": {"...": "...", "max_packets_per_simulated_second": 5}, "actual_max_packets_per_second": 50, "control_revision": 2}
```

Rejected while running — `normal_report_interval_simulated_ms` requires stopped playback:

```bash
curl -s -X PUT $BASE/config -H 'Content-Type: application/json' \
  --data '{"normal_report_interval_simulated_ms": 5000}'
```

```json
{"error":{"code":"INVALID_CONFIG","message":"simulator: this field can only change while playback is STOPPED"}}
```

`expected_control_revision` precondition:

```bash
curl -s -X PUT $BASE/config -H 'Content-Type: application/json' \
  --data '{"live_weight": 5, "expected_control_revision": 0}'
# 409 CONTROL_REVISION_CONFLICT if the current control_revision isn't 0
```

---

## `POST /start`

Begins a fresh run (from `READY`) or resumes after `/stop` (from `STOPPED`); idempotent while already `RUNNING`/`DRAINING`.

```bash
curl -s -X POST $BASE/start
```

```json
{"...": "...", "playback_state": "RUNNING", "run_id": "43d6b6e2-...", "resolved_run_start_ms": 1792972054244, "...": "..."}
```

Starting a `COMPLETED` run without resetting first:

```json
{"error":{"code":"RESET_REQUIRED","message":"simulator: reset required before starting again"}}
```

---

## `POST /stop`

Freezes the virtual clock and halts new send attempts; retains all queues/window accumulators/pending packets exactly as-is. No body.

```bash
curl -s -X POST $BASE/stop
```

---

## `POST /pause-transmission`

Simulates a coverage outage: fix generation continues (into backlog instead of live), but nothing is transmitted. No body.

```bash
curl -s -X POST $BASE/pause-transmission
```

```json
{"...": "...", "transmission_state": "PAUSED", "coverage_paused": true, "...": "..."}
```

---

## `POST /resume-transmission`

Clears a coverage outage and, if backlog accumulated while a future route point remains, arms `fresh_first_pending` (see above).

```bash
curl -s -X POST $BASE/resume-transmission
```

```json
{"...": "...", "fresh_first_pending": true, "coverage_paused": false, "...": "..."}
```

If a send previously failed (`transmission_state: BLOCKED_ERROR`), resuming also needs explicit confirmation to retry it:

```bash
curl -s -X POST $BASE/resume-transmission \
  -H 'Content-Type: application/json' --data '{"retry_failed":true}'
```

Omitting `retry_failed` while a failure is pending:

```json
{"error":{"code":"RETRY_CONFIRMATION_REQUIRED","message":"simulator: a failed packet is pending; retry_failed must be set to true to retry it","details":{"status":{"...":"..."},"pending_packet_id":"20261025-000004"}}}
```

---

## `POST /reset`

Returns to `READY`, clearing run state (counters, queues, run ID). Requires `READY`/`STOPPED`/`COMPLETED` playback and no in-flight send.

```bash
curl -s -X POST $BASE/reset
```

With unsent telemetry still queued, reset is rejected unless you explicitly discard it:

```json
{"error":{"code":"PENDING_DATA","message":"simulator: unsent telemetry remains; discard_pending must be set to true to discard it","details":{"total_unsent_fixes":6}}}
```

```bash
curl -s -X POST $BASE/reset -H 'Content-Type: application/json' --data '{"discard_pending":true}'
```

```json
{"status": {"...": "...", "playback_state": "READY", "generated_fixes": 0, "...": "..."}, "discarded_fixes": 6, "acceptance_uncertain": false}
```

Other reset errors: `409 STOP_REQUIRED` (playback still running), `409 SEND_IN_FLIGHT` (a send is mid-flight; wait or let it settle first).

---

## Reaching a simulator through the aggregator's proxy

If the aggregator is configured with a non-empty `simulators:` list (`config.example.yaml`), every endpoint above is also reachable indirectly — see [aggregator.md](aggregator.md)'s "Simulator control proxy" section:

```bash
curl -s $AGG_BASE/api/v1/simulators/sim-001/status
curl -s -X PUT $AGG_BASE/api/v1/simulators/sim-001/config -H 'Content-Type: application/json' --data '{"...":"..."}'
curl -s -X POST $AGG_BASE/api/v1/simulators/sim-001/start
```

---

## Worked example: start, inspect the windowed schedule, exercise an outage

```bash
BASE=http://127.0.0.1:8090

# 1. Confirm the configured schedule before any traffic flows.
curl -s $BASE/status | python3 -c "
import json,sys; d=json.load(sys.stdin)
print('expected_fixes_per_report:', d['expected_fixes_per_report'])
print('actual_report_interval_ms:', d['actual_report_interval_ms'])
print('actual_max_packets_per_second:', d['actual_max_packets_per_second'])
"

# 2. Start playback.
curl -s -X POST $BASE/start

# 3. Simulate an outage.
curl -s -X POST $BASE/pause-transmission
sleep 5
curl -s $BASE/status | python3 -c "import json,sys; print(json.load(sys.stdin)['total_unsent_fixes'])"

# 4. Resume and watch the fresh-first preference clear.
curl -s -X POST $BASE/resume-transmission
curl -s $BASE/status | python3 -c "import json,sys; print(json.load(sys.stdin)['fresh_first_pending'])"
```

See [simulator-verification.md](simulator-verification.md) for a fully worked multi-scenario walkthrough (different multiplier/outage/bandwidth combinations with expected numeric results), and [inspector.md](inspector.md) for using `gpsinspect` to confirm the aggregator-side trip/merge effects of a simulator-driven outage.

---

## Note on `specs/SIMULATOR_SPEC.md`

`specs/SIMULATOR_SPEC.md` sections 2, 6, 7, and 8 (packet_size/partial_flush eligibility, an unscaled wall-clock rate, and a hard "recovery gate" that blocked all sends until a post-outage live fix existed) describe the simulator's *original* design. `specs/SIMULATOR_CHANGE_REQUEST.md` superseded that mechanism with the windowed-reporting/scaled-capacity/non-blocking-preference design documented above and implemented in the code; this document (`simulator.md`) reflects the current, as-built behavior. The base spec file itself has not yet been rewritten to match.
