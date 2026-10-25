# Verifying simulator packet scheduling and bandwidth

A practical guide to convincing yourself that the simulator sends packets on the configured schedule (`normal_report_interval_simulated_ms`) and respects the configured, independently-scaled transmission ceiling (`max_packets_per_simulated_second`) — not the other way around. See [simulator.md](simulator.md) for the full endpoint reference and [specs/SIMULATOR_CHANGE_REQUEST.md](specs/SIMULATOR_CHANGE_REQUEST.md) for the normative design this verifies.

Every command below was run against real local `aggregator`/`simulator` processes while writing this document.

## Background: two independent schedules

- **Reporting schedule** — `normal_report_interval_simulated_ms` (default 10000 = 10 simulated seconds) controls *when a live report is created*, as a pure function of simulated time. At the default 1 Hz source cadence, a 10-second window normally contains 10 fixes. This never speeds up just because transmission capacity is available.
- **Transmission capacity** — `max_packets_per_simulated_second` (default 0.2) is the combined live+backlog send ceiling, in *simulated*-time packets/second. It is scaled by `clock_multiplier` into the real, enforced wall-clock rate: `actual_max_packets_per_second = max_packets_per_simulated_second * clock_multiplier`.
- **`clock_multiplier`** scales both schedules, but independently: `actual_report_interval_ms = normal_report_interval_simulated_ms / clock_multiplier`.

`GET /status` exposes all four of these numbers directly (`expected_fixes_per_report`, `actual_report_interval_ms`, `actual_max_packets_per_second`, `observed_packets_per_second`), so you never have to compute them by hand once the simulator is running — the worked scenarios below compute them in advance anyway, so you know what to expect before you look.

**Constraint to know up front:** `clock_multiplier` must be in `[10, 1000]` (enforced by `Config.Validate`) — a literal 1× real-time run is not configurable. The "realtime" scenario below therefore uses the minimum allowed value, 10×, same as the "10x" scenario; the two scenarios differ in outage duration and bandwidth, not in multiplier.

Both scenarios use `testdata/tennessee.csv` (1 Hz source fixes, ~3.2 hours of route — long enough that end-of-route is never reached during these tests).

---

## 0. Build and start the aggregator

```bash
cd /path/to/samsim
go build -o bin/aggregator ./cmd/aggregator
go build -o bin/simulator ./cmd/simulator
go build -o bin/gpsinspect ./cmd/gpsinspect

./bin/aggregator > aggregator.log 2>&1 &
curl -s http://127.0.0.1:8080/api/v1/health
# {"healthy":true,"database_id":"...","trip_gap_seconds":600}
```

---

## Scenario 1 — "realtime" (floor: 10×), 10s report window, 5-minute outage, 2 pkt/simulated-s

**Computed expectations:**

