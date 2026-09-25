import { createStore, useStore } from "./store";

// Per-device layout, ordering and first-run flags. Everything device-scoped that is
// not appearance lives here, behind ONE storage seam (`storage` below). Today it is
// localStorage; when the server-side per-device profile API lands (design 7.x
// `devices`), only `storage.load/save` change.

export const LAYOUT_IDS = ["magazine", "cards", "compact", "inbox", "headlines"] as const;
export type LayoutId = (typeof LAYOUT_IDS)[number];
export const LAYOUT_LABELS: Record<LayoutId, string> = {
  magazine: "Magazine",
  cards: "Cards",
  compact: "Compact",
  inbox: "Inbox",
  headlines: "Headlines",
};

export type OrderPref = "newest" | "oldest";

export interface DevicePrefs {
  /** Device default layout. Magazine unless changed. */
  layout: LayoutId;
  /** Per-feed and per-folder overrides, resolved feed > folder > device default. */
  overrides: { feed: Record<string, LayoutId>; folder: Record<string, LayoutId> };
  order: OrderPref;
  /** Inbox trailing thumbnails: Auto shows them when the item has an image. */
  inboxThumbs: "auto" | "off";
  /** The first-run swipe peek has played (or been dismissed) on this device. */
  peekSeen: boolean;
}

export const DEFAULT_DEVICE_PREFS: DevicePrefs = {
  layout: "magazine",
  overrides: { feed: {}, folder: {} },
  order: "newest",
  inboxThumbs: "auto",
  peekSeen: false,
};

export const DEVICE_PREFS_KEY = "kipple.device.v1";

export const isLayoutId = (v: unknown): v is LayoutId => LAYOUT_IDS.includes(v as LayoutId);

function cleanMap(v: unknown): Record<string, LayoutId> {
  const out: Record<string, LayoutId> = {};
  if (v && typeof v === "object") {
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) if (isLayoutId(val)) out[k] = val;
  }
  return out;
}

export function parseDevicePrefs(raw: string | null): DevicePrefs {
  const d = DEFAULT_DEVICE_PREFS;
  if (!raw) return { ...d, overrides: { feed: {}, folder: {} } };
  try {
    const v = JSON.parse(raw) as Partial<DevicePrefs> | null;
    return {
      layout: isLayoutId(v?.layout) ? v.layout : d.layout,
      overrides: { feed: cleanMap(v?.overrides?.feed), folder: cleanMap(v?.overrides?.folder) },
      order: v?.order === "oldest" ? "oldest" : "newest",
      inboxThumbs: v?.inboxThumbs === "off" ? "off" : "auto",
      peekSeen: v?.peekSeen === true,
    };
  } catch {
    return { ...d, overrides: { feed: {}, folder: {} } };
  }
}

/** The only place that knows where device prefs live. */
const storage = {
  load(): DevicePrefs {
    try {
      return parseDevicePrefs(localStorage.getItem(DEVICE_PREFS_KEY));
    } catch {
      return parseDevicePrefs(null);
    }
  },
  save(p: DevicePrefs): void {
    try {
      localStorage.setItem(DEVICE_PREFS_KEY, JSON.stringify(p));
    } catch {
      /* not persisted */
    }
  },
};

export const devicePrefsStore = createStore<DevicePrefs>(storage.load());

export function useDevicePrefs(): DevicePrefs {
  return useStore(devicePrefsStore);
}

export function updateDevicePrefs(patch: Partial<DevicePrefs>): void {
  devicePrefsStore.set((p) => {
    const next = { ...p, ...patch };
    storage.save(next);
    return next;
  });
}

/** Test helper: back to factory defaults. */
export function resetDevicePrefs(): void {
  devicePrefsStore.set(parseDevicePrefs(null));
  sessionLayoutStore.set(null);
}

/** Set (or with null clear) the layout override for one feed or folder. */
export function setLayoutOverride(kind: "feed" | "folder", id: string, layout: LayoutId | null): void {
  devicePrefsStore.set((p) => {
    const map = { ...p.overrides[kind] };
    if (layout) map[id] = layout;
    else delete map[id];
    const next = { ...p, overrides: { ...p.overrides, [kind]: map } };
    storage.save(next);
    return next;
  });
}

/** Transient layout used by the `c` key (not persisted, cleared by any explicit choice). */
export const sessionLayoutStore = createStore<LayoutId | null>(null);

export interface LayoutContext {
  feedId?: string;
  folderId?: string;
}

/** Resolve the layout for a list: session toggle, then feed, then folder, then the device default. */
export function resolveLayout(p: DevicePrefs, ctx: LayoutContext, session: LayoutId | null = null): LayoutId {
  if (session) return session;
  if (ctx.feedId && p.overrides.feed[ctx.feedId]) return p.overrides.feed[ctx.feedId] as LayoutId;
  if (ctx.folderId && p.overrides.folder[ctx.folderId]) return p.overrides.folder[ctx.folderId] as LayoutId;
  return p.layout;
}

/** Which override a list can carry, if any (a feed list or a folder list). */
export function overrideTarget(ctx: LayoutContext): { kind: "feed" | "folder"; id: string } | null {
  if (ctx.feedId) return { kind: "feed", id: ctx.feedId };
  if (ctx.folderId) return { kind: "folder", id: ctx.folderId };
  return null;
}
