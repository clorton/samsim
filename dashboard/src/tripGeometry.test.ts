import { describe, expect, it, vi } from "vitest";
import {
  TripGeometryLoader,
  fetchTripGeometry,
  type GeometryMap,
} from "./tripGeometry";
import type { Fix, List, Snapshot, Trip } from "./types";

const fix = (timestamp_ms: number): Fix => ({
  timestamp_ms,
  latitude: 40 + timestamp_ms / 100000,
  longitude: -73,
  speed_mps: 4,
  heading_degrees: 90,
});
const snapshot = (
  revision: number,
  entries: [string, number][],
  database_id = "db",
  vehicle_id = "vehicle",
): Snapshot => ({
  revision,
  database_id,
  vehicle: {
    vehicle_id,
    revision,
    active_trip_id: entries.at(-1)?.[0] || null,
  },
  trips: entries.map(
    ([trip_id, updated_revision]) => ({ trip_id, updated_revision }) as Trip,
  ),
});
const flush = async () => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};
function harness() {
  const calls: {
    id: string;
    signal: AbortSignal;
    resolve: (data: List<Fix>) => void;
    reject: (error: Error) => void;
  }[] = [];
  let data: GeometryMap = {},
    error = "";
  const loader = new TripGeometryLoader(
    (id, signal) =>
      new Promise((resolve, reject) =>
        calls.push({ id, signal, resolve, reject }),
      ),
    (next) => {
      data = next;
    },
    (next) => {
      error = next;
    },
  );
  return { loader, calls, data: () => data, error: () => error };
}

describe("live trip geometry during backlog recovery", () => {
  it("displays a checked post-outage trip before slow historical geometry or a merge completes", async () => {
    const h = harness();
    h.loader.update(
      snapshot(10, [
        ["before-outage", 9],
        ["live-after-outage", 10],
      ]),
      ["before-outage", "live-after-outage"],
    );
    await flush();
    for (let r = 11; r <= 30; r++)
      h.loader.update(
        snapshot(r, [
          ["before-outage", r],
          ["live-after-outage", 10],
        ]),
        ["before-outage", "live-after-outage"],
      );
    expect(h.calls).toHaveLength(2);
    expect(h.calls.every((c) => !c.signal.aborted)).toBe(true);
    h.calls
      .find((c) => c.id === "live-after-outage")!
      .resolve({
        database_id: "db",
        revision: 31,
        items: [fix(1000), fix(2000)],
      });
    await flush();
    expect(h.data()["live-after-outage"]).toEqual([fix(1000), fix(2000)]);
    expect(h.data()["before-outage"]).toBeUndefined();
    expect(h.error()).toBe("");
    h.loader.dispose();
  });

  it("coalesces updates without canceling progress, and keeps a visible route while refreshing", async () => {
    const h = harness();
    h.loader.update(snapshot(1, [["live", 1]]), ["live"]);
    await flush();
    h.loader.update(snapshot(3, [["live", 3]]), ["live"]);
    h.calls[0].resolve({ database_id: "db", revision: 2, items: [fix(1)] });
    await flush();
    expect(h.data().live).toEqual([fix(1)]);
    expect(h.calls).toHaveLength(2);
    expect(h.calls[0].signal.aborted).toBe(false);
    h.loader.update(snapshot(4, [["live", 4]]), ["live"]);
    expect(h.data().live).toEqual([fix(1)]);
    h.calls[1].resolve({
      database_id: "db",
      revision: 5,
      items: [fix(1), fix(2)],
    });
    await flush();
    expect(h.data().live).toHaveLength(2);
    expect(h.calls).toHaveLength(2);
    h.loader.dispose();
  });

  it("does not refetch active geometry for revisions affecting only historical trips", async () => {
    const h = harness();
    h.loader.update(snapshot(10, [["live", 8]]), ["live"]);
    await flush();
    h.calls[0].resolve({ database_id: "db", revision: 12, items: [fix(1)] });
    await flush();
    for (let r = 13; r < 40; r++)
      h.loader.update(
        snapshot(r, [
          ["live", 8],
          ["historical", r],
        ]),
        ["live"],
      );
    expect(h.calls).toHaveLength(1);
    expect(h.data().live).toHaveLength(1);
    h.loader.dispose();
  });

  it("cannot repaint a deselected trip or install data from a previous vehicle/database", async () => {
    const h = harness();
    h.loader.update(snapshot(1, [["a", 1]]), ["a"]);
    await flush();
    h.loader.update(snapshot(2, [["a", 1]]), []);
    h.calls[0].resolve({ database_id: "db", revision: 2, items: [fix(1)] });
    await flush();
    expect(h.data()).toEqual({});
    h.loader.update(snapshot(2, [["b", 2]]), ["b"]);
    await flush();
    h.loader.update(snapshot(1, [["b", 1]], "different-db", "other-vehicle"), [
      "b",
    ]);
    await flush();
    h.calls[1].resolve({ database_id: "db", revision: 3, items: [fix(10)] });
    await flush();
    expect(h.data()).toEqual({});
    h.calls[2].resolve({
      database_id: "different-db",
      revision: 1,
      items: [fix(20)],
    });
    await flush();
    expect(h.data().b).toEqual([fix(20)]);
    h.loader.dispose();
  });

  it("retains completed routes when another request fails instead of clearing the whole map", async () => {
    const h = harness();
    h.loader.update(
      snapshot(1, [
        ["active", 1],
        ["old", 1],
      ]),
      ["active", "old"],
    );
    await flush();
    h.calls[0].resolve({ database_id: "db", revision: 1, items: [fix(1)] });
    h.calls[1].reject(new Error("temporarily offline"));
    await flush();
    expect(h.data().active).toEqual([fix(1)]);
    expect(h.error()).toContain("temporarily offline");
    h.loader.dispose();
  });

  it("reads coherent per-trip geometry without an invalidating vehicle-wide precondition", async () => {
    const fetcher = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ database_id: "db", revision: 50, items: [fix(1)] }),
        ),
      );
    try {
      await fetchTripGeometry("new-trip", new AbortController().signal);
      expect(fetcher.mock.calls[0][0]).toBe("/api/v1/trips/new-trip/fixes");
    } finally {
      fetcher.mockRestore();
    }
  });
});
