import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import { InfiniteQueryObserver, useQueryClient, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import { itemsQuery, keys, scopeKey, SECTION_SUFFIX, useBootstrap } from "@/api/queries";
import type { Card, Feed, Folder, ItemsPage, Scope } from "@/api/types";
import { useDevicePrefs, type DigestOrder, type DigestSections } from "@/lib/devicePrefs";
import { chainOf, folderTree } from "@/lib/folderTree";
import { byFeed, byFolder } from "@/lib/sections";
import { createStore, useStore, useStoreSelector } from "@/lib/store";
import { arrivedAfter } from "@/lib/itemActions";
import { useLatestRef } from "@/lib/useLatestRef";
import { NEW_ROWS, visitStore } from "@/lib/catchup";
import { EXPAND_BATCH, groupFeeds, layoutDigest, orderSections, type LoadedSection } from "@/layouts/digestPlan";
import type { DigestSectionView, DigestView } from "@/layouts/types";
import type { ListSource } from "./listSource";

/**
 * The Digest page's data: every folder (or feed) is a list of its own, asked for lazily. The first FIRST_SECTIONS are
 * asked for at open and STEP_SECTIONS more each time the end of the page comes near; a section shows its first few
 * cards and EXPAND_BATCH more for each press of "N more". What the page shows is digestPlan's layout over what has loaded.
 *
 * The page is a snapshot, like every list: the sections it lists, and their order, are those of the moment it was
 * opened (or last started over), so a count that moves while the reader reads never adds, drops or moves a section
 * under them.
 */

/** Sections asked for when the page opens. */
export const FIRST_SECTIONS = 6;
/** Sections asked for each time the end of the page comes near. */
export const STEP_SECTIONS = 4;
/** Cards a section's request returns: one "N more" batch, so a press needs at most one more request. */
export const SECTION_PAGE = EXPAND_BATCH;

/** What a Digest page remembers about a list while the app runs: how far it has loaded, which sections were opened out. */
interface Place {
  /** Times the end of the page was reached (STEP_SECTIONS more sections each). */
  steps: number;
  /** Presses of "N more", by section key. */
  presses: ReadonlyMap<string, number>;
}
const FRESH: Place = { steps: 0, presses: new Map() };
const PLACES_MAX = 50;
const places = createStore<ReadonlyMap<string, Place>>(new Map());
/**
 * How long the page a reader left is kept as it was: the time the section lists stay in the query cache (its default
 * garbage collection), after which a return starts a new page, so a folder that gained its first unread article shows.
 */
const SNAPSHOT_KEEP_MS = 5 * 60_000;
/**
 * What each list showed when it was opened: its sections in order, the unread count of each as the bootstrap said then
 * (the page counts "N more" from it, so a count that moves under the reader does not), the Sections and Order settings
 * it came from (a change of either makes a new page; Rows and Images do not change which sections there are), the
 * `as_of` each section first came back with (what a mark over it is bounded by), and when the reader left it.
 */
const opened = new Map<string, { sig: string; keys: string[]; counts: Map<string, number | null>; bounds: Map<string, string>; left: number | null }>();
/** How many mounted hooks (the list, and the reader's previous and next) use each list's page. The reader has left it only when none does. */
const consumers = new Map<string, number>();
/** A hook starts using the list's page: it is no longer away. Returns the call that stops. */
export function holdPage(listKey: string): () => void {
  consumers.set(listKey, (consumers.get(listKey) ?? 0) + 1);
  const o = opened.get(listKey);
  if (o) o.left = null;
  return () => {
    const n = (consumers.get(listKey) ?? 1) - 1;
    if (n > 0) return void consumers.set(listKey, n);
    consumers.delete(listKey);
    const kept = opened.get(listKey);
    if (kept) kept.left = Date.now();
  };
}
/** When the reader left the page of list `listKey` (null while it is in use; undefined for a list with no page). For tests. */
export const digestPageLeftAt = (listKey: string): number | null | undefined => opened.get(listKey)?.left;

const placeOf = (key: string): Place => places.get().get(key) ?? FRESH;
function setPlace(key: string, next: (p: Place) => Place): void {
  places.set((all) => {
    const m = new Map(all);
    const p = next(m.get(key) ?? FRESH);
    m.delete(key);
    m.set(key, p);
    while (m.size > PLACES_MAX) m.delete(m.keys().next().value!);
    return m;
  });
}
/** Forget every list's place (tests). */
export function clearDigestPlaces(): void {
  places.set(new Map());
  opened.clear();
}

type FeedLike = Pick<Feed, "id" | "title" | "folder_id" | "unread" | "starred_count" | "is_archive"> & { pace?: number | null };
type FolderLike = Pick<Folder, "id" | "name" | "parent_id">;

export interface DigestInput {
  scope: Scope;
  feeds: readonly FeedLike[];
  folders: readonly FolderLike[];
  sections: DigestSections;
  order: DigestOrder;
  rows: number;
  place: Place;
  /**
   * The New page: the articles that arrived since the mark, by section key. They replace the bootstrap's unread counts,
   * and `visit` (which visit they are from) makes a new visit a new page.
   */
  arrivals?: { visit: number; counts: ReadonlyMap<string, number> };
}

/** What a section's list came to: its cards (if it has any yet), and how its requests stand. */
export interface SectionResult {
  data?: { cards: Card[]; more: boolean; asOf: string | undefined };
  error: unknown;
  fetching: boolean;
  fetchingMore: boolean;
  moreFailed: boolean;
  /** Ask the next page of this section. */
  fetchMore?: () => void;
  refetch?: () => void;
}

/** The list a section is: its view, length and the folder or feed it is. */
export const sectionScope = (scope: Pick<Scope, "view" | "length" | "after">, s: { scope: Pick<Scope, "feed" | "folder"> }): Scope => ({
  view: scope.view,
  length: scope.length,
  ...(scope.after ? { after: scope.after } : {}),
  ...s.scope,
});

function shape(data: InfiniteData<ItemsPage>, more: boolean): NonNullable<SectionResult["data"]> {
  const last = data.pages[data.pages.length - 1];
  return { cards: data.pages.flatMap((p) => p.items), more: more || !!last?.next_cursor, asOf: data.pages[0]?.as_of };
}

/** The section's list as the cache holds it now (no request is made). */
function fromCache(qc: QueryClient, sc: Scope): SectionResult | undefined {
  const data = qc.getQueryData<InfiniteData<ItemsPage>>(keys.section(sc));
  return data ? { data: shape(data, false), error: null, fetching: false, fetchingMore: false, moreFailed: false } : undefined;
}

/**
 * The page: the sections of a list in the order it shows them, the first ones asked for, and the layout over what has
 * loaded. `read` says what a section's list is.
 */
export function planDigest(input: DigestInput, read: (sc: Scope) => SectionResult | undefined) {
  const { scope, feeds, folders, place } = input;
  const tree = folderTree(folders);
  const inScope = feeds.filter((f) => (scope.feed ? f.id === scope.feed : scope.folder ? chainOf(tree, f.folder_id).includes(scope.folder) : true));
  const keyOf = input.sections === "feed" ? byFeed(inScope) : byFolder(folders, inScope, scope);
  const live = orderSections(groupFeeds(inScope, folders, keyOf, scope.view), input.order);
  // A section with nothing unread is not asked for. Which sections there are, and their order, are fixed when the page opens.
  const listKey = scopeKey(scope);
  const sig = `${input.sections}|${input.order}|${input.arrivals?.visit ?? ""}`;
  let fixed = opened.get(listKey);
  // Time away counts only while no hook uses the page: one that is mounted keeps it, however long the reader reads.
  const away = !consumers.has(listKey) && fixed?.left != null && Date.now() - fixed.left > SNAPSHOT_KEEP_MS;
  if (fixed && (fixed.sig !== sig || away)) fixed = undefined;
  if (!fixed) {
    // On the New page a section's count is what arrived since the mark; a section the visit did not list has none.
    const countOf = (d: (typeof live)[number]): number | null => (input.arrivals ? (input.arrivals.counts.get(d.key) ?? 0) : d.count);
    const kept = live.filter((d) => !(scope.view === "unread" && countOf(d) === 0));
    fixed = { sig, keys: kept.map((d) => d.key), counts: new Map(kept.map((d) => [d.key, countOf(d)])), bounds: new Map(), left: null };
    opened.delete(listKey);
    opened.set(listKey, fixed);
    while (opened.size > PLACES_MAX) opened.delete(opened.keys().next().value!);
  }
  const byKey = new Map(live.map((d) => [d.key, d]));
  // A length filter makes the unread counts a count of something else: only a count of nothing still says there is nothing.
  const counted = (k: string): number | null => {
    const n = fixed.counts.get(k) ?? null;
    return scope.length && n !== 0 ? null : n;
  };
  const defs = fixed.keys.flatMap((k) => {
    const d = byKey.get(k);
    return d ? [{ ...d, count: counted(k) }] : [];
  });
  const askedFor = new Set(defs.slice(0, FIRST_SECTIONS + STEP_SECTIONS * place.steps).map((d) => d.key));
  const results = new Map<string, SectionResult>();
  const loaded = new Map<string, LoadedSection>();
  for (const d of defs) {
    if (!askedFor.has(d.key)) continue;
    let r = read(sectionScope(scope, d));
    if (!r) continue;
    // A section's bound is the as_of of the first list the page had of it: a refetch moves the list's own as_of on, and
    // a mark bounded by that would take in articles the page never showed.
    if (r.data?.asOf) {
      const bound = fixed.bounds.get(d.key) ?? r.data.asOf;
      fixed.bounds.set(d.key, bound);
      r = { ...r, data: { ...r.data, asOf: bound } };
    }
    results.set(d.key, r);
    if (r.data) loaded.set(d.key, { cards: r.data.cards, more: r.data.more });
  }
  const layout = layoutDigest(defs, { view: scope.view, rows: input.rows, expanded: place.presses, activated: askedFor }, loaded);
  return { defs, askedFor, results, layout, waiting: defs.length > askedFor.size };
}
export type Plan = ReturnType<typeof planDigest>;

/** The sections the page draws: the ones asked for that have something to show or to say. */
function sectionViews(plan: Plan): DigestSectionView[] {
  const out: DigestSectionView[] = [];
  for (const s of plan.layout.sections) {
    if (s.state === "waiting") continue;
    if (s.state === "ready" && s.cards.length === 0) continue;
    const r = plan.results.get(s.key);
    out.push({
      ...s,
      error: s.state === "loading" ? (r?.error ?? null) : null,
      loadingMore: !!r?.fetchingMore,
      busy: !!r?.fetching,
      moreFailed: !!r?.moreFailed,
    });
  }
  return out;
}

/** The `as_of` of the oldest first page among the sections: a mark all bounded by it leaves alone what any section did not yet know of. */
export function oldestAsOf(asOfs: readonly (string | undefined)[]): string | undefined {
  let out: string | undefined;
  for (const a of asOfs) {
    if (!a) continue;
    try {
      if (out === undefined || BigInt(a) < BigInt(out)) out = a;
    } catch {
      /* an as_of that is not a number takes no part */
    }
  }
  return out;
}

type Observer = InfiniteQueryObserver<ItemsPage, Error, InfiniteData<ItemsPage, string>, readonly unknown[], string>;
type Options = Parameters<Observer["getOptimisticResult"]>[0];
interface Entry {
  observer: Observer;
  options: Options;
  off: (() => void) | null;
}

/** The observers of a page's sections, one per section, subscribed while the page wants it: that is what loads a section and follows the cache. */
class SectionObservers {
  private entries = new Map<string, Entry>();
  private listeners = new Set<() => void>();
  private version = 0;
  constructor(private qc: QueryClient) {}

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => void this.listeners.delete(fn);
  };
  getVersion = () => this.version;
  private emit = () => {
    this.version++;
    this.listeners.forEach((f) => f());
  };

  private entry(sc: Scope): Entry {
    const id = String(keys.section(sc)[1]);
    const had = this.entries.get(id);
    if (had) return had;
    const options = this.qc.defaultQueryOptions(itemsQuery(sc, keys.section(sc), SECTION_PAGE)) as unknown as Options;
    const made: Entry = { observer: new InfiniteQueryObserver(this.qc, options), options, off: null };
    this.entries.set(id, made);
    return made;
  }

  /** What the section's list is now, from the cache (a request not yet made shows as pending). */
  result(sc: Scope): SectionResult {
    const { observer, options } = this.entry(sc);
    const r = observer.getOptimisticResult(options);
    return {
      data: r.data ? shape(r.data, !!r.hasNextPage) : undefined,
      error: r.error,
      fetching: r.isFetching,
      fetchingMore: r.isFetchingNextPage,
      moreFailed: r.isFetchNextPageError,
      fetchMore: () => void observer.fetchNextPage(),
      refetch: () => void observer.refetch(),
    };
  }

  /** Subscribe to the wanted sections and let go of the others. */
  want(scopes: readonly Scope[]): void {
    const wanted = new Set(scopes.map((sc) => String(keys.section(sc)[1])));
    for (const sc of scopes) {
      const e = this.entry(sc);
      e.off ??= e.observer.subscribe(this.emit);
    }
    for (const [id, e] of this.entries) {
      if (wanted.has(id)) continue;
      e.off?.();
      this.entries.delete(id);
    }
    this.emit();
  }
  release(): void {
    for (const e of this.entries.values()) {
      e.off?.();
      e.off = null;
    }
  }
}

