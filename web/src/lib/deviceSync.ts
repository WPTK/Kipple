import { api, ApiError } from "@/api/client";
import type { DeviceView } from "@/api/types";
import { announce } from "@/shell/toasts";
import { isSchemeId } from "@/theme/schemes";
import { themeStore } from "@/theme/theme";
import { THEME_STORAGE_KEY, parseThemeSettings } from "@/theme/settings";
import type { ThemeSettings } from "@/theme/settings";
import { FONTS } from "./fonts";
import {
  DEVICE_PREFS_KEY,
  devicePrefsStore,
  parseDevicePrefs,
  replaceDevicePrefs,
  type DevicePrefs,
} from "./devicePrefs";
import { PREFS_KEY, STEPS, parsePrefs, prefsStore, touchFirst, type Prefs, type Step } from "./prefs";
import { createStore } from "./store";

// The server's device profile (docs/design.md 7.1c) is the truth for everything per device: the
// theme, the reading appearance, the layout and the other prefs that used to live in localStorage.
// localStorage stays as the instant-paint cache (the theme boot script reads it before the first
// frame), reconciled with the profile's `merged` values once the bootstrap arrives: the server wins.
//
// Local changes are diffed against what the server last confirmed and sent as ONE PATCH
// /api/device, debounced 500 ms. Because the patch is recomputed from the current local state at
// send time, a burst of changes is batched and the latest value of every key wins. A failure keeps
// the local value and shows "Couldn't save" with a Retry.

export type Profile = Record<string, unknown>;

/** Everything the profile mirrors, as the three local stores hold it. */
export interface LocalState {
  theme: ThemeSettings;
  prefs: Prefs;
  dp: DevicePrefs;
}

export const DEBOUNCE_MS = 500;
/** localStorage keys: the one-time migration flag, and the unsent changes (so a reload does not lose them). */
export const SYNC_FLAG_KEY = "kipple.deviceSync.v1";
export const SYNC_DIRTY_KEY = "kipple.deviceSync.dirty.v1";
const LEGACY_KEYS = [DEVICE_PREFS_KEY, PREFS_KEY, "kipple.theme.v1"];

const store = (): LocalState => ({ theme: themeStore.get(), prefs: prefsStore.get(), dp: devicePrefsStore.get() });

/** JSON with sorted keys: a stable comparison and fingerprint for profile values. */
export function stable(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(stable).join(",")}]`;
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    return `{${Object.keys(o)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${stable(o[k])}`)
      .join(",")}}`;
  }
  return JSON.stringify(v) ?? "null";
}

const eq = (a: unknown, b: unknown): boolean => stable(a ?? null) === stable(b ?? null);

// The server's reading_density also knows the three first-draft names; they map onto the steps.
const DENSITY_FROM_SERVER: Record<string, Step> = { compact: "snug", comfortable: "standard", relaxed: "relaxed" };
const stepFrom = (v: unknown, fallback: Step): Step =>
  typeof v === "string" ? ((STEPS as readonly string[]).includes(v) ? (v as Step) : (DENSITY_FROM_SERVER[v] ?? fallback)) : fallback;

/** The profile keys for a local state: every mapped key, null where the device has no value of its own. */
export function profileOf(l: LocalState): Profile {
  const { theme, prefs: p, dp } = l;
  const font = FONTS.find((f) => f.id === p.font)?.server ?? "";
  return {
    "ui.theme": theme.mode === "follow" ? "system" : theme.fixed,
    "ui.theme_day": theme.day,
    "ui.theme_night": theme.night,
    "ui.font_body": font,
    "ui.list_density": p.listDensity,
    "ui.reading_density": p.readingDensity,
    "ui.mark_read_on_scroll": p.markReadOnScroll,
    "client.text_size": p.textSize,
    "client.adjust_separately": p.adjustSeparately,
    "client.shortcuts": p.shortcutsChosen ? p.shortcuts : null,
    "client.spacing": p.spacing,
    "client.motion": p.motion,
    "client.large_targets": p.largeTargets,
    "client.listen": p.listen,
    "client.voice": p.voice.length <= 200 && !/[\r\n]/.test(p.voice) ? p.voice : null,
    "client.rate": p.rate,
    "client.layout": dp.layout,
    "client.layout_overrides": { feed: { ...dp.overrides.feed }, folder: { ...dp.overrides.folder } },
    "client.order": dp.order,
    "client.inbox_thumbs": dp.inboxThumbs,
    "client.peek_seen": dp.peekSeen,
    "client.article_width": dp.articleWidth,
    "client.list_width": dp.listWidth,
    "client.sidebar_width": dp.sidebarWidth,
    "client.collapsed_folders": dp.collapsedFolders.filter((id) => /^[0-9]{1,19}$/.test(id)).slice(0, 200),
    "client.link_target": dp.linkTarget,
    "client.unread_badge": dp.unreadBadge,
    "client.highlight_keywords": dp.highlightKeywords,
  };
}

