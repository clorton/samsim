export type Fix = {
  timestamp_ms: number;
  latitude: number;
  longitude: number;
  speed_mps: number;
  heading_degrees: number;
  assignment_reason?: string | null;
};
export type Vehicle = {
  vehicle_id: string;
  active_trip_id: string | null;
  revision: number;
  latest_fix?: Fix | null;
  database_id?: string;
};
export type Trip = {
  trip_id: string;
  vehicle_id: string;
  status: "ACTIVE" | "INACTIVE" | "CLOSED";
  close_reason: string;
  start_ms: number;
  end_ms: number;
  fix_count: number;
  bounds: {
    min_latitude: number;
    max_latitude: number;
    min_longitude: number;
    max_longitude: number;
  };
  superseded_by_trip_id: string | null;
  updated_revision: number;
  created_revision: number;
  closed_at_ms: number | null;
  forced_close: boolean;
};
export type Completeness = {
  trip_id: string;
  revision: number;
  missing_fix_count: number;
  missing_intervals:
    { start_ms: number; end_ms: number; missing_count: number }[] | null;
  complete: boolean;
  boundary_completeness: string;
};
export type TripDetail = Trip & {
  immediate_predecessor_trip_ids: string[] | null;
  immediate_successor_trip_id: string | null;
  terminal_successor_trip_id: string | null;
  completeness?: Completeness;
};
export type Snapshot = {
  vehicle: Vehicle;
  trips: Trip[] | null;
  revision: number;
  database_id: string;
};
export type List<T> = {
  items: T[] | null;
  next_cursor?: string | null;
  revision?: number;
  database_id?: string;
};
export type SimConfig = {
  route_id: string;
  run_start_ms: number | "now";
  clock_multiplier: number;
  expected_fix_interval_ms: number;
  normal_report_interval_simulated_ms: number;
  packet_size: number;
  encoding: string;
  max_packets_per_simulated_second: number;
  live_weight: number;
  backlog_weight: number;
};
export type SimStatus = {
  simulator_id: string;
  vehicle_id: string;
  control_revision: number;
  playback_state: string;
  transmission_state: string;
  coverage_paused: boolean;
  config: SimConfig;
  available_route_ids: string[];
  resolved_run_start_ms: number | null;
  virtual_elapsed_ms: number;
  route_rows: number;
  generated_fixes: number;
  acknowledged_fixes: number;
  total_unsent_fixes: number;
  live_queue_count: number;
  backlog_queue_count: number;
  live_window_pending_count: number;
  backlog_window_pending_count: number;
  pending_fix_count: number;
  in_flight: boolean;
  pending_failed: boolean;
  latest_generated_timestamp_ms: number | null;
  expected_fixes_per_report: number;
  actual_report_interval_ms: number;
  actual_max_packets_per_second: number;
  observed_packets_per_second: number;
  last_error: { message: string; acceptance_uncertain: boolean } | null;
};
export type SimInfo = { simulator_id: string; vehicle_id: string };
export type EventFrame = {
  type: string;
  database_id: string;
  vehicle_id: string;
  revision: number;
  timestamp_ms?: number;
  changes?: {
    type: string;
    trip_id?: string;
    predecessor_trip_ids?: string[];
    successor_trip_id?: string;
    payload?: Record<string, unknown>;
  }[];
};
export const displayTime = (ms: number | null | undefined) =>
  ms == null
    ? "—"
    : new Date(ms)
        .toISOString()
        .replace("T", " ")
        .replace(".000Z", " UTC")
        .replace("Z", " UTC");
export const shortId = (id: string) =>
  id.length > 20 ? id.slice(0, 8) + "…" + id.slice(-6) : id;
export const speedFromPosition = (position: number) =>
  Math.max(1, Math.min(1000, Math.round(10 ** (position / 10000))));

// Reveal only on an active-trip transition, not on every telemetry snapshot:
// a user's later decision to uncheck the same active trip must be respected.
export function revealNewActiveTrip(
  visible: string[],
  previousActive: string | null,
  active: string | null,
) {
  return active && active !== previousActive && !visible.includes(active)
    ? [...visible, active]
    : visible;
}

export function reconcileSelection(
  selected: string[],
  focus: string,
  changes: NonNullable<EventFrame["changes"]>,
) {
  for (const c of changes) {
    if (c.type !== "trips.merged") continue;
    const p = (c.payload || c) as {
      predecessor_trip_ids?: string[];
      successor_trip_id?: string;
    };
    if (!p.successor_trip_id) continue;
    const old = new Set(p.predecessor_trip_ids || []);
    selected = selected.map((id) => (old.has(id) ? p.successor_trip_id! : id));
    if (old.has(focus)) focus = p.successor_trip_id;
  }
  return { selected: [...new Set(selected)], focus };
}
