import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, allPages, history, idPath, json, request } from "./api";
import {
  displayTime,
  reconcileSelection,
  revealNewActiveTrip,
  shortId,
} from "./types";
import type { EventFrame, TripDetail, Vehicle } from "./types";
import { useVehicle } from "./useVehicle";
import { useTripGeometry } from "./useTripGeometry";
import Notification from "./components/Notification";
import SimulatorControls from "./components/SimulatorControls";
import VehicleSelector from "./components/VehicleSelector";
import TripList from "./components/TripList";
import TripMap from "./components/TripMap";
import EventLog from "./components/EventLog";
import DatabasePanel from "./components/DatabasePanel";

export default function App() {
  const [vehicles, setVehicles] = useState<Vehicle[]>([]),
    [vehicle, setVehicle] = useState(""),
    [visible, setVisible] = useState<string[]>([]),
    [focus, setFocus] = useState(""),
    [follow, setFollow] = useState(true),
    [databaseOpen, setDatabaseOpen] = useState(false),
    [registryError, setRegistryError] = useState(""),
    [notice, setNotice] = useState(""),
    [detail, setDetail] = useState<TripDetail | null>(null),
    [detailError, setDetailError] = useState(""),
    [closure, setClosure] = useState<TripDetail | null>(null),
    [ack, setAck] = useState(false),
    [closing, setClosing] = useState(false),
    [closeError, setCloseError] = useState("");
  const selectedRef = useRef({ visible, focus });
  selectedRef.current = { visible, focus };
  const changes = useCallback((frame: EventFrame) => {
    if (!frame.changes?.some((c) => c.type === "trips.merged")) return;
    const next = reconcileSelection(
      selectedRef.current.visible,
      selectedRef.current.focus,
      frame.changes,
    );
    setVisible(next.selected);
    setFocus(next.focus);
    setNotice(
      "Historical telemetry merged trips. The selected predecessors now follow their successor.",
    );
  }, []);
  const { snapshot, connection, error, refresh } = useVehicle(vehicle, changes);
  const { geometry, geometryError } = useTripGeometry(snapshot, visible);
  const closeDatabase = useCallback(() => setDatabaseOpen(false), []);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const d = await allPages<Vehicle>("/api/v1/vehicles", abort.signal);
        if (abort.signal.aborted) return;
        setVehicles(d.items);
        setRegistryError("");
        setVehicle((v) =>
          d.items.some((x) => x.vehicle_id === v)
            ? v
            : d.items[0]?.vehicle_id || "",
        );
      } catch (e) {
        if (!abort.signal.aborted)
          setRegistryError(`Aggregator unavailable: ${String(e)}`);
      } finally {
        if (!abort.signal.aborted) timer = setTimeout(load, 3000);
      }
    };
    void load();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, []);
  useEffect(() => {
    setVisible([]);
    setFocus("");
    setDetail(null);
    setDetailError("");
    setNotice("");
    setClosure(null);
  }, [vehicle]);
  // Initialize per vehicle/database, then reveal each newly active trip once.
  const initialized = useRef("");
  const previousActive = useRef<string | null>(null);
  useEffect(() => {
    if (!snapshot || snapshot.vehicle.vehicle_id !== vehicle) return;
    const identity = vehicle + ":" + snapshot.database_id;
    const active = snapshot.vehicle.active_trip_id;
    if (initialized.current !== identity) {
      initialized.current = identity;
      previousActive.current = active;
      const initial = active || snapshot.trips?.at(-1)?.trip_id || "";
      setVisible(initial ? [initial] : []);
      setFocus(initial);
    } else if (active !== previousActive.current) {
      const previous = previousActive.current;
      previousActive.current = active;
      setVisible((current) => revealNewActiveTrip(current, previous, active));
      if (active) setFocus((current) => current || active);
    }
  }, [snapshot, vehicle]);
  useEffect(() => {
    setDetail(null);
    setDetailError("");
    if (!focus) return;
    const abort = new AbortController();
    void request<TripDetail>(`/api/v1/trips/${idPath(focus)}`, {
      signal: abort.signal,
    })
      .then((d) => {
        if (!abort.signal.aborted) setDetail(d);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setDetailError(String(e));
      });
    return () => abort.abort();
  }, [focus, snapshot?.revision]);
  async function reviewClose() {
    if (!focus) return;
    setCloseError("");
    setAck(false);
    try {
      const d = await request<TripDetail>(`/api/v1/trips/${idPath(focus)}`);
      if (d.status === "CLOSED") {
        setNotice(`Trip is already CLOSED/${d.close_reason}.`);
        return;
      }
      if (!d.completeness) throw Error("Closure assessment unavailable");
      setClosure(d);
    } catch (e) {
      setNotice(String(e));
    }
  }
  async function closeTrip() {
    if (!closure?.completeness) return;
    setClosing(true);
    setCloseError("");
    try {
      await request(
        `/api/v1/trips/${idPath(closure.trip_id)}/close`,
        json("POST", {
          expected_revision: closure.completeness.revision,
          force: ack,
        }),
      );
      setClosure(null);
      setNotice(
        "Trip closed manually. Late internal fixes will be retained as unassigned telemetry.",
      );
      refresh();
    } catch (e) {
      if (e instanceof ApiError && e.code === "REVISION_CONFLICT") {
        setCloseError(
          "Vehicle changed since review. Close this dialog and review again; confirmation cannot carry across revisions.",
        );
        setAck(false);
      } else
        setCloseError(
          `${String(e)}${e instanceof ApiError ? "" : " Outcome uncertain: inspect the trip before trying again."}`,
        );
    } finally {
      setClosing(false);
    }
  }
  const tripList = snapshot?.trips || [],
    chosen = tripList.find((t) => t.trip_id === focus),
    mapTrips = tripList.filter((t) => visible.includes(t.trip_id));
  return (
    <div className="app">
      <header className="app-header">
        <div className="brand">
          samsim <span>/ telemetry</span>
        </div>
        <span className="environment">Local workspace</span>
        <button
          aria-expanded={databaseOpen}
          onClick={() => setDatabaseOpen(!databaseOpen)}
        >
          Database · vehicles & trips
        </button>
      </header>
      <SimulatorControls />
      {registryError && (
        <Notification error>
          {registryError}
          <button onClick={() => location.reload()}>Reconnect</button>
        </Notification>
      )}
      <main className="workspace">
        <div className="workspace-heading">
          <div>
            <h1>{vehicle || "Fleet telemetry"}</h1>
            <p className="muted">
              Latest GPS{" "}
              {displayTime(snapshot?.vehicle.latest_fix?.timestamp_ms)}
              {snapshot?.vehicle.latest_fix
                ? ` · ${snapshot.vehicle.latest_fix.speed_mps.toFixed(1)} m/s · ${snapshot.vehicle.latest_fix.heading_degrees.toFixed(0)}°`
                : ""}
            </p>
          </div>
          <div className="connection">
            <span className="status">{connection}</span>
            {snapshot && (
              <small>
                Revision {snapshot.revision} · {shortId(snapshot.database_id)}
              </small>
            )}
          </div>
        </div>
        <div className="workbench">
          <aside className="left-rail">
            <VehicleSelector
              vehicles={vehicles}
              selected={vehicle}
              onSelect={(id) => {
                setVehicle(id);
                initialized.current = "";
              }}
            />
            <TripList
              trips={tripList}
              visible={visible}
              focus={focus}
              onToggle={(id) =>
                setVisible((old) =>
                  old.includes(id) ? old.filter((x) => x !== id) : [...old, id],
                )
              }
              onFocus={setFocus}
            />
          </aside>
          <div className="map-workspace">
            <TripMap
              trips={mapTrips}
              geometry={geometry}
              latest={snapshot?.vehicle.latest_fix || null}
              follow={follow}
              onFollow={setFollow}
            />
            <div className="trip-detail">
              <div>
                {chosen ? (
                  <>
                    <strong title={chosen.trip_id}>
                      {shortId(chosen.trip_id)}
                    </strong>
                    <span className={"status " + chosen.status.toLowerCase()}>
                      {chosen.status}
                    </span>
                    <small>
                      {chosen.fix_count.toLocaleString()} fixes ·{" "}
                      {displayTime(chosen.start_ms)} →{" "}
                      {displayTime(chosen.end_ms)}
                    </small>
                    <small>
                      {detail?.completeness
                        ? `${detail.completeness.missing_fix_count.toLocaleString()} missing internal fixes · `
                        : ""}
                      Boundary completeness unknown
                    </small>
                  </>
                ) : (
                  <span className="muted">
                    Select a vehicle and a trip to inspect its route.
                  </span>
                )}
              </div>
              <button
                disabled={!chosen || chosen.status === "CLOSED"}
                onClick={() => void reviewClose()}
              >
                Close trip…
              </button>
            </div>
          </div>
        </div>
        {(error || geometryError || detailError) && (
          <Notification error>
            {error || geometryError || detailError}
          </Notification>
        )}
        {notice && (
          <Notification onDismiss={() => setNotice("")}>{notice}</Notification>
        )}
      </main>
      <EventLog />
      {databaseOpen && <DatabasePanel onClose={closeDatabase} />}
      {closure && (
        <div
          className="modal-scrim"
          onKeyDown={(e) => {
            if (e.key === "Escape" && !closing) setClosure(null);
          }}
        >
          <section
            className="modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="close-title"
          >
            <h3 id="close-title">Close trip permanently?</h3>
            <p className="mono">{closure.trip_id}</p>
            <p>
              {closure.completeness!.missing_fix_count.toLocaleString()}{" "}
              expected internal fixes are missing. Boundary completeness is
              unknown even when no internal gaps are detected.
            </p>
            <p>
              Closing freezes this trip. Later internal fixes are stored as
              unassigned telemetry. This cannot be undone in this UI.
            </p>
            {closure.completeness!.missing_fix_count > 0 && (
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={ack}
                  onChange={(e) => setAck(e.target.checked)}
                />
                I acknowledge the missing fixes and want to close anyway.
              </label>
            )}
            {closeError && (
              <p role="alert" className="error">
                {closeError}
              </p>
            )}
            <div className="row">
              <button disabled={closing} onClick={() => setClosure(null)}>
                Cancel
              </button>
              <button
                className="danger"
                disabled={
                  closing ||
                  !!closeError ||
                  (closure.completeness!.missing_fix_count > 0 && !ack)
                }
                onClick={() => void closeTrip()}
              >
                {closing ? "Closing…" : "Close permanently"}
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
