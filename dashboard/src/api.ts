import { useSyncExternalStore } from "react";
import type { List } from "./types";
export class ApiError extends Error {
  constructor(
    public code: string,
    message: string,
    public status = 0,
    public details: Record<string, unknown> = {},
  ) {
    super(message);
  }
}
export type HistoryRow = {
  id: number;
  time: number;
  kind: "call" | "event" | "notice";
  summary: string;
  status?: number;
  duration?: number;
  details: string;
  pending?: boolean;
};
let rows: HistoryRow[] = [],
  seq = 0;
const listeners = new Set<() => void>();
let evicted = 0;
export function bounded(value: unknown, depth = 0): unknown {
  if (depth > 5) return "[depth truncated]";
  if (typeof value === "string")
    return value.length > 1000 ? value.slice(0, 1000) + "… [truncated]" : value;
  if (Array.isArray(value))
    return {
      count: value.length,
      preview: value.slice(0, 5).map((v) => bounded(v, depth + 1)),
      truncated: value.length > 5,
    };
  if (value && typeof value === "object")
    return Object.fromEntries(
      Object.entries(value)
        .slice(0, 40)
        .map(([k, v]) => [
          k,
          /authorization|cookie|password|token|secret/i.test(k)
            ? "[redacted]"
            : bounded(v, depth + 1),
        ]),
    );
  return value;
}
function publish() {
  let bytes = rows.reduce(
    (n, r) => n + r.details.length * 2 + r.summary.length * 2,
    0,
  );
  while (rows.length > 2000 || bytes > 5 * 1024 * 1024) {
    const r = rows[0];
    bytes -= r.details.length * 2 + r.summary.length * 2;
    rows = rows.slice(1);
    evicted++;
  }
  listeners.forEach((f) => f());
}
export function history(
  kind: HistoryRow["kind"],
  summary: string,
  details: unknown = {},
  status?: number,
) {
  const r = {
    id: ++seq,
    time: Date.now(),
    kind,
    summary: summary.slice(0, 300),
    details: JSON.stringify(bounded(details), null, 2).slice(0, 16000),
    status,
  };
  rows = [...rows, r];
  publish();
  return r.id;
}
function update(id: number, patch: Partial<HistoryRow>) {
  const found = rows.some((r) => r.id === id);
  rows = found
    ? rows.map((r) => (r.id === id ? { ...r, ...patch } : r))
    : [
        ...rows,
        {
          id,
          time: Date.now(),
          kind: "call",
          summary: "Request started before history was cleared",
          details: "",
          ...patch,
        },
      ];
  publish();
}
export function useHistory() {
  const entries = useSyncExternalStore(
    (f) => {
      listeners.add(f);
      return () => {
        listeners.delete(f);
      };
    },
    () => rows,
  );
  return {
    entries,
    evicted,
    clear: () => {
      rows = [];
      evicted = 0;
      publish();
    },
  };
}
export async function request<T>(
  path: string,
  options: RequestInit = {},
  timeout = 10000,
): Promise<T> {
  const method = options.method || "GET",
    start = performance.now(),
    id = history("call", `${method} ${path} · pending`, {
      request:
        typeof options.body === "string" &&
        options.headers &&
        JSON.stringify(options.headers).includes("json")
          ? safeParse(options.body)
          : options.body
            ? "[CSV upload: body omitted]"
            : undefined,
    });
  update(id, { pending: true });
  const controller = new AbortController(),
    timer = setTimeout(() => controller.abort(), timeout);
  const cancel = () => controller.abort();
  options.signal?.addEventListener("abort", cancel, { once: true });
  if (options.signal?.aborted) controller.abort();
  try {
    const response = await fetch(path, {
      ...options,
      signal: controller.signal,
      redirect: "error",
    });
    const reader = response.body?.getReader();
    let text = "",
      bytes = 0;
    const decoder = new TextDecoder();
    if (reader) {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        bytes += value.byteLength;
        if (bytes > 32 * 1024 * 1024) {
          await reader.cancel();
          throw new ApiError(
            "RESPONSE_TOO_LARGE",
            "Response exceeds 32 MiB; select a smaller dataset.",
          );
        }
        text += decoder.decode(value, { stream: true });
      }
      text += decoder.decode();
    }
    let body: unknown;
    try {
      body = JSON.parse(text);
    } catch {
      throw new ApiError(
        "INVALID_RESPONSE",
        "Server returned invalid JSON",
        response.status,
      );
    }
    const error = (
      body as {
        error?: {
          code: string;
          message: string;
          details?: Record<string, unknown>;
        };
      }
    )?.error;
    update(id, {
      pending: false,
      status: response.status,
      duration: Math.round(performance.now() - start),
      summary: `${method} ${path} → ${response.status}${error ? " · " + error.code : summary(body)}`,
      details: JSON.stringify(bounded(body), null, 2).slice(0, 16000),
    });
    if (!response.ok)
      throw new ApiError(
        error?.code || "HTTP_ERROR",
        error?.message || response.statusText,
        response.status,
        error?.details,
      );
    return body as T;
  } catch (error) {
    if (!(error instanceof ApiError && error.status)) {
      update(id, {
        pending: false,
        status: 0,
        duration: Math.round(performance.now() - start),
        summary: `${method} ${path} · ${controller.signal.aborted ? "canceled / timed out" : "failed"}`,
        details: String(error),
      });
    }
    throw error;
  } finally {
    clearTimeout(timer);
    options.signal?.removeEventListener("abort", cancel);
  }
}
function safeParse(s: string) {
  try {
    return JSON.parse(s);
  } catch {
    return "[body omitted]";
  }
}
function summary(body: unknown) {
  if (!body || typeof body !== "object") return "";
  const b = body as Record<string, unknown>;
  return Array.isArray(b.items)
    ? ` · ${b.items.length} items`
    : b.revision !== undefined
      ? ` · revision ${b.revision}`
      : b.playback_state
        ? ` · ${b.playback_state}`
        : "";
}
export const json = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});
export const idPath = (value: string) => encodeURIComponent(value);
export async function allPages<T>(
  path: string,
  signal?: AbortSignal,
): Promise<{ items: T[]; database_id?: string; revision?: number }> {
  const out: T[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  let database: string | undefined, revision: number | undefined;
  do {
    const page = await request<List<T>>(
      path +
        (path.includes("?") ? "&" : "?") +
        "limit=1000" +
        (cursor ? "&cursor=" + encodeURIComponent(cursor) : ""),
      { signal },
    );
    if (database && page.database_id !== database)
      throw new ApiError(
        "DATABASE_CHANGED",
        "Database changed during pagination",
      );
    if (revision !== undefined && page.revision !== revision)
      throw new ApiError(
        "REVISION_CONFLICT",
        "Revision changed during pagination",
        409,
      );
    database = page.database_id;
    revision = page.revision;
    out.push(...(page.items || []));
    if (out.length > 100000)
      throw new ApiError("TOO_MANY_ROWS", "List exceeds 100,000 rows");
    cursor = page.next_cursor || undefined;
    if (cursor) {
      if (seen.has(cursor))
        throw new ApiError("CURSOR_CYCLE", "Invalid pagination cursor");
      seen.add(cursor);
    }
  } while (cursor);
  return { items: out, database_id: database, revision };
}
