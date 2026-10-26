import type { Vehicle } from "../types";
export default function VehicleSelector({
  vehicles,
  selected,
  onSelect,
}: {
  vehicles: Vehicle[];
  selected: string;
  onSelect: (id: string) => void;
}) {
  return (
    <section className="vehicle-selector">
      <h2>
        Known vehicles <span>{vehicles.length}</span>
      </h2>
      {vehicles.map((v) => (
        <button
          key={v.vehicle_id}
          className={
            "vehicle-row " + (selected === v.vehicle_id ? "selected" : "")
          }
          onClick={() => onSelect(v.vehicle_id)}
          aria-pressed={selected === v.vehicle_id}
        >
          <span>
            {v.vehicle_id}
            <small>{v.active_trip_id ? "Active trip" : "No active trip"}</small>
          </span>
          <span className={"dot " + (v.active_trip_id ? "online" : "")} />
        </button>
      ))}
      {!vehicles.length && (
        <p className="empty">Vehicles appear after telemetry arrives.</p>
      )}
    </section>
  );
}
