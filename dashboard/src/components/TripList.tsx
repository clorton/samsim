import type { Trip } from "../types";
import { shortId, displayTime } from "../types";
export default function TripList({
  trips,
  visible,
  focus,
  onToggle,
  onFocus,
}: {
  trips: Trip[];
  visible: string[];
  focus: string;
  onToggle: (id: string) => void;
  onFocus: (id: string) => void;
}) {
  return (
    <section className="trip-list">
      <h2>
        Trips <span>{trips.length}</span>
      </h2>
      <p className="footnote">Check to display · select for details</p>
      {trips
        .slice()
        .sort((a, b) => b.end_ms - a.end_ms)
        .map((t) => (
          <article
            key={t.trip_id}
            className={"trip-card " + (focus === t.trip_id ? "focused" : "")}
          >
            <div className="row">
              <input
                type="checkbox"
                aria-label={`Show trip ${t.trip_id} on map`}
                checked={visible.includes(t.trip_id)}
                onChange={() => onToggle(t.trip_id)}
              />
              <button
                className="text-button trip-name"
                title={t.trip_id}
                onClick={() => onFocus(t.trip_id)}
              >
                {shortId(t.trip_id)}
              </button>
            </div>
            <div className="row trip-state">
              <span className={"status " + t.status.toLowerCase()}>
                {t.status}
              </span>
              {t.close_reason !== "NONE" && <small>{t.close_reason}</small>}
            </div>
            <small>
              {displayTime(t.start_ms)}
              <br />→ {displayTime(t.end_ms)}
            </small>
            <small>{t.fix_count.toLocaleString()} fixes</small>
          </article>
        ))}
      {!trips.length && <p className="empty">No trips yet.</p>}
    </section>
  );
}
