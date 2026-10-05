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
export const ORDER_LABELS: Record<OrderPref, string> = { newest: "Newest first", oldest: "Oldest first" };

/** The views a feed or folder list can open in. Unread unless the feed or a folder above it says otherwise. */
export type ListView = "unread" | "all";
export const LIST_VIEWS: readonly ListView[] = ["unread", "all"];
export const LIST_VIEW_LABELS: Record<ListView, string> = { unread: "Unread", all: "All" };
export const DEFAULT_LIST_VIEW: ListView = "unread";

/**
 * What one feed or folder list sets for itself on this device (server key `client.list_overrides`). A field left out is
 * inherited: from the nearest folder above that sets it, else the device default.
 */
export interface ListOverride {
  layout?: LayoutId;
  order?: OrderPref;
  view?: ListView;
}
export type ListField = keyof ListOverride;

/** The Search screen's ordering. The ids are the URL/saved-search ones; the profile key `client.search_order`
 * (docs/design.md 7.1c) calls them relevance, newest and oldest. */
export const SEARCH_ORDERS = ["rank", "date", "oldest"] as const;
export type SearchOrder = (typeof SEARCH_ORDERS)[number];
const isSearchOrder = (v: unknown): v is SearchOrder => (SEARCH_ORDERS as readonly unknown[]).includes(v);
export const SEARCH_ORDER_TO_SERVER: Record<SearchOrder, string> = { rank: "relevance", date: "newest", oldest: "oldest" };
export const searchOrderFromServer = (v: unknown): SearchOrder | undefined =>
  v === "relevance" ? "rank" : v === "newest" ? "date" : v === "oldest" ? "oldest" : undefined;
/** Where this ordering lived before it joined the device profile (one-time migration source). */
const LEGACY_SEARCH_ORDER_KEY = "kipple.searchOrder.v1";

export interface DevicePrefs {
  /** Device default layout. Magazine unless changed. */
  layout: LayoutId;
  /** Per-feed and per-folder overrides of layout, order and view, resolved field by field: the feed, then the folders
   * up the tree, then the device default (resolveList). */
  overrides: { feed: Record<string, ListOverride>; folder: Record<string, ListOverride> };
  /** Device default order. */
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
  /** Draw the words of Highlight filters in lists and articles (the reading menu's "Highlight keywords"). */
  highlightKeywords: boolean;
  /** The layout to go back to when "Titles only in lists" is turned off. Local to this device (no profile key). */
  layoutBeforeTitlesOnly: LayoutId | null;
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
  highlightKeywords: true,
  layoutBeforeTitlesOnly: null,
};

/**
 * The cache of this device's prefs. Versioned by the shape of `overrides`: v1 held `{id: layout}`, v2 holds
 * `{id: {layout?, order?, view?}}`. A tab of the other build writes the other key, so neither reads the other's
 * values back as its own (a v1 writer's cache read as v2 would drop every override and send that up).
 */
export const DEVICE_PREFS_KEY = "kipple.device.v2";
/** The v1 cache: read once, when there is no v2 cache yet, and never written. */
export const LEGACY_DEVICE_PREFS_KEY = "kipple.device.v1";

const isLayoutId = (v: unknown): v is LayoutId => LAYOUT_IDS.includes(v as LayoutId);

const clampNum = (n: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, Math.round(n)));

/** A well-formed favorites list: known kinds, digit ids, no repeats, at most `max` (500). */
export function cleanFavorites(v: unknown, max = 500): Favorite[] {
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
    if (out.length >= max) break;
  }
  return out;
}

/** One well-formed override: only known fields with known values; null when nothing is left. */
function cleanOverride(v: unknown): ListOverride | null {
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  const o = v as Record<string, unknown>;
  const out: ListOverride = {};
  if (isLayoutId(o.layout)) out.layout = o.layout;
  if (o.order === "newest" || o.order === "oldest") out.order = o.order;
  if (LIST_VIEWS.includes(o.view as ListView)) out.view = o.view as ListView;
  return Object.keys(out).length ? out : null;
}

function cleanMap(v: unknown): Record<string, ListOverride> {
  const out: Record<string, ListOverride> = {};
  if (v && typeof v === "object") {
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
      const o = cleanOverride(val);
      if (o) out[k] = o;
    }
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
      highlightKeywords: v?.highlightKeywords !== false,
      layoutBeforeTitlesOnly: isLayoutId(v?.layoutBeforeTitlesOnly) && v.layoutBeforeTitlesOnly !== "headlines" ? v.layoutBeforeTitlesOnly : null,
    };
  } catch {
    return { ...d, overrides: { feed: {}, folder: {} } };
  }
}

/** A v1 cache, whose overrides were a layout id per feed or folder: each becomes `{layout}`. */
export function parseLegacyDevicePrefs(raw: string | null): DevicePrefs {
  try {
    const v = JSON.parse(raw ?? "null") as { overrides?: Record<string, unknown> } | null;
    if (v && typeof v === "object" && v.overrides && typeof v.overrides === "object") {
      const wrap = (m: unknown) =>
        m && typeof m === "object" ? Object.fromEntries(Object.entries(m as Record<string, unknown>).map(([k, l]) => [k, { layout: l }])) : {};
      v.overrides = { feed: wrap(v.overrides.feed), folder: wrap(v.overrides.folder) };
    }
    return parseDevicePrefs(JSON.stringify(v));
  } catch {
    return parseDevicePrefs(null);
  }
}

