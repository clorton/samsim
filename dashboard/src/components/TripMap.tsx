import { useEffect, useRef, useState } from "react";
import maplibregl from "maplibre-gl";
import type { FeatureCollection, Feature, Geometry } from "geojson";
import type { Fix, Trip } from "../types";
import "maplibre-gl/dist/maplibre-gl.css";
const empty: FeatureCollection = { type: "FeatureCollection", features: [] };
const colors = ["#137d83", "#9166b1", "#bd7c26", "#447ac7", "#b25675"];
export default function TripMap({
  trips,
  geometry,
  latest,
  follow,
  onFollow,
}: {
  trips: Trip[];
  geometry: Record<string, Fix[]>;
  latest: Fix | null;
  follow: boolean;
  onFollow: (value: boolean) => void;
}) {
  const host = useRef<HTMLDivElement>(null),
    map = useRef<maplibregl.Map | null>(null),
    [ready, setReady] = useState(false),
    [error, setError] = useState("");
  const current = useRef({ trips, geometry, latest, follow, onFollow });
  current.current = { trips, geometry, latest, follow, onFollow };
  useEffect(() => {
    if (!host.current) return;
    let m: maplibregl.Map;
    try {
      m = new maplibregl.Map({
        container: host.current,
        center: [-98, 39],
        zoom: 3,
        attributionControl: false,
        style: {
          version: 8,
          sources: {
            states: { type: "geojson", data: "/maps/us_states.geojson" },
            trips: { type: "geojson", data: empty },
            position: { type: "geojson", data: empty },
          },
          layers: [
            {
              id: "background",
              type: "background",
              paint: { "background-color": "#eef3f5" },
            },
            {
              id: "states-fill",
              type: "fill",
              source: "states",
              paint: { "fill-color": "#dfe8e6", "fill-opacity": 0.9 },
            },
            {
              id: "states-outline",
              type: "line",
              source: "states",
              paint: { "line-color": "#a9bbbf", "line-width": 0.7 },
            },
            {
              id: "trip-lines",
              type: "line",
              source: "trips",
              filter: ["==", ["geometry-type"], "LineString"],
              paint: {
                "line-color": ["get", "color"],
                "line-width": 4,
                "line-opacity": 0.95,
              },
            },
            {
              id: "trip-ends",
              type: "circle",
              source: "trips",
              filter: ["==", ["geometry-type"], "Point"],
              paint: {
                "circle-radius": 4,
                "circle-color": ["get", "color"],
                "circle-stroke-color": "#fff",
                "circle-stroke-width": 2,
              },
            },
            {
              id: "vehicle",
              type: "circle",
              source: "position",
              paint: {
                "circle-radius": 8,
                "circle-color": "#126a65",
                "circle-stroke-color": "#fff",
                "circle-stroke-width": 3,
              },
            },
          ],
        },
      });
      map.current = m;
      m.addControl(
        new maplibregl.NavigationControl({ showCompass: false }),
        "top-right",
      );
      m.on("load", () => setReady(true));
      m.on("error", (e) => setError(e.error?.message || "Map could not load"));
      m.on("dragstart", () => current.current.onFollow(false));
      m.on("zoomstart", (e) => {
        if (e.originalEvent) current.current.onFollow(false);
      });
      const observer = new ResizeObserver(() => m.resize());
      observer.observe(host.current);
      return () => {
        observer.disconnect();
        setReady(false);
        m.remove();
        map.current = null;
      };
    } catch (e) {
      setError(`Map requires WebGL: ${String(e)}`);
    }
  }, []);
  useEffect(() => {
    const m = map.current;
    if (!ready || !m) return;
    const features: Feature<Geometry>[] = [];
    trips.forEach((t, i) => {
      const points = geometry[t.trip_id] || [];
      const stride = Math.max(1, Math.ceil(points.length / 10000));
      const coords = points
        .filter((_, j) => j % stride === 0 || j === points.length - 1)
        .map((f) => [f.longitude, f.latitude]);
      if (coords.length > 1)
        features.push({
          type: "Feature",
          properties: { color: colors[i % colors.length] },
          geometry: { type: "LineString", coordinates: coords },
        });
      for (const c of coords.length ? [coords[0], coords.at(-1)!] : [])
        features.push({
          type: "Feature",
          properties: { color: colors[i % colors.length] },
          geometry: { type: "Point", coordinates: c },
        });
    });
    (m.getSource("trips") as maplibregl.GeoJSONSource).setData({
      type: "FeatureCollection",
      features,
    });
    (m.getSource("position") as maplibregl.GeoJSONSource).setData(
      latest
        ? {
            type: "FeatureCollection",
            features: [
              {
                type: "Feature",
                properties: {},
                geometry: {
                  type: "Point",
                  coordinates: [latest.longitude, latest.latitude],
                },
              },
            ],
          }
        : empty,
    );
    if (follow && latest)
      m.easeTo({
        center: [latest.longitude, latest.latitude],
        zoom: Math.max(m.getZoom(), 8),
        duration: 200,
      });
  }, [ready, trips, geometry, latest, follow]);
  function fit() {
    const m = map.current;
    if (!m) return;
    onFollow(false);
    const points = trips.flatMap((t) => geometry[t.trip_id] || []);
    if (!points.length && latest) points.push(latest);
    if (!points.length) return;
    const bounds = new maplibregl.LngLatBounds();
    points.forEach((f) => bounds.extend([f.longitude, f.latitude]));
    m.fitBounds(bounds, { padding: 60, maxZoom: 14, duration: 300 });
  }
  return (
    <section className="map-shell" aria-label="Vehicle trip map">
      <div ref={host} className="map-canvas" />
      <div className="map-actions">
        <button onClick={fit}>Fit visible trips</button>
        <button
          onClick={() => {
            onFollow(false);
            map.current?.fitBounds(
              [
                [-125, 24],
                [-66, 50],
              ],
              { padding: 20, duration: 300 },
            );
          }}
        >
          United States
        </button>
        <button
          aria-pressed={follow}
          className={follow ? "selected" : ""}
          onClick={() => onFollow(!follow)}
        >
          Follow vehicle
        </button>
      </div>
      {error && (
        <div role="alert" className="map-error">
          {error}
        </div>
      )}
      <div className="map-legend">
        {trips.map((t, i) => (
          <span key={t.trip_id}>
            <i style={{ background: colors[i % colors.length] }} />
            {t.status} · {t.trip_id.slice(0, 8)}
          </span>
        ))}
      </div>
      <span className="map-credit">
        US outlines · supplied GeoJSON · no external map tiles
      </span>
    </section>
  );
}
