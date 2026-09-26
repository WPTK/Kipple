import { createStore } from "./store";

/**
 * What the app knows about the connection and its own freshness. Kept apart from lib/offline.ts (which
 * calls the API) so api/client.ts can update it without an import cycle.
 */
export interface OfflineState {
  /** navigator.onLine, corrected by what requests actually did: a network failure sets it false, any answer true. */
  online: boolean;
  /** Changes made offline that wait for the server (lib/offline.ts holds them). */
  pending: number;
  /** The server speaks a newer web API than this page was built for, or a new build is waiting: reload. */
  updateReady: boolean;
  /**
   * The sign-in in front of Kipple (an access proxy such as Cloudflare Access) expired: API calls are answered with
   * a redirect to its login page. Only a full reload can sign in again. Not offline: nothing is queued meanwhile.
   */
  sessionExpired: boolean;
}

export const offlineStore = createStore<OfflineState>({
  online: typeof navigator === "undefined" ? true : navigator.onLine !== false,
  pending: 0,
  updateReady: false,
  sessionExpired: false,
});

export function setSessionExpired(expired = true): void {
  offlineStore.set((s) => (s.sessionExpired === expired ? s : { ...s, sessionExpired: expired }));
}

export function setOnline(online: boolean): void {
  offlineStore.set((s) => (s.online === online ? s : { ...s, online }));
}

export function setPending(pending: number): void {
  offlineStore.set((s) => (s.pending === pending ? s : { ...s, pending }));
}

export function setUpdateReady(): void {
  offlineStore.set((s) => (s.updateReady ? s : { ...s, updateReady: true }));
}

/** The web API contract this build speaks; the server announces its own in X-Kipple-API (internal/httpx). */
export const API_VERSION = 1;

/** Read the handshake headers of an /api response. */
export function noteResponse(res: Pick<Response, "headers">, quiet = false): void {
  // A copy the service worker served while the network was down, or too slow, proves nothing either way; a
  // background request (the prefetch) never says the app is offline.
  if (res.headers.get("X-Kipple-Cache")) {
    if (!quiet) setOnline(false);
    return;
  }
  setOnline(true);
  const server = Number(res.headers.get("X-Kipple-API"));
  if (Number.isFinite(server) && server > API_VERSION) setUpdateReady();
}