/** Local state for the server's effective values `m`. Anything the server sends that this build cannot show keeps `cur`. */
export function deriveLocal(m: Profile, cur: LocalState): LocalState {
  const g = (k: string) => m[k];
  // Theme: ids come from the server, the colors and names from the local schemes file.
  const t = g("ui.theme");
  const theme: ThemeSettings = { ...cur.theme };
  if (t === "system" || t === undefined) theme.mode = "follow";
  else if (typeof t === "string" && isSchemeId(t)) {
    theme.mode = "fixed";
    theme.fixed = t;
  }
  if (isSchemeId(g("ui.theme_day"))) theme.day = g("ui.theme_day") as string;
  if (isSchemeId(g("ui.theme_night"))) theme.night = g("ui.theme_night") as string;

  const fontName = g("ui.font_body");
  const fontId = typeof fontName === "string" ? FONTS.find((f) => (f.server ?? null) === fontName)?.id : undefined;
  const has = (k: string) => m[k] !== undefined && m[k] !== null;
  const raw = {
    font: fontId ?? cur.prefs.font,
    textSize: g("client.text_size"),
    listDensity: stepFrom(g("ui.list_density"), cur.prefs.listDensity),
    readingDensity: stepFrom(g("ui.reading_density"), cur.prefs.readingDensity),
    adjustSeparately: g("client.adjust_separately"),
    shortcuts: has("client.shortcuts") ? g("client.shortcuts") : undefined,
    shortcutsChosen: has("client.shortcuts"),
    spacing: g("client.spacing"),
    motion: g("client.motion"),
    largeTargets: g("client.large_targets"),
    listen: g("client.listen"),
    voice: g("client.voice"),
    rate: g("client.rate"),
    markReadOnScroll: g("ui.mark_read_on_scroll"),
  };
  const prefs = parsePrefs(JSON.stringify(raw));
  // parsePrefs derives an unchosen shortcuts default from the device; keep that (it is not a stored choice).
  if (!raw.shortcutsChosen) {
    prefs.shortcuts = !touchFirst();
    prefs.shortcutsChosen = false;
  }
  const dpRaw = {
    layout: g("client.layout"),
    overrides: g("client.layout_overrides"),
    order: g("client.order"),
    inboxThumbs: g("client.inbox_thumbs"),
    peekSeen: g("client.peek_seen"),
    articleWidth: g("client.article_width"),
    listWidth: g("client.list_width"),
    sidebarWidth: g("client.sidebar_width"),
    collapsedFolders: g("client.collapsed_folders"),
    linkTarget: g("client.link_target"),
    unreadBadge: g("client.unread_badge"),
    highlightKeywords: g("client.highlight_keywords"),
  };
  // Only the favorites fallback (kept when the server does not accept favorites) has no profile key.
  const dp = { ...parseDevicePrefs(JSON.stringify(dpRaw)), favoritesLocal: cur.dp.favoritesLocal };
  return { theme, prefs, dp };
}

/** What a device with the server values `m` would send if it changed nothing. Comparing against it avoids spurious writes. */
export const normalize = (m: Profile, cur: LocalState): Profile => profileOf(deriveLocal(m, cur));

function applyLocal(next: LocalState): void {
  const cur = store();
  applying = true;
  try {
    if (stable(next.theme) !== stable(cur.theme)) themeStore.set(next.theme);
    if (stable(next.prefs) !== stable(cur.prefs)) prefsStore.set(next.prefs);
    if (stable(next.dp) !== stable(cur.dp)) replaceDevicePrefs(next.dp);
  } finally {
    applying = false;
  }
}