/** The only place that knows where device prefs live. */
const storage = {
  load(): DevicePrefs {
    try {
      let raw = localStorage.getItem(DEVICE_PREFS_KEY);
      let p: DevicePrefs;
      if (raw === null && (raw = localStorage.getItem(LEGACY_DEVICE_PREFS_KEY)) !== null) {
        // First load of this build: the first paint keeps the layouts the v1 cache held. The v1 key stays for a tab
        // of the older build still open.
        p = parseLegacyDevicePrefs(raw);
        localStorage.setItem(DEVICE_PREFS_KEY, JSON.stringify(p));
      } else p = parseDevicePrefs(raw);
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

const isId = (x: string): boolean => /^[0-9]{1,19}$/.test(x);
/** The highest numeric id in `ids`, or null for none. */
function maxId(ids: ReadonlySet<string>): bigint | null {
  let top: bigint | null = null;
  for (const x of ids) if (isId(x) && (top === null || BigInt(x) > top)) top = BigInt(x);
  return top;
}

/** The feeds and folders the library has (the bootstrap), for dropping overrides of deleted ones. */
export interface KnownLists {
  feeds: ReadonlySet<string>;
  folders: ReadonlySet<string>;
}

/**
 * Set (or with null clear) one field of a feed's or folder's override; an override with no field left is removed.
 * With `known`, overrides of feeds and folders that no longer exist (absent from `known` and below its highest id of
 * that kind) are dropped in the same write: the profile key has
 * a byte budget, and a deleted list's override would otherwise hold its share forever. Pruning happens here, where
 * the key grows, rather than on the server's feed and folder deletes: the server stores this key without reading it,
 * and every device prunes its own profile the next time it writes one.
 */
export function setListOverride<F extends ListField>(kind: "feed" | "folder", id: string, field: F, value: ListOverride[F] | null, known?: KnownLists): void {
  devicePrefsStore.set((p) => {
    const keep = (k: "feed" | "folder", m: Record<string, ListOverride>) => {
      if (!known) return { ...m };
      const ids = k === "feed" ? known.feeds : known.folders;
      const top = maxId(ids);
      // Only an id below the highest one this bootstrap knows can be a deleted list: ids only grow, so a higher one
      // may be a feed or folder another tab created after this tab's bootstrap was loaded.
      return Object.fromEntries(Object.entries(m).filter(([x]) => (k === kind && x === id) || ids.has(x) || top === null || !isId(x) || BigInt(x) > top));
    };
    const overrides = { feed: keep("feed", p.overrides.feed), folder: keep("folder", p.overrides.folder) };
    const map = overrides[kind];
    const entry: ListOverride = { ...map[id] };
    if (value) entry[field] = value;
    else delete entry[field];
    if (Object.keys(entry).length) map[id] = entry;
    else delete map[id];
    const next = { ...p, overrides };
    storage.save(next);
    return next;
  });
}

/** Set (or with null clear) the layout of one feed or folder. */
export const setLayoutOverride = (kind: "feed" | "folder", id: string, layout: LayoutId | null): void => setListOverride(kind, id, "layout", layout);

/** Transient layout used by the `c` key (not persisted, cleared by any explicit choice). */
export const sessionLayoutStore = createStore<LayoutId | null>(null);

export interface LayoutContext {
  feedId?: string;
  /** The list's folder (a feed list: the feed's folder) and the folders above it, nearest first. */
  folderIds?: readonly string[];
}

type FieldValue<F extends ListField> = NonNullable<ListOverride[F]>;

export interface Resolved<F extends ListField> {
  value: FieldValue<F>;
  /** Where it comes from: the feed itself, the nearest folder that sets the field, or null for the device default. */
  from: { kind: "feed" | "folder"; id: string } | null;
}

const deviceDefault = <F extends ListField>(p: DevicePrefs, field: F): FieldValue<F> =>
  (field === "layout" ? p.layout : field === "order" ? p.order : DEFAULT_LIST_VIEW) as FieldValue<F>;

/**
 * The one resolution of a list's layout, order and view: the feed's own override, then the nearest folder up the tree
 * that sets the field, then the device default (Unread for the view, which has no device default). Each field resolves
 * on its own, so a feed can keep its folder's layout and set its own order.
 */
export function resolveList<F extends ListField>(p: DevicePrefs, ctx: LayoutContext, field: F): Resolved<F> {
  const own = ctx.feedId ? p.overrides.feed[ctx.feedId]?.[field] : undefined;
  if (own && ctx.feedId) return { value: own as FieldValue<F>, from: { kind: "feed", id: ctx.feedId } };
  for (const id of ctx.folderIds ?? []) {
    const v = p.overrides.folder[id]?.[field];
    if (v) return { value: v as FieldValue<F>, from: { kind: "folder", id } };
  }
  return { value: deviceDefault(p, field), from: null };
}

/** What a feed or folder list gets without its own value for `field`: the same resolution, one level up. */
export function inheritedList<F extends ListField>(p: DevicePrefs, ctx: LayoutContext, field: F): Resolved<F> {
  const target = overrideTarget(ctx);
  if (!target) return resolveList(p, ctx, field);
  return resolveList(p, { folderIds: target.kind === "feed" ? ctx.folderIds : ctx.folderIds?.slice(1) }, field);
}

/** The layout for a list: the session toggle (`c`), else resolveList. */
export function resolveLayout(p: DevicePrefs, ctx: LayoutContext, session: LayoutId | null = null): LayoutId {
  return session ?? resolveList(p, ctx, "layout").value;
}

/** Which override a list can carry, if any (a feed list or a folder list). */
export function overrideTarget(ctx: LayoutContext): { kind: "feed" | "folder"; id: string } | null {
  if (ctx.feedId) return { kind: "feed", id: ctx.feedId };
  const folder = ctx.folderIds?.[0];
  if (folder) return { kind: "folder", id: folder };
  return null;
}
