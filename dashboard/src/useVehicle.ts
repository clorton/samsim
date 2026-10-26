import { useEffect, useRef, useState, useCallback } from "react";
import { ApiError, history, idPath, request } from "./api";
import type { EventFrame, Snapshot } from "./types";

export function useVehicle(
  vehicleId: string,
  onChanges: (frame: EventFrame) => void,
) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null),
    [connection, setConnection] = useState("Select a vehicle"),
    [error, setError] = useState("");
  const changes = useRef(onChanges);
  changes.current = onChanges;
  const trigger = useRef<() => void>(() => {});
  const refresh = useCallback(() => trigger.current(), []);
  useEffect(() => {
    setSnapshot(null);
    setError("");
    if (!vehicleId) {
      setConnection("Select a vehicle");
      return;
    }
    let stopped = false,
      ws: WebSocket | null = null,
      reconnect: ReturnType<typeof setTimeout>,
      debounce: ReturnType<typeof setTimeout> | undefined,
      ready = false,
      seen = 0,
      database = "",
      installed = 0,
      busy = false,
      dirty = false,
      failures = 0,
      generation = 0;
    const abort = new AbortController();
    const fetchSnapshot = async () => {
      if (stopped || busy || !ready) {
        dirty = true;
        return;
      }
      busy = true;
      dirty = false;
      const mine = generation;
      try {
        const snap = await request<Snapshot>(
          `/api/v1/vehicles/${idPath(vehicleId)}/snapshot`,
          { signal: abort.signal },
        );
        if (stopped || mine !== generation) return;
        if (snap.database_id !== database) {
          history("notice", "Database changed; reconnecting");
          ws?.close();
          return;
        }
        if (snap.revision < installed) return;
        installed = snap.revision;
        setSnapshot({ ...snap, trips: snap.trips || [] });
        setError("");
        setConnection("Live · synchronized");
        failures = 0;
      } catch (e) {
        if (stopped) return;
        if (e instanceof ApiError && e.status === 404) {
          setConnection("Waiting for first telemetry");
          setError("");
        } else {
          setError(e instanceof Error ? e.message : String(e));
          setConnection("Snapshot unavailable");
        }
      } finally {
        busy = false;
        if (dirty && !stopped)
          debounce = setTimeout(() => {
            debounce = undefined;
            void fetchSnapshot();
          }, 250);
      }
    };
    const schedule = () => {
      dirty = true;
      if (debounce) return;
      debounce = setTimeout(() => {
        debounce = undefined;
        void fetchSnapshot();
      }, 200);
    };
    trigger.current = schedule;
    const connect = () => {
      if (stopped) return;
      generation++;
      ready = false;
      installed = 0;
      setConnection(failures ? "Reconnecting…" : "Connecting…");
      const url = new URL("/api/v1/events", location.origin);
      url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
      url.searchParams.set("vehicle_id", vehicleId);
      ws = new WebSocket(url);
      const current = ws;
      history("notice", `WS subscribe · ${vehicleId}`);
      const handshake = setTimeout(() => {
        if (!ready) current.close();
      }, 10000);
      current.onmessage = (e) => {
        if (stopped || current !== ws) return;
        try {
          if (typeof e.data !== "string" || e.data.length > 1024 * 1024)
            throw Error("Oversized event");
          const f = JSON.parse(e.data) as EventFrame;
          history(
            "event",
            `${f.type} · ${f.vehicle_id} · rev ${f.revision}${f.changes ? " · " + f.changes.map((c) => c.type + (c.successor_trip_id ? " → " + c.successor_trip_id : "")).join("; ") : ""}`,
            f,
          );
          if (
            f.vehicle_id !== vehicleId ||
            !Number.isSafeInteger(f.revision) ||
            !f.database_id
          )
            throw Error("Invalid event identity");
          if (!ready) {
            if (f.type !== "ready") throw Error("Expected ready handshake");
            ready = true;
            clearTimeout(handshake);
            seen = f.revision;
            database = f.database_id;
            dirty = true;
            void fetchSnapshot();
            return;
          }
          if (f.database_id !== database) throw Error("Database changed");
          if (f.revision <= seen) return;
          if (f.revision !== seen + 1) throw Error("Revision gap");
          seen = f.revision;
          changes.current(f);
          schedule();
        } catch (e) {
          history("notice", `WS resync · ${String(e)}`);
          current.close();
        }
      };
      current.onerror = () => {
        setConnection("Connection interrupted");
      };
      current.onclose = () => {
        clearTimeout(handshake);
        if (stopped || current !== ws) return;
        ready = false;
        setConnection("Disconnected · reconnecting");
        history(
          "notice",
          `WS disconnected · ${vehicleId}; resnapshot on reconnect`,
        );
        reconnect = setTimeout(
          connect,
          Math.min(10000, 500 * 2 ** Math.min(failures++, 5)),
        );
      };
    };
    connect();
    const fallback = setInterval(() => {
      if (ready) schedule();
    }, 10000);
    return () => {
      stopped = true;
      abort.abort();
      clearInterval(fallback);
      clearTimeout(reconnect);
      clearTimeout(debounce);
      ws?.close();
      trigger.current = () => {};
    };
  }, [vehicleId]);
  return { snapshot, connection, error, refresh };
}
