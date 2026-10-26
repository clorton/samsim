import { useEffect, useRef, useState } from "react";
import Notification from "./Notification";
import { ApiError, idPath, json, request, history } from "../api";
import { displayTime, speedFromPosition } from "../types";
import type { SimConfig, SimInfo, SimStatus } from "../types";

type Draft = {
  vehicle: string;
  route: string;
  position: number;
  interval: number;
  capacity: number;
  live: number;
  backlog: number;
};
const defaultDraft: Draft = {
  vehicle: "",
  route: "",
  position: 20000,
  interval: 10,
  capacity: 0.2,
  live: 3,
  backlog: 1,
};
export default function SimulatorControls() {
  const [sims, setSims] = useState<SimInfo[]>([]),
    [selected, setSelected] = useState(""),
    [statuses, setStatuses] = useState<Record<string, SimStatus>>({}),
    [failures, setFailures] = useState<Record<string, string>>({}),
    [registryError, setRegistryError] = useState(""),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState(""),
    [form, setForm] = useState(false),
    [draft, setDraft] = useState(defaultDraft),
    [fileLabel, setFileLabel] = useState(""),
    [uploading, setUploading] = useState(false),
    [outageMinutes, setOutageMinutes] = useState(30),
    [timed, setTimed] = useState<Record<string, number>>({}),
    [refreshKey, setRefreshKey] = useState(0);
  const [uploadInvalid, setUploadInvalid] = useState(false);
  const selectedRef = useRef(selected);
  selectedRef.current = selected;
  const deadlines = useRef<Record<string, number>>({}),
    alive = useRef(true),
    operation = useRef(false);
  const status = statuses[selected];
  const base = (id = selected) => `/api/v1/simulators/${idPath(id)}`;
  useEffect(() => {
    alive.current = true;
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const d = await request<{ items: SimInfo[] | null }>(
          "/api/v1/simulators",
          { signal: abort.signal },
        );
        if (!alive.current) return;
        const items = (d.items || []).sort((a, b) =>
          a.simulator_id.localeCompare(b.simulator_id),
        );
        setSims(items);
        setRegistryError(
          items.length
            ? ""
            : "No simulators registered. Add simulator endpoints to the aggregator configuration.",
        );
        setSelected((v) =>
          items.some((s) => s.simulator_id === v)
            ? v
            : items[0]?.simulator_id || "",
        );
      } catch (e) {
        if (!abort.signal.aborted)
          setRegistryError(
            e instanceof ApiError && e.status === 404
              ? "Simulator proxy is not configured. Use config.dashboard.example.yaml when starting the aggregator."
              : String(e),
          );
      } finally {
        if (!abort.signal.aborted) timer = setTimeout(load, 15000);
      }
    };
    void load();
    return () => {
      alive.current = false;
      abort.abort();
      clearTimeout(timer);
    };
  }, []);
  useEffect(() => {
    if (!sims.length) return;
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      await Promise.all(
        sims.map(async (sim) => {
          const id = sim.simulator_id;
          try {
            const s = await request<SimStatus>(base(id) + "/status", {
              signal: abort.signal,
            });
            if (abort.signal.aborted) return;
            setStatuses((old) => ({ ...old, [id]: s }));
            setFailures((old) => ({ ...old, [id]: "" }));
            const deadline = deadlines.current[id];
            if (deadline !== undefined) {
              if (s.playback_state === "READY" || !s.coverage_paused) {
                delete deadlines.current[id];
                setTimed({ ...deadlines.current });
              } else if (s.virtual_elapsed_ms >= deadline) {
                delete deadlines.current[id];
                setTimed({ ...deadlines.current });
                history(
                  "notice",
                  `Timed interruption ended · ${id} · requesting resume`,
                );
                try {
                  await request(
                    base(id) + "/resume-transmission",
                    json("POST", {}),
                  );
                } catch (e) {
                  setFailures((old) => ({
                    ...old,
                    [id]: `Timed resume failed: ${String(e)}. Review and resume manually.`,
                  }));
                }
              }
            }
          } catch (e) {
            if (!abort.signal.aborted)
              setFailures((old) => ({ ...old, [id]: String(e) }));
          }
        }),
      );
      if (!abort.signal.aborted) timer = setTimeout(poll, 1000);
    };
    void poll();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [sims, refreshKey]);
  async function action(name: string, body: unknown = {}, id = selected) {
    if (operation.current) return;
    operation.current = true;
    setBusy(true);
    setMessage("");
    try {
      const r = await request<SimStatus | { status: SimStatus }>(
        base(id) + "/" + name,
        json("POST", body),
      );
      const s = "status" in r ? r.status : r;
      setStatuses((old) => ({ ...old, [id]: s }));
      if (name === "resume-transmission" || name === "reset") {
        delete deadlines.current[id];
        setTimed({ ...deadlines.current });
      }
      setMessage(`${id}: ${name} succeeded.`);
    } catch (e) {
      setMessage(
        `${String(e)}${e instanceof ApiError ? "" : " The outcome may be uncertain; inspect status before repeating."}`,
      );
    } finally {
      operation.current = false;
      setBusy(false);
      setRefreshKey((k) => k + 1);
    }
  }
  async function updateConfig(changes: Partial<SimConfig>) {
    if (!status || operation.current) return;
    operation.current = true;
    setBusy(true);
    setMessage("");
    try {
      const r = await request<SimStatus>(
        base() + "/config",
        json("PUT", {
          ...changes,
          expected_control_revision: status.control_revision,
        }),
      );
      setStatuses((old) => ({ ...old, [selected]: r }));
      setMessage("Configuration saved.");
    } catch (e) {
      setMessage(String(e));
    } finally {
      operation.current = false;
      setBusy(false);
      setRefreshKey((k) => k + 1);
    }
  }
  function openForm() {
    if (!status) return;
    const c = status.config;
    setDraft({
      vehicle: status.vehicle_id,
      route: c.route_id,
      position: Math.round(Math.log10(c.clock_multiplier) * 10000),
      interval: c.normal_report_interval_simulated_ms / 1000,
      capacity: c.max_packets_per_simulated_second,
      live: c.live_weight,
      backlog: c.backlog_weight,
    });
    setFileLabel("");
    setUploadInvalid(false);
    setMessage("");
    setForm(true);
  }
  async function upload(file?: File) {
    if (!file || !status) return;
    setUploadInvalid(true);
    if (status.playback_state !== "READY") {
      setMessage(
        "Reset the stopped simulator to READY before uploading a new source.",
      );
      return;
    }
    if (
      file.size > 64 * 1024 * 1024 ||
      !file.name.toLowerCase().endsWith(".csv")
    ) {
      setMessage("Choose one .csv file no larger than 64 MiB.");
      return;
    }
    setUploading(true);
    setMessage("");
    try {
      const result = await request<{
        route_id: string;
        fix_count: number;
        duration_ms: number;
        control_revision: number;
      }>(
        base() + "/routes",
        { method: "POST", headers: { "Content-Type": "text/csv" }, body: file },
        120000,
      );
      setDraft((d) => ({ ...d, route: result.route_id }));
      setFileLabel(
        `${file.name} · ${result.fix_count.toLocaleString()} fixes · ${(result.duration_ms / 60000).toFixed(1)} simulated minutes · temporary until simulator restart`,
      );
      const s = await request<SimStatus>(base() + "/status");
      setStatuses((old) => ({ ...old, [selected]: s }));
      setUploadInvalid(false);
    } catch (e) {
      setMessage(String(e));
    } finally {
      setUploading(false);
    }
  }
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!status || operation.current || uploading || uploadInvalid) return;
    if (!/^[A-Za-z0-9._-]{1,128}$/.test(draft.vehicle)) {
      setMessage(
        "Vehicle ID must be 1–128 letters, digits, periods, underscores or hyphens.",
      );
      return;
    }
    operation.current = true;
    setBusy(true);
    setMessage("");
    try {
      const ready = status.playback_state === "READY";
      const body: Record<string, unknown> = {
        clock_multiplier: speedFromPosition(draft.position),
        max_packets_per_simulated_second: draft.capacity,
        live_weight: draft.live,
        backlog_weight: draft.backlog,
        expected_control_revision: status.control_revision,
      };
      if (ready) {
        body.vehicle_id = draft.vehicle;
        body.route_id = draft.route;
        body.run_start_ms = "now";
      }
      if (["READY", "STOPPED"].includes(status.playback_state))
        body.normal_report_interval_simulated_ms = Math.round(
          draft.interval * 1000,
        );
      await request(base() + "/config", json("PUT", body));
      const s = await request<SimStatus>(base() + "/start", json("POST", {}));
      setStatuses((old) => ({ ...old, [selected]: s }));
      setForm(false);
    } catch (e) {
      setMessage(String(e));
    } finally {
      operation.current = false;
      setBusy(false);
      setRefreshKey((k) => k + 1);
    }
  }
  async function timedOutage() {
    if (!status || operation.current) return;
    operation.current = true;
    setBusy(true);
    try {
      const s = await request<SimStatus>(
        base() + "/pause-transmission",
        json("POST", {}),
      );
      deadlines.current[selected] =
        s.virtual_elapsed_ms + outageMinutes * 60000;
      setTimed({ ...deadlines.current });
      setStatuses((old) => ({ ...old, [selected]: s }));
      setMessage(
        "Timed interruption scheduled in simulated time. Keep this dashboard open; stopping playback freezes its countdown.",
      );
    } catch (e) {
      setMessage(String(e));
    } finally {
      operation.current = false;
      setBusy(false);
    }
  }
  const running =
      status && ["RUNNING", "DRAINING"].includes(status.playback_state),
    remaining =
      status && timed[selected] !== undefined
        ? Math.max(0, (timed[selected] - status.virtual_elapsed_ms) / 60000)
        : null;
  return (
    <>
      <section className="control-strip">
        <div className="control-group">
          <div className="row between">
            <h2>Simulator</h2>
            <select
              aria-label="Selected simulator"
              value={selected}
              onChange={(e) => {
                setSelected(e.target.value);
                setMessage("");
              }}
              disabled={busy || form}
            >
              {sims.map((s) => (
                <option key={s.simulator_id}>{s.simulator_id}</option>
              ))}
            </select>
          </div>
          {status ? (
            <>
              <div className="row">
                <strong>{status.vehicle_id}</strong>
                <span className="status">{status.playback_state}</span>
                <span>{status.config.clock_multiplier}×</span>
              </div>
              <p className="footnote">Source: {status.config.route_id}</p>
              <div className="row">
                <button className="primary" disabled={busy} onClick={openForm}>
                  {status.playback_state === "READY"
                    ? "Configure & start…"
                    : "Settings…"}
                </button>
                <button
                  disabled={busy || status.playback_state !== "STOPPED"}
                  onClick={() => void action("start")}
                >
                  Resume playback
                </button>
                <button
                  disabled={busy || !running}
                  onClick={() => void action("stop")}
                >
                  Stop
                </button>
                <button
                  disabled={busy || running || status.in_flight}
                  onClick={() => {
                    if (
                      status.total_unsent_fixes &&
                      !window.confirm(
                        `Discard ${status.total_unsent_fixes.toLocaleString()} unsent fixes? Reset cannot undo data already accepted by the aggregator.`,
                      )
                    )
                      return;
                    void action("reset", {
                      discard_pending: status.total_unsent_fixes > 0,
                    });
                  }}
                >
                  Reset…
                </button>
              </div>
              <div className="queue-summary">
                <span>
                  Live{" "}
                  <b>
                    {(
                      (status.live_queue_count || 0) +
                      (status.live_window_pending_count || 0)
                    ).toLocaleString()}
                  </b>
                </span>
                <span>
                  Buffered{" "}
                  <b>
                    {(
                      (status.backlog_queue_count || 0) +
                      (status.backlog_window_pending_count || 0)
                    ).toLocaleString()}
                  </b>
                </span>
                <span>
                  Pending <b>{status.pending_fix_count}</b>
                </span>
              </div>
            </>
          ) : (
            <p className="muted">
              {registryError || "Connecting to simulator…"}
            </p>
          )}
        </div>
        <div className="control-group">
          <h2>Reporting & transmission</h2>
          {status && (
            <>
              <div className="row">
                <div>
                  <small>Normal report interval</small>
                  <strong>
                    {status.config.normal_report_interval_simulated_ms / 1000}{" "}
                    simulated s
                  </strong>
                </div>
                <div>
                  <small>Maximum transmission</small>
                  <strong>
                    {status.config.max_packets_per_simulated_second} packets /
                    sim s
                  </strong>
                </div>
              </div>
              <p className="footnote">
                ≈ {status.expected_fixes_per_report} fixes/report · actual
                interval {status.actual_report_interval_ms.toFixed(1)} ms
              </p>
              <p className="footnote">
                Target ≤ {status.actual_max_packets_per_second.toFixed(1)}{" "}
                actual packets/s · observed{" "}
                {status.observed_packets_per_second.toFixed(1)}/s
              </p>
              <div className="row">
                <button disabled={busy} onClick={openForm}>
                  Edit settings
                </button>
                <span className="muted">
                  Live/backlog {status.config.live_weight}:
                  {status.config.backlog_weight}
                </span>
              </div>
            </>
          )}
        </div>
        <div className="control-group">
          <h2>Communication interruption</h2>
          {status && (
            <>
              <div className="row">
                <label>
                  Duration · simulated min
                  <input
                    type="number"
                    value={outageMinutes}
                    min={1}
                    max={120}
                    onChange={(e) => setOutageMinutes(Number(e.target.value))}
                  />
                </label>
                <button
                  disabled={
                    busy ||
                    !running ||
                    status.coverage_paused ||
                    !Number.isFinite(outageMinutes) ||
                    outageMinutes < 1 ||
                    outageMinutes > 120
                  }
                  onClick={() => void timedOutage()}
                >
                  Start timed outage
                </button>
              </div>
              <div className="row">
                <button
                  disabled={busy || !running}
                  onClick={() =>
                    void action(
                      status.coverage_paused
                        ? "resume-transmission"
                        : "pause-transmission",
                    )
                  }
                >
                  {status.coverage_paused
                    ? "Resume transmission"
                    : "Pause transmission"}
                </button>
                {status.pending_failed && (
                  <button
                    className="danger"
                    disabled={busy}
                    onClick={() => {
                      if (
                        window.confirm(
                          "Retry the retained packet once with its original identity?",
                        )
                      )
                        void action("resume-transmission", {
                          retry_failed: true,
                        });
                    }}
                  >
                    Retry failed packet…
                  </button>
                )}
                <span className="status">{status.transmission_state}</span>
              </div>
              <p className="footnote">
                {remaining !== null
                  ? `${remaining.toFixed(1)} simulated min remaining · `
                  : ""}
                GPS time {displayTime(status.latest_generated_timestamp_ms)}
              </p>
              <p className="footnote">
                Backlog drains automatically after recovery. Timed restoration
                requires this page to remain connected.
              </p>
            </>
          )}
        </div>
      </section>
      {(message || failures[selected] || status?.last_error) && (
        <Notification>
          {message || failures[selected] || status?.last_error?.message}
          {status?.last_error?.acceptance_uncertain
            ? " · Prior acceptance may be uncertain."
            : ""}
        </Notification>
      )}
      {form && status && (
        <div
          className="modal-scrim"
          onKeyDown={(e) => {
            if (e.key === "Escape" && !busy && !uploading) setForm(false);
          }}
        >
          <section
            className="modal wide"
            role="dialog"
            aria-modal="true"
            aria-labelledby="sim-config-title"
          >
            <div className="row between">
              <h3 id="sim-config-title">Configure {selected}</h3>
              <button
                disabled={busy || uploading}
                onClick={() => setForm(false)}
              >
                Cancel
              </button>
            </div>
            <form onSubmit={(e) => void submit(e)}>
              <div className="form-columns">
                <section>
                  <h2>Route & identity</h2>
                  <label>
                    Source route
                    <select
                      value={draft.route}
                      onChange={(e) => {
                        setDraft({ ...draft, route: e.target.value });
                        setUploadInvalid(false);
                        setFileLabel("");
                        setMessage("");
                      }}
                      disabled={status.playback_state !== "READY" || uploading}
                    >
                      {status.available_route_ids.map((id) => (
                        <option key={id}>{id}</option>
                      ))}
                    </select>
                  </label>
                  <div
                    className="drop-zone"
                    onDragOver={(e) => e.preventDefault()}
                    onDrop={(e) => {
                      e.preventDefault();
                      if (e.dataTransfer.files.length !== 1) {
                        setUploadInvalid(true);
                        setMessage("Drop one CSV file.");
                        return;
                      }
                      void upload(e.dataTransfer.files[0]);
                    }}
                  >
                    <label>
                      Choose a CSV file or drop it here
                      <input
                        type="file"
                        accept=".csv,text/csv"
                        disabled={
                          uploading || status.playback_state !== "READY"
                        }
                        onChange={(e) => void upload(e.target.files?.[0])}
                      />
                    </label>
                    <p className="footnote">
                      {uploading
                        ? "Validating CSV on simulator…"
                        : fileLabel ||
                          "ts, lat, lon, speed_mps, heading_deg · up to 64 MiB"}
                    </p>
                  </div>
                  {uploadInvalid && !uploading && (
                    <button
                      type="button"
                      onClick={() => {
                        setUploadInvalid(false);
                        setFileLabel("");
                        setMessage(
                          "Using the selected registered route instead of the rejected file.",
                        );
                      }}
                    >
                      Use selected registered route instead
                    </button>
                  )}
                  <label>
                    Vehicle ID
                    <input
                      value={draft.vehicle}
                      required
                      maxLength={128}
                      pattern="[A-Za-z0-9._-]{1,128}"
                      disabled={status.playback_state !== "READY"}
                      onChange={(e) =>
                        setDraft({ ...draft, vehicle: e.target.value })
                      }
                    />
                  </label>
                  <small>
                    Letters, digits, periods, underscores and hyphens.
                    Source/vehicle changes require READY; stop and reset an
                    existing run first.
                  </small>
                </section>
                <section>
                  <h2>Playback & delivery</h2>
                  <label>
                    Playback speed{" "}
                    <output>{speedFromPosition(draft.position)}×</output>
                    <input
                      type="range"
                      min={0}
                      max={30000}
                      step={1}
                      value={draft.position}
                      aria-valuetext={`${speedFromPosition(draft.position)} times real time`}
                      onChange={(e) =>
                        setDraft({ ...draft, position: Number(e.target.value) })
                      }
                    />
                  </label>
                  <div className="ruler">
                    <span>1×</span>
                    <span>10×</span>
                    <span>100×</span>
                    <span>1000×</span>
                  </div>
                  <label>
                    Normal report interval · simulated seconds
                    <input
                      type="number"
                      min={0.001}
                      step={0.001}
                      required
                      value={draft.interval}
                      disabled={
                        !["READY", "STOPPED"].includes(status.playback_state)
                      }
                      onChange={(e) =>
                        setDraft({ ...draft, interval: Number(e.target.value) })
                      }
                    />
                  </label>
                  <label>
                    Maximum packets per simulated second
                    <input
                      type="number"
                      min={0.001}
                      max={10000}
                      step="any"
                      required
                      value={draft.capacity}
                      onChange={(e) =>
                        setDraft({ ...draft, capacity: Number(e.target.value) })
                      }
                    />
                  </label>
                  <div className="row">
                    <label>
                      Live weight
                      <input
                        type="number"
                        min={1}
                        max={100}
                        value={draft.live}
                        required
                        onChange={(e) =>
                          setDraft({ ...draft, live: Number(e.target.value) })
                        }
                      />
                    </label>
                    <label>
                      Backlog weight
                      <input
                        type="number"
                        min={1}
                        max={100}
                        value={draft.backlog}
                        required
                        onChange={(e) =>
                          setDraft({
                            ...draft,
                            backlog: Number(e.target.value),
                          })
                        }
                      />
                    </label>
                  </div>
                  <small>
                    Actual normal interval{" "}
                    {(
                      draft.interval / speedFromPosition(draft.position)
                    ).toFixed(3)}{" "}
                    s · maximum{" "}
                    {(
                      draft.capacity * speedFromPosition(draft.position)
                    ).toFixed(1)}{" "}
                    actual packets/s. Reporting interval requires stopped
                    playback.
                  </small>
                </section>
              </div>
              {message && (
                <p className="error" role="alert">
                  {message}
                </p>
              )}
              <div className="row modal-footer">
                <button
                  className="primary"
                  disabled={
                    busy ||
                    uploading ||
                    uploadInvalid ||
                    status.playback_state === "COMPLETED"
                  }
                >
                  {busy
                    ? "Applying…"
                    : running
                      ? "Apply hot settings"
                      : status.playback_state === "READY"
                        ? "Apply & start"
                        : "Apply & resume"}
                </button>
                <span className="muted">
                  Existing queues and packet identities are preserved.
                </span>
              </div>
            </form>
          </section>
        </div>
      )}
    </>
  );
}
