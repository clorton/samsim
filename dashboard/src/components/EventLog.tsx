import { useEffect, useRef, useState } from "react";
import { useHistory } from "../api";
export default function EventLog() {
  const { entries, evicted, clear } = useHistory(),
    [open, setOpen] = useState(false),
    [filter, setFilter] = useState("all"),
    [query, setQuery] = useState(""),
    [follow, setFollow] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  const filtered = entries.filter(
    (e) =>
      (filter === "all" || filter === "errors"
        ? filter !== "errors" || e.status === 0 || (e.status || 0) >= 400
        : e.kind === filter) &&
      e.summary.toLowerCase().includes(query.toLowerCase()),
  );
  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [entries, follow, open]);
  return (
    <section className="history-panel">
      <div className="history-toolbar">
        <button
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="text-button"
        >
          {open ? "▾" : "▸"} History{" "}
          <span className="muted">
            {entries.length} entries
            {evicted ? ` · ${evicted} older removed` : ""}
          </span>
        </button>
        <div className="row">
          <select
            aria-label="History filter"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          >
            <option value="all">All traffic</option>
            <option value="call">Calls</option>
            <option value="event">Events</option>
            <option value="errors">Errors</option>
            <option value="notice">Connection notices</option>
          </select>
          {open && (
            <input
              aria-label="Search history"
              placeholder="Search traffic"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          )}
          <button onClick={clear}>Clear</button>
        </div>
      </div>
      {open && (
        <>
          <div
            className="history-rows"
            ref={box}
            onScroll={() => {
              const b = box.current;
              if (b)
                setFollow(b.scrollHeight - b.scrollTop - b.clientHeight < 30);
            }}
          >
            {filtered.map((e) => (
              <details
                key={e.id}
                className={
                  "history-row " +
                  (e.status === 0 || (e.status || 0) >= 400 ? "failed" : "")
                }
              >
                <summary>
                  <time>{new Date(e.time).toLocaleTimeString()}</time>
                  <span className="history-kind">
                    {e.kind === "event"
                      ? "WS"
                      : e.kind === "call"
                        ? "HTTP"
                        : "UI"}
                  </span>
                  <span>{e.summary}</span>
                  {e.duration !== undefined && <span>{e.duration} ms</span>}
                  {e.pending && <span>Pending</span>}
                </summary>
                <pre>{e.details}</pre>
              </details>
            ))}
            {!filtered.length && <p className="empty">No matching entries.</p>}
          </div>
          {!follow && (
            <button
              onClick={() => {
                setFollow(true);
                box.current?.scrollTo(0, box.current.scrollHeight);
              }}
            >
              Jump to latest
            </button>
          )}
          <p className="footnote">
            This browser session only · body previews are bounded and redacted ·
            event replay is not available.
          </p>
        </>
      )}
    </section>
  );
}
