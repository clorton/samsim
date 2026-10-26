import { useEffect, useRef, useState } from "react";
import { allPages, ApiError, idPath, request } from "../api";
import { displayTime, shortId } from "../types";
import type { Trip, TripDetail, Vehicle } from "../types";
export default function DatabasePanel({ onClose }: { onClose: () => void }) {
  const [vehicles, setVehicles] = useState<Vehicle[]>([]),
    [trips, setTrips] = useState<Trip[]>([]),
    [errors, setErrors] = useState<string[]>([]),
    [loading, setLoading] = useState(true),
    [filter, setFilter] = useState("all"),
    [status, setStatus] = useState("all"),
    [page, setPage] = useState(0),
    [detail, setDetail] = useState<TripDetail | null>(null),
    [detailError, setDetailError] = useState(""),
    [stamp, setStamp] = useState<number | null>(null),
    [refresh, setRefresh] = useState(0);
  const detailSequence = useRef(0);
  useEffect(() => {
    const listener = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [onClose]);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const list = await allPages<Vehicle>("/api/v1/vehicles", abort.signal);
        const vehicleRows: Vehicle[] = [],
          tripRows: Trip[] = [],
          failed: string[] = [];
        for (const v of list.items) {
          let success = false;
          for (let attempt = 0; attempt < 3; attempt++) {
            try {
              const vehicle = await request<Vehicle>(
                `/api/v1/vehicles/${idPath(v.vehicle_id)}`,
                { signal: abort.signal },
              );
              const rows = await allPages<Trip>(
                `/api/v1/vehicles/${idPath(v.vehicle_id)}/trips?include_merged=true&at_revision=${vehicle.revision}`,
                abort.signal,
              );
              if (rows.database_id !== list.database_id)
                throw new ApiError(
                  "DATABASE_CHANGED",
                  "Database changed; refresh this view",
                );
              vehicleRows.push(vehicle);
              tripRows.push(...rows.items);
              success = true;
              break;
            } catch (e) {
              if (
                e instanceof ApiError &&
                e.code === "REVISION_CONFLICT" &&
                attempt < 2
              )
                continue;
              failed.push(`${v.vehicle_id}: ${String(e)}`);
              break;
            }
          }
          if (!success) vehicleRows.push(v);
        }
        if (abort.signal.aborted) return;
        setVehicles(vehicleRows);
        setTrips(tripRows);
        setErrors(failed);
        setStamp(Date.now());
      } catch (e) {
        if (!abort.signal.aborted) setErrors([String(e)]);
      } finally {
        if (!abort.signal.aborted) {
          setLoading(false);
          timer = setTimeout(load, 10000);
        }
      }
    };
    void load();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [refresh]);
  const filtered = trips
      .filter(
        (t) =>
          (filter === "all" || t.vehicle_id === filter) &&
          (status === "all" || t.status === status),
      )
      .sort(
        (a, b) => b.start_ms - a.start_ms || a.trip_id.localeCompare(b.trip_id),
      ),
    pages = Math.max(1, Math.ceil(filtered.length / 8)),
    safePage = Math.min(page, pages - 1),
    shown = filtered.slice(safePage * 8, (safePage + 1) * 8);
  async function select(id: string) {
    const mine = ++detailSequence.current;
    setDetailError("");
    try {
      const d = await request<TripDetail>(`/api/v1/trips/${idPath(id)}`);
      if (mine === detailSequence.current) setDetail(d);
    } catch (e) {
      if (mine === detailSequence.current) setDetailError(String(e));
    }
  }
  return (
    <div className="drawer-scrim">
      <aside
        className="database-drawer"
        aria-label="Aggregator database explorer"
      >
        <div className="row between">
          <div>
            <h3>Aggregator database</h3>
            <p className="muted">All vehicles · all trip records</p>
          </div>
          <button onClick={onClose}>Close</button>
        </div>
        <div className="row between">
          <small>
            {loading
              ? "Loading metadata…"
              : stamp
                ? "Updated " + new Date(stamp).toLocaleTimeString()
                : ""}
          </small>
          <button
            onClick={() => {
              setLoading(true);
              setRefresh((k) => k + 1);
            }}
          >
            Refresh
          </button>
        </div>
        {errors.length > 0 && (
          <div role="alert" className="error">
            Partial/stale results: {errors.join(" · ")}
          </div>
        )}
        <h2>Known vehicles · {vehicles.length}</h2>
        <div className="database-vehicles">
          {vehicles.map((v) => (
            <button
              key={v.vehicle_id}
              className={filter === v.vehicle_id ? "selected" : ""}
              onClick={() => {
                setFilter(v.vehicle_id);
                setPage(0);
              }}
            >
              <strong>{v.vehicle_id}</strong>
              <small>
                {trips.filter((t) => t.vehicle_id === v.vehicle_id).length} trip
                records · {v.active_trip_id ? "active trip" : "no active trip"}
              </small>
              <small>
                Latest GPS: {displayTime(v.latest_fix?.timestamp_ms)}
              </small>
            </button>
          ))}
        </div>
        <div className="row between">
          <h2>All trip entries · {trips.length}</h2>
          <div className="row">
            <select
              aria-label="Database vehicle filter"
              value={filter}
              onChange={(e) => {
                setFilter(e.target.value);
                setPage(0);
              }}
            >
              <option value="all">All vehicles</option>
              {vehicles.map((v) => (
                <option key={v.vehicle_id}>{v.vehicle_id}</option>
              ))}
            </select>
            <select
              aria-label="Database status filter"
              value={status}
              onChange={(e) => {
                setStatus(e.target.value);
                setPage(0);
              }}
            >
              <option value="all">All statuses</option>
              <option>ACTIVE</option>
              <option>INACTIVE</option>
              <option>CLOSED</option>
            </select>
          </div>
        </div>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Trip / vehicle</th>
                <th>Status / reason</th>
                <th>Start UTC</th>
                <th>End UTC</th>
                <th>Synopsis</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((t) => (
                <tr key={t.trip_id}>
                  <td>
                    <button
                      className="text-button"
                      title={t.trip_id}
                      onClick={() => void select(t.trip_id)}
                    >
                      {shortId(t.trip_id)}
                    </button>
                    <small>{t.vehicle_id}</small>
                  </td>
                  <td>
                    <span className={"status " + t.status.toLowerCase()}>
                      {t.status}
                    </span>
                    <small>{t.close_reason}</small>
                    {t.superseded_by_trip_id && (
                      <button
                        className="text-button"
                        onClick={() => void select(t.superseded_by_trip_id!)}
                      >
                        → {shortId(t.superseded_by_trip_id)}
                      </button>
                    )}
                  </td>
                  <td>{displayTime(t.start_ms)}</td>
                  <td>{displayTime(t.end_ms)}</td>
                  <td>
                    {t.fix_count.toLocaleString()} fixes
                    <small>
                      {((t.end_ms - t.start_ms) / 60000).toFixed(1)} min span
                    </small>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!shown.length && !loading && (
          <p className="empty">No matching trip records.</p>
        )}
        <div className="row between">
          <button
            disabled={safePage === 0}
            onClick={() => setPage(safePage - 1)}
          >
            Previous
          </button>
          <small>
            {filtered.length} matching · page {safePage + 1} of {pages}
          </small>
          <button
            disabled={safePage >= pages - 1}
            onClick={() => setPage(safePage + 1)}
          >
            Next
          </button>
        </div>
        <p className="footnote">
          Metadata only. CLOSED includes manual closures and merged
          predecessors. Each vehicle is read at its own revision; this is not a
          database-wide transactional snapshot. Refreshes every 10 seconds.
        </p>
        {detailError && (
          <p role="alert" className="error">
            {detailError}
          </p>
        )}
        {detail && (
          <section className="metadata-detail">
            <h3>{detail.trip_id}</h3>
            <p>
              {detail.vehicle_id} · {detail.status} / {detail.close_reason}
            </p>
            <dl>
              <dt>GPS start</dt>
              <dd>{displayTime(detail.start_ms)}</dd>
              <dt>GPS end</dt>
              <dd>{displayTime(detail.end_ms)}</dd>
              <dt>Fixes</dt>
              <dd>{detail.fix_count.toLocaleString()}</dd>
              <dt>Closure time</dt>
              <dd>{displayTime(detail.closed_at_ms)}</dd>
              <dt>Predecessors</dt>
              <dd>
                {detail.immediate_predecessor_trip_ids?.join(", ") || "—"}
              </dd>
              <dt>Successor</dt>
              <dd>{detail.immediate_successor_trip_id || "—"}</dd>
              <dt>Terminal successor</dt>
              <dd>{detail.terminal_successor_trip_id || "—"}</dd>
              <dt>Internal missing fixes</dt>
              <dd>
                {detail.completeness?.missing_fix_count ?? "Unknown"} · boundary
                completeness unknown
              </dd>
              <dt>Bounds</dt>
              <dd>
                {detail.bounds.min_latitude.toFixed(5)},{" "}
                {detail.bounds.min_longitude.toFixed(5)} →{" "}
                {detail.bounds.max_latitude.toFixed(5)},{" "}
                {detail.bounds.max_longitude.toFixed(5)}
              </dd>
            </dl>
          </section>
        )}
      </aside>
    </div>
  );
}
