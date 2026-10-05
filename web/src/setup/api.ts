// The setup wizard's calls (docs/design.md §7.1e) and the plain-English wording of what they
// can answer. Nothing here knows about React.
import { ApiError, api } from "@/api/client";

/** Why open mode (no password) is refused for a request. */
export type OpenReason = "host" | "peer" | "forwarded";

/** What the account form can offer from where this browser is (GET /api/instance, while Kipple has no account). */
export interface SetupOptions {
  access: { enabled: boolean; verified: boolean };
  open: {
    /** Null: open mode works from here. */
    reason: OpenReason | null;
  };
}

/** GET /api/instance: the one fact the signed-out app needs to pick its first screen. */
export type InstanceInfo = ({ setup: true; auth: null } & SetupOptions) | { setup: false; auth: null | "password" | "access" | "open" };

export type Passwordless = "access" | "open";

export interface AccountBody {
  username: string;
  password?: string;
  passwordless?: Passwordless;
  acknowledge_open?: boolean;
}

export interface StarterFeed {
  id: string;
  title: string;
  url: string;
  site?: string;
  description?: string;
  checked: boolean;
  lang?: string;
  subscribed: boolean;
}

export interface StarterCategory {
  id: string;
  title: string;
  feeds: StarterFeed[];
}

export interface StarterList {
  available: boolean;
  categories: StarterCategory[];
}

/** The query key of GET /api/instance. It starts with "auth" so the app's sign-out cleanup (App.tsx) leaves it alone. */
export const INSTANCE_KEY = ["auth", "instance"] as const;
export const fetchInstance = (signal?: AbortSignal) => api<InstanceInfo>("/api/instance", { signal, anon: true, quiet: true });
/** Creates the account (the first request wins; there is no code) and signs this browser in: a success turns the app to signed in. */
export const createAccount = (body: AccountBody) => api<{ username: string; auth_mode: string }>("/api/setup/account", { method: "POST", body, anon: true, signsIn: true });
/** Open-mode sign-in. */
export const signInOpen = () => api("/api/auth/open", { method: "POST", anon: true, signsIn: true });
export const completeOnboarding = () => api("/api/onboarding/complete", { method: "POST" });
export const restartOnboarding = () => api("/api/onboarding/restart", { method: "POST" });
export const fetchStarterFeeds = (signal?: AbortSignal) => api<StarterList>("/api/starter-feeds", { signal });
export const subscribeStarter = (ids: string[], folders: boolean) =>
  api<{ added: number; existing: number; run_id: string | null }>("/api/starter-feeds", { method: "POST", body: { ids, folders } });

/** Plain-English reason open mode is refused, and what to do about it. */
export function openReasonText(reason: OpenReason | string | null | undefined): string {
  switch (reason) {
    case "host":
      return "Without a password Kipple only answers its IP address (for example http://192.168.1.20:1919), localhost, a Tailscale name and the names you allowed. To allow the name in this page's address, add it to KIPPLE_ALLOWED_HOSTS (for example KIPPLE_ALLOWED_HOSTS=nas.local) and restart Kipple, or open Kipple by its IP address.";
    case "forwarded":
      return "This page reached Kipple through a proxy or tunnel (such as a Cloudflare Tunnel). Without a password that would let anyone on the internet in, so it isn't allowed. Open Kipple directly from this computer, your local network or over Tailscale, or use a password.";
    case "peer":
      return "You are connecting from a device that isn't this computer, on your local network or on your Tailscale network. Without a password only those are allowed. Use a password to reach Kipple from anywhere else.";
    default:
      return "Kipple only allows no-password sign-in from this computer, your local network or over Tailscale. Open it from one of those, or use a password.";
  }
}

/** The message of an open_refused answer: the reason in plain English, never the server's developer wording. */
export function openRefusedText(e: unknown): string | null {
  if (e instanceof ApiError && e.code === "open_refused") {
    return openReasonText(typeof e.body?.reason === "string" ? e.body.reason : null);
  }
  return null;
}

/** The reason of an open_refused answer, or null when the error is something else. */
export function openRefusedReason(e: unknown): OpenReason | null {
  if (e instanceof ApiError && e.code === "open_refused") {
    const r = e.body?.reason;
    return r === "host" || r === "peer" || r === "forwarded" ? r : null;
  }
  return null;
}

/** Whether an error is the open gate refusing a request that was already signed in (any route). */
export const isOpenRefused = (e: unknown): boolean => e instanceof ApiError && e.status === 403 && e.code === "open_refused";

export type AccountFailure = { kind: "field"; field: "username" | "password" | "open"; message: string } | { kind: "form"; message: string } | { kind: "done"; message: string };

/** What a failed account creation means for the form. */
export function accountFailure(e: unknown): AccountFailure {
  if (e instanceof ApiError) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    if (e.status === 0) return { kind: "form", message: "Kipple couldn't reach the server." };
    if (e.code === "bad_username") return { kind: "field", field: "username", message: "The user name can be 1 to 64 letters, digits, dots, dashes or underscores." };
    if (e.code === "bad_new_password") return { kind: "field", field: "password", message: msg ? capital(msg) + "." : "The password must be 5 to 256 characters." };
    if (e.code === "ack_required") return { kind: "field", field: "open", message: "Tick the box to confirm you understand." };
    if (e.code === "access_required")
      return { kind: "form", message: "Signing in without a password through Cloudflare Access needs Access to be set up on the server and Kipple opened through it. Choose another option, or open Kipple through its Access address." };
    if (e.code === "access_unavailable") return { kind: "form", message: "Kipple can't check your Cloudflare Access sign-in right now. Try again in a moment." };
    if (e.code === "open_refused") return { kind: "form", message: openReasonText(typeof e.body?.reason === "string" ? e.body.reason : null) };
    if (e.code === "already_set_up") return { kind: "done", message: "Kipple was set up a moment ago by someone else. Reload the page to sign in." };
    if (e.status === 404) return { kind: "done", message: "Kipple is already set up. Reload the page to sign in." };
    if (e.code === "origin") return { kind: "form", message: "Your browser sent this from a different address than the page came from. Open Kipple at the address you use to reach it and try again." };
  }
  return { kind: "form", message: "Something went wrong. Try again." };
}

function capital(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The bytes a password takes (the server counts bytes, not characters). */
export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

/** Why a password won't do, or null. The same rules as the server: 5 to 256 bytes, not the example value. */
export function passwordProblem(pw: string): string | null {
  if (pw === "change-me") return "That is the example password from the setup files. Choose your own.";
  const n = byteLength(pw);
  if (n < 5) return "Use at least 5 characters.";
  if (n > 256) return "That's too long. Use at most 256 characters.";
  return null;
}