// ---- the engine ---------------------------------------------------------------------------------

export interface SyncState {
  /** off: no profile to sync with (not hydrated, or the server sent none). */
  status: "off" | "idle" | "saving" | "error";
  /** How many settings the server refused (they keep their local value). */
  refused: number;
}
export const syncStore = createStore<SyncState>({ status: "off", refused: 0 });

let enabled = false;
let applying = false;
let subscribed = false;
let hydratedFor: string | null = null;
let synced: Profile = {};
/** Keys the server refused (400), by the fingerprint of the value refused: not sent again until the value changes. */
let refused: Record<string, string> = {};
let timer: ReturnType<typeof setTimeout> | undefined;
let inflight: Promise<void> | null = null;
let again = false;

/** Refused values the user has since changed are no longer refused: only a value still refused counts. */
function pruneRefused(): void {
  const keys = Object.keys(refused);
  if (!keys.length) return;
  const want = profileOf(store());
  for (const k of keys) if (stable(want[k] ?? null) !== refused[k]) delete refused[k];
}

function setStatus(status: SyncState["status"]): void {
  pruneRefused();
  syncStore.set((s) => (s.status === status && s.refused === Object.keys(refused).length ? s : { status, refused: Object.keys(refused).length }));
}

/** Changes not yet confirmed by the server: local value against the last confirmed one, refused keys left out. */
export function pendingChanges(): Profile {
  const want = profileOf(store());
  const out: Profile = {};
  for (const k of Object.keys(want)) {
    const w = want[k] ?? null;
    if (eq(w, synced[k])) continue;
    if (refused[k] !== undefined && refused[k] === stable(w)) continue;
    out[k] = w;
  }
  return out;
}

function persistDirty(d: Profile): void {
  try {
    if (Object.keys(d).length) localStorage.setItem(SYNC_DIRTY_KEY, JSON.stringify(d));
    else localStorage.removeItem(SYNC_DIRTY_KEY);
  } catch {
    /* not persisted */
  }
}

function readDirty(): Profile {
  try {
    const v = JSON.parse(localStorage.getItem(SYNC_DIRTY_KEY) ?? "null") as unknown;
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Profile) : {};
  } catch {
    return {};
  }
}

function onLocalChange(): void {
  if (!enabled || applying) return;
  pruneRefused();
  persistDirty(pendingChanges());
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => void flush(), DEBOUNCE_MS);
}

/** Send what is pending now (no debounce). Resolves when the server has answered. */
export function flush(): Promise<void> {
  if (timer) clearTimeout(timer);
  timer = undefined;
  if (!enabled) return Promise.resolve();
  if (inflight) {
    again = true;
    return inflight;
  }
  const patch = pendingChanges();
  if (Object.keys(patch).length === 0) {
    persistDirty({});
    setStatus("idle");
    return Promise.resolve();
  }
  setStatus("saving");
  const run = (async () => {
    let retryRest = false;
    try {
      const res = await api<DeviceView>("/api/device", { method: "PATCH", body: patch });
      reconcile(patch, res.merged);
      setStatus("idle");
    } catch (e) {
      const keys = e instanceof ApiError && e.status === 400 && Array.isArray(e.body?.keys) ? (e.body.keys as string[]) : null;
      if (keys && keys.length) {
        // The server refused these values: they stay as they are on this device, and the rest is sent again.
        for (const k of keys) if (k in patch) refused[k] = stable(patch[k]);
        retryRest = true;
      } else {
        if (syncStore.get().status !== "error") announce("Couldn't save your settings");
        setStatus("error");
      }
    } finally {
      inflight = null;
    }
    const more = again || retryRest;
    again = false;
    if (more) await flush();
    else {
      const left = pendingChanges();
      persistDirty(left);
      // Changed while the request was in flight: send that too, unless the failure is waiting on a Retry.
      if (Object.keys(left).length && syncStore.get().status === "idle") timer = setTimeout(() => void flush(), DEBOUNCE_MS);
    }
  })();
  inflight = run;
  return run;
}

