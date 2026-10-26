# Samsim dashboard

The actual React/TypeScript dashboard for the Go aggregator and simulator. It uses the implemented contracts in `docs/aggregator.md` and `docs/simulator.md`, not sample telemetry. MapLibre renders the supplied US GeoJSON with no tile service or map API key.

## Run locally

Prerequisites: Go, Node.js 22.12+ (Node 24 also works), and pnpm. All commands start from the repository root unless noted. Keep the services bound to loopback; this demonstration has no authentication.

```bash
# Terminal 1: aggregator with the simulator control registry enabled.
go run ./cmd/aggregator -config config.dashboard.example.yaml

# Terminal 2: simulator (starts READY; the UI starts playback).
go run ./cmd/simulator -config simulator.example.json

# Terminal 3: dashboard.
cd dashboard
pnpm install
pnpm dev
```

Open **http://127.0.0.1:5173/**. The dashboard's development server proxies `/api` and WebSockets to `http://127.0.0.1:8080`, avoiding browser CORS configuration. There is no cloud deployment. To target another aggregator:

```bash
AGGREGATOR_URL=http://127.0.0.1:18080 pnpm dev
```

The default `config.example.yaml` has an empty simulator registry, so simulator controls return 404 if you start with that file unchanged. `config.dashboard.example.yaml` adds sim-001 at port 8090 while retaining the default aggregator database. If your existing database uses nondefault trip-gap/cadence settings, preserve those frozen settings when adapting the example.

For several simulator processes, give each its own config/listen address, simulator ID and identity database; add each control URL to the aggregator registry. The dashboard selector discovers them through `/api/v1/simulators`. Selecting a simulator does not select a map vehicle automatically.

## Workflow

1. Choose a simulator and open **Configure & start**. Select a registered route or choose/drop one CSV, then enter a vehicle ID. Uploads require READY/empty state; stop/reset an existing run before changing source or vehicle. Reset prompts before discarding unsent fixes.
2. Set playback speed with the logarithmic integer **1–1000×** slider. Set the **normal report interval** (default 10 simulated seconds) separately from the **maximum packets per simulated second** (default 0.2). The dialog shows both derived actual-time rates. Interval changes require READY/STOPPED; speed/capacity/weights can change while running.
3. Apply/start and select a known vehicle. Its active trip is visible by default; every newly active trip (including after an outage) is automatically added without hiding historical selections. You can uncheck it afterward; ordinary updates do not recheck the same active trip. Check other trips to show them and click a trip to focus its details. Pan/zoom, Fit visible trips, United States and Follow vehicle affect map presentation only. The latest vehicle position comes from the aggregator's maximum event timestamp.
4. Pause transmission to keep generating buffered fixes. Resume transmission immediately restarts live-priority backlog delivery. Stop freezes both generation and sending; Resume playback preserves queues and the connection state.
5. Timed outages use the simulator's virtual elapsed time and are managed by this open page. Stop freezes the countdown. Keep the dashboard open and connected until restoration; closing/reloading it loses its automatic-resume schedule. Failures are surfaced for manual recovery, not silently retried with force.
6. The **Database** flyout lists all known vehicles and trip summaries, including CLOSED/MANUAL and CLOSED/MERGED audit predecessors. It fetches metadata only, with per-vehicle revision-bound pagination; it never downloads geometry just for that table. Results can be partial/stale during failures or continuous writes and are labeled accordingly.
7. **History** shows bounded request outcomes/durations, events and reconnect notices. Expand rows for redacted/truncated previews; use filters/search, clear, or scroll away to stop auto-follow. It is a browser-session diagnostic log, not durable replay.

Manual closure always reviews current completeness and binds confirmation to its revision. Known internal gaps require explicit acknowledgment; boundary completeness remains unknown even without internal gaps. If telemetry changes during review, reopen/review rather than automatically applying the old confirmation to new data. Closed trips cannot be reopened from this UI.

## CSV uploads

The dashboard now supports `POST /api/v1/simulators/{id}/routes`, proxied to the selected simulator's `POST /routes` with a raw `text/csv` body. The simulator validates with its existing route parser, returns an opaque content-derived `route_id`, fix count and duration, and registers that ID for subsequent `PUT /config`.

Required columns: `ts,lat,lon,speed_mps,heading_deg`; `ts` is elapsed seconds. Optional `dist_m` and `speed_source` retain the existing parser semantics. File names are display-only and never filesystem paths. Upload is limited to 64 MiB and the simulator's configured row/byte limits; retained uploads are bounded to 16 files and twice the per-file byte limit. The upload proxy has a 30-second deadline; ordinary control requests keep their existing three-second deadline.

**Uploaded source data is kept in simulator memory and is lost when that process restarts.** Keep the original CSV; use a configured route file for a durable route registration. The UI labels uploaded metadata as temporary. Uploading does not change the active route until configuration is applied. Invalid files leave the previous configuration intact.

The action proxy also forwards JSON bodies now, so explicit `retry_failed` and `discard_pending` confirmations reach the simulator. There are no implicit mutation retries. The simulator minimum multiplier is now 1× to match the agreed UI range.

## Consistency and limits

- Subscribe to the selected vehicle first, then install a coherent snapshot; events are invalidation hints. Missing revisions, disconnections and database identity changes cause reconnect/resnapshot.
- Geometry is fetched only for visible trips. Each trip response is a coherent server read, tagged with its database identity and vehicle revision. Routes update independently as responses arrive; cached geometry remains visible during refresh. Requests are coalesced rather than canceled by every snapshot, and unrelated backlog revisions do not invalidate live-trip reads. Map geometry may be newer than the metadata snapshot while telemetry is arriving; closure and database pagination retain their strict revision checks. Merge events move selected predecessor IDs to their successor.
- Informational notices and global errors render in a fixed notification stack outside the page flow. Refreshing geometry never shows a routine revision-conflict warning or shifts the map/history layout.
- The database panel browses per-vehicle snapshots, not one global database transaction. A constantly changing vehicle may require a refresh or stopped playback for complete pagination.
- Request responses are bounded to 32 MiB. History retains up to 2000 entries/5 MiB, with small array previews and 16 KiB details per row. Route drawing downsamples to at most about 10,000 vertices per trip without changing aggregator data.
- The map background intentionally has state boundaries rather than street tiles; at street zoom levels, no road basemap is available. WebGL is required. Typography has a system-font fallback if external fonts cannot load.

## Verify / production build

```bash
cd dashboard
pnpm test
pnpm build
pnpm preview
```

Preview serves the production build at **http://127.0.0.1:4173/** with the same API proxy; services must be running. For another target set `AGGREGATOR_URL` on the preview command too. `dist/` by itself is static: another host must proxy `/api` and WebSocket upgrades to the local aggregator. Do not expose this unauthenticated demonstration publicly.

From the repository root, run `go test ./...` and `go test -race ./...`. The Go additions include tests for CSV validation/selection, refusal during an active run, 1× support, CSV proxying and confirmation-body forwarding. Frontend tests cover logarithmic speed coverage, chained merge selection, bounded/redacted logging and revision-safe pagination.

Original dashboard placeholders were replaced; existing route files, configurations and persistent databases are retained. The provided ZIP is kept alongside extracted `public/maps/us_states.geojson`.
