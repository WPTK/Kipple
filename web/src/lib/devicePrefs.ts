import { createStore, useStore } from "./store";

// Per-device layout, ordering and first-run flags. Everything device-scoped that is
// not appearance lives here, behind ONE storage seam (`storage` below): localStorage is the
// instant-paint cache of the server's device profile, which lib/deviceSync.ts keeps in step.

export const LAYOUT_IDS = ["magazine", "cards", "compact", "inbox", "headlines"] as const;
export type LayoutId = (typeof LAYOUT_IDS)[number];
// The ids are what devices have stored, so they never change; only the labels do. "magazine" is shown as
// Editorial and "headlines" as Email - Compact (title-only rows in the email style).
export const LAYOUT_LABELS: Record<LayoutId, string> = {
  magazine: "Editorial",
  cards: "Cards",
  compact: "Compact",
  inbox: "Inbox",
  headlines: "Email - Compact",
};

/** One-line description of each layout, shown beside it in the layout menu and settings. */
export const LAYOUT_HINTS: Record<LayoutId, string> = {
  magazine: "Big lead image, large title and excerpt",
  cards: "A grid of picture cards",
  compact: "Small source line over the title, no pictures",
  inbox: "Email rows: sender, subject, snippet, time",
  headlines: "One line per article, titles only",
};

export const ARTICLE_WIDTHS = ["narrow", "medium", "wide", "full"] as const;
export type ArticleWidth = (typeof ARTICLE_WIDTHS)[number];
export const ARTICLE_WIDTH_LABELS: Record<ArticleWidth, string> = { narrow: "Narrow", medium: "Medium", wide: "Wide", full: "Full" };
/** Max width of the article column (rem); Full uses the whole pane. */
export const ARTICLE_WIDTH_REM: Record<ArticleWidth, number | null> = { narrow: 34, medium: 46, wide: 62, full: null };

export type LinkTarget = "new" | "same";
export type UnreadBadge = "count" | "dot" | "off";
export const UNREAD_BADGES: readonly UnreadBadge[] = ["count", "dot", "off"];

/** A pinned sidebar entry (matches the server's `library.favorites`). */
export interface Favorite {
  t: "folder" | "feed";
  id: string;
}

/** Bounds of the resizable list column (px), on a wide screen. */
export const LIST_WIDTH_MIN = 260;
export const LIST_WIDTH_MAX = 720;

export type OrderPref = "newest" | "oldest";

/** The Search screen's ordering. The ids are the URL/saved-search ones; the profile key `client.search_order`
 * (docs/design.md 7.1c) calls them relevance, newest and oldest. */
export const SEARCH_ORDERS = ["rank", "date", "oldest"] as const;
export type SearchOrder = (typeof SEARCH_ORDERS)[number];
export const isSearchOrder = (v: unknown): v is SearchOrder => (SEARCH_ORDERS as readonly unknown[]).includes(v);
export const SEARCH_ORDER_TO_SERVER: Record<SearchOrder, string> = { rank: "relevance", date: "newest", oldest: "oldest" };
export const searchOrderFromServer = (v: unknown): SearchOrder | undefined =>
  v === "relevance" ? "rank" : v === "newest" ? "date" : v === "oldest" ? "oldest" : undefined;
/** Where this ordering lived before it joined the device profile (one-time migration source). */
export const LEGACY_SEARCH_ORDER_KEY = "kipple.searchOrder.v1";

export interface DevicePrefs {
  /** Device default layout. Magazine unless changed. */
  layout: LayoutId;
  /** Per-feed and per-folder overrides, resolved feed > folder > device default. */
  overrides: { feed: Record<string, LayoutId>; folder: Record<string, LayoutId> };
  order: OrderPref;
  /** Result ordering of the Search screen (server key `client.search_order`). */
  searchOrder: SearchOrder;
  /** Inbox trailing thumbnails: Auto shows them when the item has an image. */
  inboxThumbs: "auto" | "off";
  /** The first-run swipe peek has played (or been dismissed) on this device. */
  peekSeen: boolean;
  /** Width of the article column in the reader pane and the full-screen article. */
  articleWidth: ArticleWidth;
  /** Width in px of the list column beside the reader pane; null follows the layout's own width. */
  listWidth: number | null;
  /** Width in px of the sidebar. */
  sidebarWidth: number;
  /** Folders collapsed in the sidebar (folder ids). */
  collapsedFolders: string[];
  /** Where external links open. null: the device default (same tab on iOS and iPadOS, a new tab elsewhere). */
  linkTarget: LinkTarget | null;
  /** The unread badge on the tab bar and sidebar. */
  unreadBadge: UnreadBadge;
  /** Favorites kept on this device when the server does not accept them. */
  favoritesLocal: Favorite[];
  /** Draw the words of Highlight filters in lists and articles (the reading menu's "Highlight keywords"). */
  highlightKeywords: boolean;
}