/**
 * After a PATCH: the sent keys are confirmed. Every other key the server now holds differently was changed
 * elsewhere (another tab or device): the server wins there, unless the user changed that key here since it was
 * last confirmed. Never send a value this tab merely never saw back to the server.
 */
function reconcile(patch: Profile, merged: Profile): void {
  const cur = store();
  const local = profileOf(cur);
  const m = normalize(merged, cur);
  const next: Profile = { ...synced };
  const take: Profile = {};
  for (const k of Object.keys(m)) {
    if (k in patch) {
      next[k] = m[k];
      continue;
    }
    if (eq(m[k], synced[k])) continue;
    if (eq(local[k], synced[k])) {
      take[k] = m[k];
      next[k] = m[k];
    }
    // else: changed here meanwhile; it stays pending against the old confirmed value
  }
  synced = next;
  if (Object.keys(take).length) applyLocal(deriveLocal({ ...local, ...take }, cur));
}

/** The Retry button: send what is still pending. Refused values are not sent again (Discard drops them). */
export function retrySave(): Promise<void> {
  return flush();
}

/** The Discard button: put the server's value back for every refused setting. */
export function discardRefused(): void {
  const keys = Object.keys(refused);
  if (!keys.length) return;
  const cur = store();
  const back = profileOf(cur);
  for (const k of keys) back[k] = synced[k] ?? null;
  refused = {};
  applyLocal(deriveLocal(back, cur));
  const left = pendingChanges();
  persistDirty(left);
  setStatus(Object.keys(left).length === 0 ? "idle" : syncStore.get().status);
}

/** Another tab wrote the local cache: follow it (the stores have no other cross-tab link). */
function onStorage(e: StorageEvent): void {
  if (e.newValue === null) return;
  if (e.key === PREFS_KEY) {
    const n = parsePrefs(e.newValue);
    if (stable(n) !== stable(prefsStore.get())) prefsStore.set(n);
  } else if (e.key === DEVICE_PREFS_KEY) {
    const n = parseDevicePrefs(e.newValue);
    if (stable(n) !== stable(devicePrefsStore.get())) replaceDevicePrefs(n);
  } else if (e.key === THEME_STORAGE_KEY) {
    const n = parseThemeSettings(e.newValue);
    if (stable(n) !== stable(themeStore.get())) themeStore.set(n);
  }
}

function ensureSubscribed(): void {
  if (subscribed) return;
  subscribed = true;
  themeStore.subscribe(onLocalChange);
  prefsStore.subscribe(onLocalChange);
  devicePrefsStore.subscribe(onLocalChange);
  try {
    window.addEventListener("storage", onStorage);
    window.addEventListener("online", () => {
      if (syncStore.get().status === "error") void flush();
    });
  } catch {
    /* no window */
  }
}

/** Which profile keys the pre-sync localStorage caches actually held (a key they never stored is not a choice). */
function legacyProfileKeys(): Set<string> {
  const out = new Set<string>();
  const read = (key: string): Record<string, unknown> | null => {
    try {
      const raw = localStorage.getItem(key);
      const v = raw === null ? null : (JSON.parse(raw) as unknown);
      return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
    } catch {
      return null;
    }
  };
  const add = (o: Record<string, unknown> | null, map: Record<string, string[]>) => {
    if (o) for (const f of Object.keys(o)) for (const k of map[f] ?? []) out.add(k);
  };
  add(read("kipple.theme.v1"), { mode: ["ui.theme"], fixed: ["ui.theme"], day: ["ui.theme_day"], night: ["ui.theme_night"] });
  const p = read(PREFS_KEY);
  add(p, {
    font: ["ui.font_body"], textSize: ["client.text_size"], listDensity: ["ui.list_density"], readingDensity: ["ui.reading_density"],
    adjustSeparately: ["client.adjust_separately"], spacing: ["client.spacing"], motion: ["client.motion"],
    largeTargets: ["client.large_targets"], listen: ["client.listen"], voice: ["client.voice"], rate: ["client.rate"],
  });
  // Never held before F4, so only a true value is a choice; false is just the field's default.
  if (p?.markReadOnScroll === true) out.add("ui.mark_read_on_scroll");
  if (p && parsePrefs(JSON.stringify(p)).shortcutsChosen) out.add("client.shortcuts");
  add(read(DEVICE_PREFS_KEY), {
    layout: ["client.layout"], overrides: ["client.layout_overrides"], order: ["client.order"], inboxThumbs: ["client.inbox_thumbs"],
    peekSeen: ["client.peek_seen"], articleWidth: ["client.article_width"], listWidth: ["client.list_width"],
    sidebarWidth: ["client.sidebar_width"], collapsedFolders: ["client.collapsed_folders"], linkTarget: ["client.link_target"],
    unreadBadge: ["client.unread_badge"], highlightKeywords: ["client.highlight_keywords"],
  });
  return out;
}

