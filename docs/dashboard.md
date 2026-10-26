# Dashboard

The implemented local dashboard lives in `dashboard/`. See [its README](../dashboard/README.md) for installation, three-service startup, controls, upload behavior, consistency rules and tests.

New integration endpoints:

- `POST /api/v1/simulators/{id}/routes`: aggregator proxy for a raw `text/csv` upload (64 MiB maximum; trusted simulator registry only).
- `POST /routes`: simulator-side CSV validation and bounded in-memory registration. READY/empty required. Returns `route_id`, `fix_count`, `duration_ms`, `first_offset_ms`, `control_revision`, and `persistent:false`. Use returned ID with existing `PUT /config`; uploaded bytes survive reset but not process restart.
- Existing proxied simulator actions now forward bounded JSON bodies, enabling `retry_failed` and `discard_pending` explicitly.

The simulator clock-multiplier range is now 1–1000 to support the requested logarithmic slider. Normal reporting cadence and maximum transmission capacity retain the implemented simulated-time semantics in [simulator.md](simulator.md).

Timed interruptions are scheduled by the browser against polled simulator virtual time. Keep the page connected until automatic restoration; there is no new server-managed outage-deadline endpoint.