| Quantity | Value |
|---|---|
| `clock_multiplier` | 10 (minimum allowed; true 1× isn't configurable) |
| Report window | `normal_report_interval_simulated_ms: 10000` → 10 simulated seconds → 10 fixes/report at 1 Hz |
| Actual wall-clock report cadence | `10000 / 10` = **1 report every real second** |
| `max_packets_per_simulated_second: 2` | Actual ceiling: `2 * 10` = **20 packets/s actual** |
| Outage | 5 min = 300 simulated seconds → wall-clock wait: `300 / 10` = **30 real seconds** |
| Expected backlog after outage | ~300 fixes (30 ten-fix packets) |
| 300s outage vs. 600s trip gap | **Below** the gap threshold — expect a **single continuous trip**, no merge |

Create `sim-rt1x.json`:

```json
{
  "listen": "127.0.0.1:8090",
  "simulator_id": "sim-rt1x",
  "vehicle_id": "vehicle-rt1x",
  "aggregator_url": "http://127.0.0.1:8080",
  "routes": { "tenn": "./testdata/tennessee.csv" },
  "route_id": "tenn",
  "run_start_ms": "now",
  "clock_multiplier": 10,
  "expected_fix_interval_ms": 1000,
  "normal_report_interval_simulated_ms": 10000,
  "packet_size": 10,
  "encoding": "json",
  "max_packets_per_simulated_second": 2,
  "live_weight": 3,
  "backlog_weight": 1,
  "request_timeout_ms": 2000,
  "identity_db_path": "./data/simulator-identity-rt1x.sqlite",
  "max_route_bytes": 67108864,
  "max_route_rows": 1000000
}
```

Start it:

```bash
./bin/simulator -config sim-rt1x.json > sim-rt1x.log 2>&1 &
curl -s -X POST http://127.0.0.1:8090/start
```

### Step 1 — confirm the configured schedule before any traffic flows

```bash
curl -s http://127.0.0.1:8090/status | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('expected_fixes_per_report:', d['expected_fixes_per_report'])          # want 10.0
print('actual_report_interval_ms:', d['actual_report_interval_ms'])          # want 1000.0
print('actual_max_packets_per_second:', d['actual_max_packets_per_second'])  # want 20.0
"
```

### Step 2 — watch normal-operation cadence (reporting decoupled from capacity)

Capacity here is 20 actual packets/s, but demand is only ~1 report/s — confirm packets don't land faster just because capacity allows it:

```bash
for i in $(seq 1 8); do
  curl -s http://127.0.0.1:8090/status | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('virtual_ms=%d next_window_close=%s acked=%d congested=%s observed/s=%.2f' % (
  d['virtual_elapsed_ms'], d['next_report_window_close_virtual_ms'],
  d['packets_acknowledged'], d['congested'], d['observed_packets_per_second']))
"
  sleep 1
done
```

**Expect:** `packets_acknowledged` incrementing by ~1 per loop iteration (not bursts of 20), `next_report_window_close_virtual_ms` jumping by exactly 10000 each time `virtual_elapsed_ms` crosses it, and `congested: false`.

### Step 3 — simulate the 5-minute outage

```bash
curl -s -X POST http://127.0.0.1:8090/pause-transmission
sleep 35   # >= 30s needed for 300 simulated seconds at 10x
curl -s http://127.0.0.1:8090/status | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('total_unsent_fixes:', d['total_unsent_fixes'])        # want ~300
print('backlog_queue_count:', d['backlog_queue_count'])
print('backlog_window_pending_count:', d['backlog_window_pending_count'])
"
```

### Step 4 — resume and watch fresh-first + backlog drain

```bash
curl -s -X POST http://127.0.0.1:8090/resume-transmission
for i in $(seq 1 10); do
  curl -s http://127.0.0.1:8090/status | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('fresh_first_pending=%s total_unsent=%d live_q=%d backlog_q=%d acked=%d' % (
  d['fresh_first_pending'], d['total_unsent_fixes'], d['live_queue_count'],
  d['backlog_queue_count'], d['packets_acknowledged']))
"
  sleep 0.5
done
```

**Expect:** `fresh_first_pending: true` immediately after resume, then `false` once a live report lands; `total_unsent_fixes` falling toward 0 quickly (300 fixes over a 20-packet/s ceiling drains in a couple of real seconds).

### Step 5 — cross-check against the aggregator

```bash
./bin/gpsinspect vehicle vehicle-rt1x
./bin/gpsinspect trips vehicle-rt1x --include-merged
```

**Expect:** exactly **one** trip (no `MERGED` entries) — the 300s gap stayed under the 600s threshold — and the latest fix timestamp matching/exceeding the simulator's `latest_acknowledged_timestamp_ms`.

---

## Scenario 2 — "10x", 10s report window, 15-minute outage, 5 pkt/simulated-s

**Computed expectations:**

| Quantity | Value |
|---|---|
| `clock_multiplier` | 10 |
| Actual wall-clock report cadence | `10000 / 10` = **1 report every real second** (same as scenario 1) |
| `max_packets_per_simulated_second: 5` | Actual ceiling: `5 * 10` = **50 packets/s actual** |
| Outage | 15 min = 900 simulated seconds → wall-clock wait: `900 / 10` = **90 real seconds** |
| Expected backlog after outage | ~900 fixes (90 ten-fix packets) |
| 900s outage vs. 600s trip gap | **Exceeds** the threshold — expect **two trips**, merging once backlog bridges the gap |

Create `sim-10x.json`:

```json
{
  "listen": "127.0.0.1:8091",
  "simulator_id": "sim-10x",
  "vehicle_id": "vehicle-10x",
  "aggregator_url": "http://127.0.0.1:8080",
  "routes": { "tenn": "./testdata/tennessee.csv" },
  "route_id": "tenn",
  "run_start_ms": "now",
  "clock_multiplier": 10,
  "expected_fix_interval_ms": 1000,
  "normal_report_interval_simulated_ms": 10000,
  "packet_size": 10,
  "encoding": "json",
  "max_packets_per_simulated_second": 5,
  "live_weight": 3,
  "backlog_weight": 1,
  "request_timeout_ms": 2000,
  "identity_db_path": "./data/simulator-identity-10x.sqlite",
  "max_route_bytes": 67108864,
  "max_route_rows": 1000000
}
```

```bash
./bin/simulator -config sim-10x.json > sim-10x.log 2>&1 &
curl -s -X POST http://127.0.0.1:8091/start
```

Steps 1–2 (schedule sanity check, normal-cadence watch) are identical to scenario 1, just against port `8091` — `actual_max_packets_per_second` should read `50.0` instead of `20.0`.

### Step 3 — simulate the 15-minute outage

```bash
curl -s -X POST http://127.0.0.1:8091/pause-transmission
sleep 95   # >= 90s needed for 900 simulated seconds at 10x
curl -s http://127.0.0.1:8091/status | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('total_unsent_fixes:', d['total_unsent_fixes'])   # want ~900
"
```

### Step 4 — resume and watch the drain

```bash
curl -s -X POST http://127.0.0.1:8091/resume-transmission
for i in $(seq 1 15); do
  curl -s http://127.0.0.1:8091/status | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('fresh_first_pending=%s total_unsent=%d acked=%d' % (
  d['fresh_first_pending'], d['total_unsent_fixes'], d['packets_acknowledged']))
"
  sleep 1
done
```

### Step 5 — cross-check: two trips merging

```bash
./bin/gpsinspect trips vehicle-10x --include-merged
```

**Expect** two entries marked `MERGED` whose `superseded_by_trip_id` points at one current `ACTIVE` trip — the outage/merge pattern, since this gap exceeded 600s. Run `./bin/gpsinspect validate vehicle-10x` for an independent reconstruction check (V04/V07 specifically confirm the merge lineage and membership match).

---

## Adapting these scenarios

To build your own scenario, pick four independent numbers and compute the rest before running anything, so you know what "correct" looks like in advance:

1. **`clock_multiplier`** (10–1000) — how fast simulated time advances relative to wall-clock time.
2. **`normal_report_interval_simulated_ms`** — the simulated-time reporting cadence. Divide by `clock_multiplier` to get the wall-clock cadence you'll actually observe.
3. **`max_packets_per_simulated_second`** — the simulated-time transmission ceiling. Multiply by `clock_multiplier` to get the actual packets/s ceiling. For a clean demo, keep this comfortably above the demand implied by (1)/(2) — `expected_fixes_per_report / packet_size` packets needed per report window — unless you're deliberately demonstrating congestion (see `TestOverload_QueueGrowthAndCongestionNoDrop` in `internal/simulator/overload_test.go` for an intentionally-undercapacity example).
4. **Outage duration (simulated seconds)** — divide by `clock_multiplier` to get how long to `sleep` in wall-clock time. Compare against the aggregator's `trip_gap_seconds` (default 600): below it, expect one continuous trip; above it, expect two trips that later merge once backlog delivery bridges the gap.

`GET /status`'s `expected_fixes_per_report`, `actual_report_interval_ms`, `actual_max_packets_per_second`, and `observed_packets_per_second` let you confirm your arithmetic against the simulator's own view at any point, without waiting for the full scenario to play out.

---

## Cleanup

```bash
kill %1 %2 %3 2>/dev/null   # or: pkill -f './bin/aggregator'; pkill -f './bin/simulator'
rm -rf data/ sim-rt1x.log sim-10x.log aggregator.log
```