export const SIDEBAR_WIDTH_MIN = 200;
export const SIDEBAR_WIDTH_MAX = 420;

export const DEFAULT_DEVICE_PREFS: DevicePrefs = {
  layout: "magazine",
  overrides: { feed: {}, folder: {} },
  order: "newest",
  searchOrder: "rank",
  inboxThumbs: "auto",
  peekSeen: false,
  articleWidth: "medium",
  listWidth: null,
  sidebarWidth: 240,
  collapsedFolders: [],
  linkTarget: null,
  unreadBadge: "count",
  favoritesLocal: [],
  highlightKeywords: true,
};

export const DEVICE_PREFS_KEY = "kipple.device.v1";

export const isLayoutId = (v: unknown): v is LayoutId => LAYOUT_IDS.includes(v as LayoutId);

export const clampNum = (n: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, Math.round(n)));

/** A well-formed favorites list: known kinds, digit ids, no repeats, at most 500. */
export function cleanFavorites(v: unknown): Favorite[] {
  if (!Array.isArray(v)) return [];
  const seen = new Set<string>();
  const out: Favorite[] = [];
  for (const x of v as unknown[]) {
    const o = x as Partial<Favorite> | null;
    if (!o || (o.t !== "folder" && o.t !== "feed") || typeof o.id !== "string" || !/^[0-9]{1,19}$/.test(o.id)) continue;
    const k = o.t + ":" + o.id;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push({ t: o.t, id: o.id });
    if (out.length >= 500) break;
  }
  return out;
}

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
      searchOrder: isSearchOrder(v?.searchOrder) ? v.searchOrder : d.searchOrder,
      inboxThumbs: v?.inboxThumbs === "off" ? "off" : "auto",
      peekSeen: v?.peekSeen === true,
      articleWidth: ARTICLE_WIDTHS.includes(v?.articleWidth as ArticleWidth) ? (v?.articleWidth as ArticleWidth) : d.articleWidth,
      listWidth: typeof v?.listWidth === "number" && Number.isFinite(v.listWidth) ? clampNum(v.listWidth, LIST_WIDTH_MIN, LIST_WIDTH_MAX) : null,
      sidebarWidth:
        typeof v?.sidebarWidth === "number" && Number.isFinite(v.sidebarWidth) ? clampNum(v.sidebarWidth, SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX) : d.sidebarWidth,
      collapsedFolders: Array.isArray(v?.collapsedFolders) ? v.collapsedFolders.filter((x): x is string => typeof x === "string") : [],
      linkTarget: v?.linkTarget === "new" || v?.linkTarget === "same" ? v.linkTarget : null,
      unreadBadge: UNREAD_BADGES.includes(v?.unreadBadge as UnreadBadge) ? (v?.unreadBadge as UnreadBadge) : d.unreadBadge,
      favoritesLocal: cleanFavorites(v?.favoritesLocal),
      highlightKeywords: v?.highlightKeywords !== false,
    };
  } catch {
    return { ...d, overrides: { feed: {}, folder: {} } };
  }
}

/** The only place that knows where device prefs live. */
const storage = {
  load(): DevicePrefs {
    try {
      const raw = localStorage.getItem(DEVICE_PREFS_KEY);
      const p = parseDevicePrefs(raw);
      // One-time migration: the search ordering used to be its own localStorage key. Adopt it when the device
      // cache has no value yet, then drop the old key (deviceSync sends it up like any other held value).
      const old = localStorage.getItem(LEGACY_SEARCH_ORDER_KEY);
      if (old !== null) {
        let has = false;
        try {
          has = isSearchOrder((JSON.parse(raw ?? "null") as { searchOrder?: unknown } | null)?.searchOrder);
        } catch {
          /* no cache */
        }
        if (!has && isSearchOrder(old)) {
          p.searchOrder = old;
          localStorage.setItem(DEVICE_PREFS_KEY, JSON.stringify(p));
        }
        localStorage.removeItem(LEGACY_SEARCH_ORDER_KEY);
      }
      return p;
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

/** Replace everything (the server profile was applied), keeping what is only ever local. */
export function replaceDevicePrefs(next: DevicePrefs): void {
  devicePrefsStore.set(next);
  storage.save(next);
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