const hasLegacy = (): boolean => {
  try {
    return LEGACY_KEYS.some((k) => localStorage.getItem(k) !== null);
  } catch {
    return false;
  }
};

/**
 * Called once per page load when the bootstrap carries the device. First run on this browser with an
 * empty server profile and old localStorage values: they are sent up once (migration). Otherwise the
 * server wins and its effective values replace the local cache; changes that were made but never sent
 * (a reload inside the debounce, or a failed save) are put back on top and sent.
 */
export function hydrateDevice(device: DeviceView | undefined): void {
  if (!device || !device.merged) return;
  if (hydratedFor === device.id) return;
  hydratedFor = device.id;
  ensureSubscribed();
  const cur = store();
  let done = false;
  try {
    done = localStorage.getItem(SYNC_FLAG_KEY) === "1";
  } catch {
    /* treat as not done */
  }
  const emptyProfile = Object.keys(device.profile ?? {}).length === 0;
  synced = normalize(device.merged, cur);
  refused = {};
  if (!done && emptyProfile && hasLegacy()) {
    // Migrate: the values the old caches held become the profile. Every other key takes the server's
    // effective value (an account-wide setting such as mark-read-on-scroll must not become a device override).
    const held = legacyProfileKeys();
    const want = profileOf(cur);
    for (const k of Object.keys(want)) if (!held.has(k)) want[k] = synced[k] ?? null;
    applyLocal(deriveLocal(want, cur));
    enabled = true;
    setStatus("idle");
    try {
      localStorage.setItem(SYNC_FLAG_KEY, "1");
    } catch {
      /* the next load tries again, which is harmless */
    }
    if (Object.keys(pendingChanges()).length) {
      persistDirty(pendingChanges());
      void flush();
    }
    return;
  }
  try {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
  } catch {
    /* ignore */
  }
  enabled = false;
  // Unsent local changes win over the server's older value for the same key.
  const dirty = readDirty();
  applyLocal(deriveLocal({ ...device.merged, ...dirty }, cur));
  enabled = true;
  setStatus("idle");
  const left = pendingChanges();
  persistDirty(left);
  if (Object.keys(left).length) void flush();
}

/** The server's answer to a copy, reset or rename: adopt its effective values and forget anything pending. */
export function adoptDevice(device: DeviceView): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  again = false;
  refused = {};
  const cur = store();
  enabled = false;
  applyLocal(deriveLocal(device.merged, cur));
  synced = normalize(device.merged, store());
  enabled = true;
  hydratedFor = device.id;
  persistDirty({});
  setStatus("idle");
}

/** Copy another device's settings here, or pass "defaults" to clear this device's own choices. */
export async function copySettingsFrom(id: string | "defaults"): Promise<DeviceView> {
  if (timer) clearTimeout(timer);
  timer = undefined;
  if (inflight) await inflight;
  const res = await api<DeviceView>(`/api/device/copy-from/${encodeURIComponent(id)}`, { method: "POST" });
  adoptDevice(res);
  return res;
}

/** Make this device's settings the default for devices that have none of their own yet. Unsent changes go first. */
export async function makeThisDeviceDefault(): Promise<void> {
  await flush();
  await api<DeviceView>("/api/device/make-default", { method: "POST" });
}

/** Tests: forget everything (a fresh page load). */
export function resetDeviceSync(): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  enabled = false;
  hydratedFor = null;
  synced = {};
  refused = {};
  inflight = null;
  again = false;
  syncStore.set({ status: "off", refused: 0 });
}
