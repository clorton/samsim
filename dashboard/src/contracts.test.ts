import { describe, it, expect, vi } from "vitest";
import { allPages, bounded, ApiError } from "./api";
import {
  reconcileSelection,
  revealNewActiveTrip,
  speedFromPosition,
} from "./types";
describe("dashboard contracts", () => {
  it("reveals successive post-outage active trips without hiding historical selections", () => {
    let visible = ["historical", "before-outage"];
    visible = revealNewActiveTrip(
      visible,
      "before-outage",
      "after-first-outage",
    );
    visible = revealNewActiveTrip(
      visible,
      "after-first-outage",
      "after-second-outage",
    );
    expect(visible).toEqual([
      "historical",
      "before-outage",
      "after-first-outage",
      "after-second-outage",
    ]);
  });
  it("respects unchecking an active trip until a different trip becomes active", () => {
    const unchecked = ["historical"];
    expect(revealNewActiveTrip(unchecked, "active", "active")).toBe(unchecked);
    expect(revealNewActiveTrip(unchecked, "active", null)).toBe(unchecked);
    expect(revealNewActiveTrip(unchecked, null, "next-active")).toEqual([
      "historical",
      "next-active",
    ]);
  });
  it("does not duplicate a merged successor that is already selected", () => {
    const selected = ["successor"];
    expect(revealNewActiveTrip(selected, "predecessor", "successor")).toBe(
      selected,
    );
  });
  it("supports every integer speed with logarithmic landmarks", () => {
    expect([0, 10000, 20000, 30000].map(speedFromPosition)).toEqual([
      1, 10, 100, 1000,
    ]);
    expect(
      new Set(Array.from({ length: 30001 }, (_, i) => speedFromPosition(i)))
        .size,
    ).toBe(1000);
  });
  it("follows selected predecessors through chained merge events", () => {
    const result = reconcileSelection(["a", "b", "other"], "b", [
      {
        type: "trips.merged",
        predecessor_trip_ids: ["a", "b"],
        successor_trip_id: "c",
      },
      {
        type: "trips.merged",
        predecessor_trip_ids: ["c", "d"],
        successor_trip_id: "e",
      },
    ]);
    expect(result).toEqual({ selected: ["e", "other"], focus: "e" });
  });
  it("bounds geometry previews and redacts credentials", () => {
    const b = bounded({
      authorization: "secret",
      items: Array.from({ length: 1000 }, (_, i) => ({ timestamp_ms: i })),
    }) as {
      authorization: string;
      items: { count: number; preview: unknown[]; truncated: boolean };
    };
    expect(b.authorization).toBe("[redacted]");
    expect(b.items.count).toBe(1000);
    expect(b.items.preview).toHaveLength(5);
    expect(b.items.truncated).toBe(true);
  });
  it("keeps revision preconditions on cursor requests", async () => {
    const fetcher = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            items: [1],
            revision: 4,
            database_id: "d",
            next_cursor: "cursor",
          }),
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            items: [2],
            revision: 4,
            database_id: "d",
            next_cursor: null,
          }),
        ),
      );
    try {
      const result = await allPages<number>(
        "/api/v1/vehicles/v/trips?at_revision=4",
      );
      expect(result.items).toEqual([1, 2]);
      expect(fetcher.mock.calls[1][0]).toContain("at_revision=4");
      expect(fetcher.mock.calls[1][0]).toContain("cursor=cursor");
    } finally {
      fetcher.mockRestore();
    }
  });
  it("rejects mixed revisions rather than displaying inconsistent pages", async () => {
    const fetcher = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            items: [1],
            revision: 4,
            database_id: "d",
            next_cursor: "cursor",
          }),
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            items: [2],
            revision: 5,
            database_id: "d",
            next_cursor: null,
          }),
        ),
      );
    try {
      await expect(allPages("/api/v1/test")).rejects.toMatchObject({
        code: "REVISION_CONFLICT",
      } satisfies Partial<ApiError>);
    } finally {
      fetcher.mockRestore();
    }
  });
});
