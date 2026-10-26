import { ApiError, idPath, request } from "./api";
import type { Fix, List, Snapshot } from "./types";

export type GeometryMap = Record<string, Fix[]>;
type CacheEntry = { revision: number; fixes: Fix[] };
type Job = {
  controller: AbortController;
  generation: number;
  revision: number;
};

// A single trip read is coherent on the server. Pinning it to the *vehicle*
// revision makes unrelated backlog traffic invalidate live geometry forever.
export function fetchTripGeometry(id: string, signal: AbortSignal) {
  return request<List<Fix>>(`/api/v1/trips/${idPath(id)}/fixes`, { signal });
}

export class TripGeometryLoader {
  private identity = "";
  private database = "";
  private generation = 0;
  private targets = new Map<string, number>();
  private cache = new Map<string, CacheEntry>();
  private jobs = new Map<string, Job>();
  private retries = new Map<string, ReturnType<typeof setTimeout>>();
  private errors = new Map<string, string>();
  private published: GeometryMap = {};

  constructor(
    private fetchTrip: typeof fetchTripGeometry,
    private onData: (data: GeometryMap) => void,
    private onError: (message: string) => void,
    private concurrency = 3,
    private retryMs = 2000,
  ) {}

  update(snapshot: Snapshot | null, visible: string[]) {
    const identity = snapshot
      ? `${snapshot.database_id}:${snapshot.vehicle.vehicle_id}`
      : "";
    if (identity !== this.identity) {
      this.dispose();
      this.identity = identity;
      this.database = snapshot?.database_id || "";
    }
    const selected = new Set(visible);
    this.targets = new Map(
      (snapshot?.trips || [])
        .filter((t) => selected.has(t.trip_id))
        .map((t) => [t.trip_id, t.updated_revision]),
    );
    for (const [id, job] of this.jobs) {
      if (!this.targets.has(id)) {
        job.controller.abort();
        this.jobs.delete(id);
      }
    }
    for (const [id, timer] of this.retries) {
      if (!this.targets.has(id)) {
        clearTimeout(timer);
        this.retries.delete(id);
      }
    }
    for (const id of this.cache.keys())
      if (!this.targets.has(id)) this.cache.delete(id);
    for (const id of this.errors.keys())
      if (!this.targets.has(id)) this.errors.delete(id);
    this.publish();
    this.pump();
  }

  dispose() {
    this.generation++;
    for (const job of this.jobs.values()) job.controller.abort();
    for (const timer of this.retries.values()) clearTimeout(timer);
    this.jobs.clear();
    this.retries.clear();
    this.targets.clear();
    this.cache.clear();
    this.errors.clear();
    this.identity = "";
    this.database = "";
    this.publish();
  }

  private publish() {
    const next = Object.fromEntries(
      [...this.targets.keys()]
        .filter((id) => this.cache.has(id))
        .map((id) => [id, this.cache.get(id)!.fixes]),
    );
    if (
      Object.keys(next).length !== Object.keys(this.published).length ||
      Object.entries(next).some(([id, fixes]) => this.published[id] !== fixes)
    ) {
      this.published = next;
      this.onData(next);
    }
    this.onError([...this.errors.values()].join(" · "));
  }

  private pump() {
    // New selections get a slot before cached routes awaiting refresh.
    const targets = [...this.targets].sort(
      ([a], [b]) => Number(this.cache.has(a)) - Number(this.cache.has(b)),
    );
    for (const [id, revision] of targets) {
      if (this.jobs.size >= this.concurrency) break;
      if (
        this.jobs.has(id) ||
        this.retries.has(id) ||
        (this.cache.get(id)?.revision ?? -1) >= revision
      )
        continue;
      const job: Job = {
        controller: new AbortController(),
        generation: this.generation,
        revision,
      };
      const database = this.database;
      this.jobs.set(id, job);
      const relevant = () =>
        this.generation === job.generation && this.jobs.get(id) === job;
      void Promise.resolve()
        .then(() => this.fetchTrip(id, job.controller.signal))
        .then((data) => {
          if (!relevant()) return;
          if (
            data.database_id !== database ||
            !Number.isSafeInteger(data.revision) ||
            data.revision! < job.revision
          )
            throw new ApiError(
              "INVALID_GEOMETRY",
              "Geometry response has a different database or an older revision.",
            );
          // This response includes every update to this trip through its returned
          // vehicle revision, even if other trips changed during the request.
          this.cache.set(id, {
            revision: data.revision!,
            fixes: data.items || [],
          });
          this.errors.delete(id);
          this.publish(); // Never wait for other selected trips before painting.
        })
        .catch((error) => {
          if (!relevant() || job.controller.signal.aborted) return;
          this.errors.set(id, `Trip ${id}: ${String(error)}`);
          this.publish(); // Keep the previous route on a failed refresh.
          this.retries.set(
            id,
            setTimeout(() => {
              this.retries.delete(id);
              if (this.generation === job.generation) this.pump();
            }, this.retryMs),
          );
        })
        .finally(() => {
          if (!relevant()) return;
          this.jobs.delete(id);
          this.pump(); // Coalesce revisions seen in flight; don't cancel progress.
        });
    }
  }
}
