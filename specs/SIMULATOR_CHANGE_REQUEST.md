# Change request: separate normal reporting from transmission capacity

Update the Go simulator and UI to distinguish **when live reports are created** from **how fast queued packets can be transmitted**. This supersedes packet-size/partial-flush logic wherever it would cause live reports to be emitted faster than their normal schedule. Related tasks: SIM-02..07, DASH-01/04.

## Configuration and behavior

- Add `normal_report_interval_simulated_ms`, default **10000**. Create one live report per simulated-time window, containing all newly generated CSV fixes in that window. At 1 Hz, this normally means 10 fixes. Use half-open windows `[start,end)`, first report due at end; no empty reports. Preserve every fix and its original event timestamp. Do not generate intermediate reports simply because transmission capacity is available or a real-time partial-flush timer expired.
- Keep `max_packets_per_simulated_second` as a separate positive fractional setting: the **combined ceiling for live and backlog send attempts**. Example: **5 packets/simulated second**. The prototype uses **0.2** by default so recovery is easy to observe. Maximum capacity is not the normal live reporting frequency.
- Playback multiplier M scales both schedules: actual normal interval = simulated interval / M; actual packet-rate ceiling = configured rate × M. At 100×, a 10-second reporting interval becomes 0.1 actual seconds and a 5-packet ceiling becomes 500 actual packets/s. Real HTTP throughput may be lower; expose target and observed rates separately.
- Source sampling cadence comes from CSV timestamps, not the reporting interval. Show expected fixes per report as a derived estimate. If a window exceeds the server's packet limit, split it into bounded packets at its deadline; never drop rows. Packet-size limits are safety bounds, not the live reporting clock.
- During an outage, generation and report-window assembly continue; all unsent reports are buffered. On recovery, attempt delivery automatically, prioritize available fresh live reports, and use spare capacity for backlog with the existing weighted fairness policy. Never wait for another normal report deadline when backlog is eligible and no live report is ready. EOF flushes remaining fixes and continues draining.
- Preserve the fresh-first recovery exception explicitly: if newer fixes than the last acknowledged position are available, one immediate live report may contain the latest reporting window, even if partial. Reusing fixes from buffered reports is safe under aggregator fix idempotency; preserve buffered packet identity/content. After that one recovery report, return to normal deadlines. Every attempt, including recovery/retry, consumes the same capacity budget. Remove any gate that blocks backlog waiting for a future live fix.
- A new outage can interrupt incomplete recovery. Retain queued/pending data and continue buffering new reports. Stop freezes playback and sending; resume preserves event time and queues. Failed packets retain their existing identity/body and explicit-retry safeguards.
- Runtime changes apply prospectively: speed/capacity recompute pacing without burst credits; interval changes require stopped playback, seal the current partial window once, and anchor the new schedule at current virtual time. Do not rewrite queued packets.

## UI and status

Expose **Normal report interval (simulated seconds)** and **Maximum transmission rate (packets/simulated second)** as independent controls. Show playback speed, expected fixes/report, derived actual reporting interval and actual rate ceiling. Keep live/backlog priority in advanced settings; show queue counts. No backlog hold/release control.

Support the new interval field through existing config/status/proxy paths. Report configured settings, next report deadline, queue counts, target actual rate and observed actual throughput. Keep network timeouts explicitly in actual time.

## Acceptance

1. At 1 Hz, interval 10 seconds, cap 5 packets/s and 1×, ordinary operation creates one 10-fix report every 10 simulated seconds—not five reports/second.
2. Repeat at 100×/1000×; cadence and capacity scale independently and correctly.
3. A 30–60 minute outage buffers every fix; recovery starts automatic live-priority backlog delivery. A second outage retains remaining backlog; several outages can yield 3+ provisional trips before historical bridges merge them.
4. Test irregular CSV timing, window boundaries, EOF partial report, insufficient capacity, runtime changes, retry idempotency, and no accumulated-capacity burst after an outage. Verify every generated fix remains accounted for, including the recovery report's intentional duplicates.
