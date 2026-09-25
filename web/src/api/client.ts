import { createStore } from "@/lib/store";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** The parsed JSON error body, when there was one (settings 400s carry `keys` and `issues`). */
  readonly body: Record<string, unknown> | null;
  constructor(status: number, code: string, body: Record<string, unknown> | null = null) {
    super(`${status} ${code}`);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

/** Whether the session is signed in. `unknown` until the first API answer. */
export type AuthState = "unknown" | "in" | "out";
export const authStore = createStore<AuthState>("unknown");

/** X-Kipple-Client doubles as the stats attribution: pwa when installed. */
export function clientKind(): "web" | "pwa" {
  try {
    const nav = navigator as Navigator & { standalone?: boolean };
    if (nav.standalone === true || window.matchMedia("(display-mode: standalone)").matches) return "pwa";
  } catch {
    /* fall through */
  }
  return "web";
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  params?: Record<string, string | number | undefined | null>;
  signal?: AbortSignal;
}

export function buildPath(path: string, params?: RequestOptions["params"]): string {
  if (!params) return path;
  const usp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") usp.set(k, String(v));
  }
  const qs = usp.toString();
  return qs ? `${path}?${qs}` : path;
}

/**
 * Same-origin JSON fetch. Adds X-Kipple-Client (required on every write, see
 * design section 7), returns parsed JSON (undefined for 204) and throws
 * ApiError. A 401 also flips authStore to "out", which shows the login screen.
 */
export async function api<T = void>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json", "X-Kipple-Client": clientKind() };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  let res: Response;
  try {
    res = await fetch(buildPath(path, opts.params), {
      method,
      headers,
      body,
      credentials: "same-origin",
      signal: opts.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError(0, "network");
  }
  if (res.status === 401) {
    authStore.set("out");
    throw new ApiError(401, "auth");
  }
  if (!res.ok) {
    let code = "http_" + res.status;
    let body: Record<string, unknown> | null = null;
    try {
      body = (await res.json()) as Record<string, unknown>;
      if (typeof body.error === "string") code = body.error;
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, code, body);
  }
  if (authStore.get() !== "in") authStore.set("in");
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** Human message for an error toast. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 0) return "Kipple couldn't reach the server.";
    if (e.status === 429) return "Too many attempts. Try again in a few minutes.";
    if (e.status >= 500) return "The server returned an error. Try again.";
    if (e.status === 404) return "That item is no longer available.";
    return "Something went wrong. Try again.";
  }
  return "Something went wrong. Try again.";
}
