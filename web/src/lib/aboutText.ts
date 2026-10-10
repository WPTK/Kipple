import type { About } from "@/api/about";

/** What only this browser knows. */
export interface ClientFacts {
  bundleVersion: string;
  bundleBuild: string;
  /** The newest shell cache of the service worker, "none" without one. */
  workerShell: string;
  standalone: boolean;
  userAgent: string;
}

/** "3 h 12 min", "4 d 2 h", "45 s": the two biggest units that are not zero. */
export function uptimeText(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} min`;
  if (m > 0) return `${m} min`;
  return `${s} s`;
}

/** "Oct 1, 2026, 9:00 AM EDT": the start time in this browser\'s own time zone (the server reports UTC). */
export function startedText(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZoneName: "short" });
}

const yes = (b: boolean) => (b ? "yes" : "no");

const AUTH_LABELS: Record<About["auth_mode"], string> = {
  password: "password",
  access: "Cloudflare Access only (no web password)",
  open: "open (no password)",
};

/** The rows the About screen shows and the debug text repeats, in order. */
export function aboutRows(a: About, c: ClientFacts): { label: string; value: string }[] {
  return [
    { label: "Version", value: a.version },
    { label: "Commit", value: a.commit },
    { label: "Built", value: a.build_date },
    { label: "Go", value: `${a.go_version} (${a.os_arch})` },
    { label: "Database schema", value: a.schema_version === a.schema_latest ? String(a.schema_version) : `${a.schema_version} (this build expects ${a.schema_latest})` },
    { label: "SQLite", value: a.sqlite_version || "unknown" },
    { label: "Running for", value: `${uptimeText(a.uptime_s)} (since ${startedText(a.started_at)})` },
    { label: "Time zone", value: a.tz },
    { label: "Data folder writable", value: yes(a.data_dir_writable) },
    { label: "Sign-in", value: AUTH_LABELS[a.auth_mode] ?? a.auth_mode },
    { label: "Cloudflare Access validation", value: a.access_enabled ? "on" : "off" },
    { label: "Public URL set", value: yes(a.public_url_set) },
    { label: "Web build on the server", value: a.web_build || "unknown" },
    { label: "Web build in this page", value: `${c.bundleVersion} (${c.bundleBuild})` },
    { label: "Service worker", value: c.workerShell },
    { label: "Opened as", value: c.standalone ? "installed app" : "browser tab" },
    { label: "Browser", value: c.userAgent },
  ];
}

/** The plain-text block for a bug report. Nothing in it identifies the person or where Kipple runs. */
export function debugText(a: About, c: ClientFacts): string {
  return ["Kipple debug info", ...aboutRows(a, c).map((r) => `${r.label}: ${r.value}`)].join("\n");
}

/** The browser-side facts, read once when the About screen opens. Never throws. */
export async function readClientFacts(bundle: { version: string; build: string }): Promise<ClientFacts> {
  let workerShell = "none";
  try {
    const shells = (await caches.keys()).filter((k) => k.startsWith("kipple-shell-")).sort();
    const newest = shells[shells.length - 1];
    if (newest) workerShell = newest.slice("kipple-shell-".length);
  } catch {
    // no Cache Storage here (an insecure origin, a private window): "none"
  }
  let standalone = false;
  try {
    standalone = window.matchMedia("(display-mode: standalone)").matches || (navigator as { standalone?: boolean }).standalone === true;
  } catch {
    // matchMedia is missing in some test environments
  }
  return { bundleVersion: bundle.version, bundleBuild: bundle.build, workerShell, standalone, userAgent: navigator.userAgent };
}