export interface DigestSource extends ListSource {
  digest: DigestView;
  /** Every article the sections hold, shown or not, in the page's order. */
  loadedCards: Card[];
  /** Ask for the next sections; resolves once they are in, with the articles shown then and whether sections are still left. */
  loadMore: () => Promise<{ cards: Card[]; more: boolean }>;
}

const EMPTY_CARDS: Card[] = [];

/**
 * The Digest page of a list: the list screen's source when the layout is the Digest. `on` false makes it inert (the
 * hook is called either way). `live` false only reads what is cached, for the reader's previous and next with no list
 * mounted.
 */
export function useDigestSource(scope: Scope, on: boolean, live = true): DigestSource {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const listKey = scopeKey(scope);
  const place = useStoreSelector(places, (all) => all.get(listKey) ?? FRESH);
  const observers = useMemo(() => new SectionObservers(qc), [qc]);
  const version = useSyncExternalStore(observers.subscribe, observers.getVersion);
  const feeds = boot.data?.feeds;
  const folders = boot.data?.folders;
  const { view, feed, folder, length, after } = scope;
  // The New page is a Digest of its own: a fixed few articles in each top-level folder, in the sidebar's order.
  const visit = useStore(visitStore);
  const arrivals = useMemo(() => (after && visit?.mark === after ? { visit: visit.id, counts: visit.counts } : undefined), [after, visit]);
  const rows = after ? NEW_ROWS : dp.digestRows;

  const input = useMemo<DigestInput | null>(
    () => (on && feeds && folders ? { scope: { view, feed, folder, length, after }, feeds, folders, sections: after ? "folder" : dp.digestSections, order: after ? "sidebar" : dp.digestOrder, rows, place, arrivals } : null),
    [on, feeds, folders, view, feed, folder, length, after, dp.digestSections, dp.digestOrder, rows, place, arrivals],
  );
  // `version` is not read here: it says that the observers' results moved.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const plan = useMemo(() => (input ? planDigest(input, (sc) => observers.result(sc)) : null), [input, observers, version]);

  // The sections asked for are subscribed to: that is what loads them.
  const wanted = useMemo(() => (plan && live ? plan.defs.filter((d) => plan.askedFor.has(d.key)).map((d) => sectionScope({ view, length, after }, d)) : []), [plan, live, view, length, after]);
  const wantedKey = wanted.map((sc) => scopeKey(sc)).join("\n");
  useEffect(() => {
    observers.want(wanted);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [observers, wantedKey]);
  useEffect(() => () => observers.release(), [observers]);
  useEffect(() => (on ? holdPage(listKey) : undefined), [on, listKey]);

  // A press of "N more" shows more cards than the section has loaded: load the page that has them.
  useEffect(() => {
    if (!plan || !live) return;
    for (const s of plan.layout.sections) {
      const r = plan.results.get(s.key);
      const want = rows + EXPAND_BATCH * (place.presses.get(s.key) ?? 0);
      if (r?.data?.more && r.data.cards.length < want && !r.fetching && !r.moreFailed) r.fetchMore?.();
    }
  });

  const latest = useLatestRef({ input, plan });

  const expand = useCallback((key: string) => setPlace(listKey, (p) => ({ ...p, presses: new Map(p.presses).set(key, (p.presses.get(key) ?? 0) + 1) })), [listKey]);
  const retry = useCallback((key: string) => {
    const r = latest.current.plan?.results.get(key);
    if (r?.error != null && !r.data) r.refetch?.();
    else r?.fetchMore?.();
  }, [latest]);

  const snapshot = useCallback((): { cards: Card[]; more: boolean } => {
    const inp = latest.current.input;
    if (!inp) return { cards: [], more: false };
    const p = planDigest({ ...inp, place: placeOf(listKey) }, (sc) => fromCache(qc, sc));
    return { cards: p.layout.cards, more: p.waiting };
  }, [qc, listKey, latest]);

  const loadMore = useCallback(async () => {
    const inp = latest.current.input;
    if (!inp) return { cards: [], more: false };
    setPlace(listKey, (p) => ({ ...p, steps: p.steps + 1 }));
    const next = planDigest({ ...inp, place: placeOf(listKey) }, (sc) => fromCache(qc, sc));
    await Promise.allSettled(
      next.defs
        .filter((d) => next.askedFor.has(d.key))
        .map((d) => {
          const sc = sectionScope(inp.scope, d);
          return qc.fetchInfiniteQuery(itemsQuery(sc, keys.section(sc), SECTION_PAGE));
        }),
    );
    return snapshot();
  }, [qc, listKey, snapshot, latest]);

  const startOver = useCallback(async () => {
    setPlace(listKey, () => FRESH);
    opened.delete(listKey);
    await qc.resetQueries({ queryKey: keys.itemsAll, predicate: (q) => String(q.queryKey[1]).startsWith(`${view}|`) && String(q.queryKey[1]).endsWith(SECTION_SUFFIX) && String(q.queryKey[1]).includes("|after:") === !!after });
  }, [qc, listKey, view, after]);

  const results = useMemo(() => (plan ? [...plan.results.values()] : []), [plan]);
  const hasData = results.some((r) => r.data);
  // A section is out while it has neither its cards nor an error to show for them.
  const loading = results.some((r) => !r.data && r.error == null);
  const errored = results.find((r) => r.error != null && !r.data);
  const asOf = useMemo(() => oldestAsOf(results.map((r) => r.data?.asOf)), [results]);
  const shownViews = useMemo(() => (plan ? sectionViews(plan) : []), [plan]);
  const allCards = useMemo(() => (plan ? plan.defs.flatMap((d) => plan.results.get(d.key)?.data?.cards ?? []) : EMPTY_CARDS), [plan]);
  const digest = useMemo<DigestView>(() => ({ sections: shownViews, rows, images: dp.digestImages, counts: !!after, expand, retry }), [shownViews, rows, dp.digestImages, after, expand, retry]);

  // The sections asked for came back with nothing to show (a length filter can empty them all) and there are more to
  // ask for: ask, so the page is never empty while a section that has articles has not been looked at. With nothing
  // drawn there is no end of the page to reach.
  const nothingYet = !!plan && live && !loading && plan.waiting && shownViews.length === 0;
  useEffect(() => {
    if (nothingYet) setPlace(listKey, (p) => ({ ...p, steps: p.steps + 1 }));
  }, [nothingYet, listKey, plan?.askedFor.size]);

  return {
    cards: plan?.layout.cards ?? EMPTY_CARDS,
    more: !!plan?.waiting,
    loadMore,
    loadingMore: hasData && results.some((r) => r.fetching && !r.data),
    loadMoreFailed: false,
    // Nothing to show until the bootstrap says which sections there are and one has come back; and while sections are
    // still out, a failed one does not stand for the page.
    pending: on && (!plan || (plan.askedFor.size > 0 && !hasData && loading) || nothingYet),
    fetching: results.some((r) => r.fetching),
    error: hasData || loading ? null : (errored?.error ?? null),
    hasData,
    asOf,
    fallback: false,
    retry: () => results.forEach((r) => (r.error != null && !r.data ? r.refetch?.() : undefined)),
    startOver,
    rangeOf: (item) => {
      for (const d of plan?.defs ?? []) {
        const r = plan?.results.get(d.key)?.data;
        if (r?.cards.some((c) => c.id === item.id)) return { cards: r.cards, scope: sectionScope(scope, d), asOf: r.asOf };
      }
      return { why: "Mark above and below need the article's section loaded" };
    },
    markAllParts: () => {
      // The whole list, up to the oldest bound among the sections (the page's opening): it covers every section, the
      // ones not loaded too. A loaded section whose own bound is newer is marked up to that, so what it shows stays marked.
      const loadedSections = (plan?.defs ?? []).flatMap((d) => {
        const r = plan?.results.get(d.key)?.data;
        return r ? [{ d, r }] : [];
      });
      const oldest = oldestAsOf(loadedSections.map((s) => s.r.asOf));
      // With no section loaded there is no bound, and a request without one marks what arrived after the page opened,
      // which the reader never saw: no request is better than that one.
      if (!oldest) return [];
      const whole = { scope: undefined, asOf: oldest, cards: loadedSections.flatMap((s) => s.r.cards).filter((c) => !arrivedAfter(c.id, oldest)) };
      const newer = loadedSections
        .filter((s) => oldest && s.r.asOf && arrivedAfter(s.r.asOf, oldest))
        .map((s) => ({ scope: sectionScope(scope, s.d), asOf: s.r.asOf, cards: s.r.cards.filter((c) => arrivedAfter(c.id, oldest)) }));
      return [whole, ...newer];
    },
    digest,
    loadedCards: allCards,
  };
}
