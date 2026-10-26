import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { BookmarkCheck, BookmarkPlus } from "lucide-react";
import { markAllLabel, markAllReach, MarkAllIcon } from "@/lib/itemActionTable";
import { useMarkAllConfirm } from "./MarkAllConfirm";
import { savedScopeOf, scopeFromSearchParams, searchRoute, useSavedSearches } from "@/api/savedSearches";
import { useBootstrap, useFolderTree } from "@/api/queries";
import { folderPath } from "@/lib/folderTree";
import { visibleFeeds } from "@/lib/visibleFeeds";
import type { Scope } from "@/api/types";
import { SEARCH_ORDERS, SEARCH_ORDER_LABELS, scopeOrder, setSearchOrder, useSearchOrder, type SearchOrder } from "@/lib/searchPrefs";
import { Button } from "@/ui/button";
import { cn } from "@/lib/cn";
import { Chip, inputCls } from "@/ui/kit";
import { ReadingMenu } from "./AppearanceControls";
import { EmptyState } from "@/ui/EmptyState";
import { ListPane, type ListControls } from "./ListPane";
import { SaveSearchDialog } from "./search/SaveSearchDialog";
import { SearchHelp } from "./search/SearchHelp";
import { useLatestRef } from "@/lib/useLatestRef";

/** Debounce of the search box: the query is sent this long after the last keystroke. */
const SEARCH_DEBOUNCE_MS = 250;

/**
 * Search across all articles (FTS on the server), or across the scope of a saved search.
 *
 * The text is sent untrimmed. While the user is typing the request carries `typing=1`, so the unfinished last word
 * also matches as a prefix; Enter (or the search key on a phone) makes it a submitted search and drops the flag, as
 * does running a saved search. "Mark all results as read" is only offered for a submitted search, because a search
 * that is still being typed shows a wider set than the exact one the server would mark.
 */
export function SearchScreen() {
  useDocumentTitle("Search");
  const [sp, setSp] = useSearchParams();
  const urlQ = sp.get("q") ?? "";
  const ssId = sp.get("ss");
  const [text, setText] = useState(urlQ);
  // What the box last held from typing, and the query last submitted (Enter). Results are "typing" (wider prefix
  // match) only for a URL `q` that came from the box and differs from the submitted query, so a saved search, an
  // unrelated `q`, or typing back to the submitted text is never one. Two strings, so nothing grows while the screen
  // is open. Derived during render: no effect that lags a frame behind the list's own query.
  const [entry, setEntry] = useState<{ typed: string | null; submitted: string | null }>(() => ({ typed: null, submitted: urlQ }));
  if ((entry.typed !== null || entry.submitted !== null) && ssId !== null) setEntry({ typed: null, submitted: null });
  const typing = entry.typed === urlQ && entry.submitted !== urlQ && ssId === null;
  const [saving, setSaving] = useState(false);
  const [controls, setControls] = useState<ListControls | null>(null);
  const markAll = useMarkAllConfirm();
  const inputId = useId();
  const sortId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const shellRef = useRef<HTMLDivElement>(null);
  const boxRef = useRef<HTMLFormElement>(null);
  const headRef = useRef<HTMLElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);
  const pref = useSearchOrder();
  const boot = useBootstrap();
  const tree = useFolderTree();
  const saved = useSavedSearches();

  const { scope: where, order } = scopeFromSearchParams(sp, pref);
  const whereKey = `${where.view}|${where.feed ?? ""}|${where.folder ?? ""}`;

  // The debounce timer fires after the render that scheduled it: it must build the URL from the params as they are
  // then, or a scope chip or Sort change made inside the window would be put back.
  const latest = useLatestRef(sp);

  /** Change the URL, keeping the scope and saved-search params unless told otherwise. */
  const setQuery = (q: string, drop: string[] = []) => {
    const next = new URLSearchParams(latest.current);
    if (q) next.set("q", q);
    else next.delete("q");
    for (const k of drop) next.delete(k);
    setSp(next, { replace: true });
  };

  // What the box last pushed to the URL; a different `q` arriving (a saved search tapped while on this screen)
  // replaces the text and is a submitted search.
  const pushed = useRef(urlQ);
  useEffect(() => {
    if (urlQ === pushed.current) return;
    pushed.current = urlQ;
    setText(urlQ);
  }, [urlQ]);

  // Debounce: typing updates the URL (and so the query) SEARCH_DEBOUNCE_MS after the last keystroke.
  useEffect(() => {
    if (text === urlQ) return;
    const h = setTimeout(() => {
      pushed.current = text;
      setQuery(text, ["ss"]);
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text, urlQ]);

  // Where focus lands on arrival (the shell does the focusing, see AppShell): the box on a page load or reload
  // (data-load-focus), the heading for every in-app arrival.

  // Escape on the Search screen goes back, as it does on an open article. Not while typing (the box takes the first
  // Escape and lets go of focus), not in a menu or dialog (those take it first), not during IME composition.
  const navigate = useNavigate();
  const noFeeds = !!boot.data && visibleFeeds(boot.data.feeds).length === 0;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isComposing) return;
      if (e.target instanceof HTMLElement && (e.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName))) return;
      if (document.querySelector('[role="dialog"],[role="menu"],[role="listbox"]')) return;
      const idx = (window.history.state as { idx?: number } | null)?.idx ?? 0;
      if (idx <= 0) return;
      e.preventDefault();
      navigate(-1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [navigate]);

  const submit = () => {
    pushed.current = text;
    setEntry({ typed: null, submitted: text });
    if (text !== urlQ) setQuery(text, ["ss"]);
  };

  const q = urlQ;
  const trimmed = q.trim();
  const ready = trimmed.length >= 2;
  const scope: Scope | null = useMemo(
    () => (ready ? { ...where, q, ...(scopeOrder(order) ? { order: scopeOrder(order) } : {}), ...(typing ? { typing: true } : {}) } : null),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ready, whereKey, q, order, typing],
  );

  const feedTitle = boot.data?.feeds.find((f) => f.id === where.feed)?.title;
  const folderName = where.folder && tree.byId.has(where.folder) ? folderPath(tree, where.folder) : undefined;
  const scopeLabel = where.feed
    ? `the feed ${feedTitle ?? "you chose"}`
    : where.folder
      ? `the folder ${folderName ?? "you chose"}`
      : where.view === "unread"
        ? "unread articles"
        : where.view === "starred"
          ? "starred articles"
          : "the whole library";
  const chip = where.feed ? (feedTitle ?? "One feed") : where.folder ? (folderName ?? "One folder") : where.view === "unread" ? "Unread only" : where.view === "starred" ? "Starred only" : null;
  const existing = ssId ? saved.searches.find((s) => s.id === ssId) : undefined;
  // On a screen too short for the header and the results together (400% zoom, a phone held sideways) the screen scrolls as
  // one (index.css .search-shell[data-short]) with only the box held at the top, and the results fill what is left below
  // the box. Both are measured: the screen is short when the header would take more than half of it, and the box height
  // is what the results subtract. The header is measured as the sum of its children, which is the same in both layouts
  // (the dissolved header loses its own padding and border, so its own height would not be), so the answer cannot flip
  // itself at a height near the cutoff.
  const measure = useCallback(() => {
    const shell = shellRef.current;
    const box = boxRef.current;
    const head = headRef.current;
    if (!shell || !box || !head) return;
    shell.style.setProperty("--search-box-h", `${box.offsetHeight}px`);
    let headH = 0;
    for (const c of Array.from(head.children) as HTMLElement[]) headH += c.offsetHeight + parseFloat(getComputedStyle(c).marginTop || "0");
    shell.toggleAttribute("data-short", headH > shell.clientHeight / 2);
  }, []);
  useLayoutEffect(() => measure());
  useLayoutEffect(() => {
    const shell = shellRef.current;
    const box = boxRef.current;
    if (!shell || !box || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(shell);
    ro.observe(box);
    return () => ro.disconnect();
  }, [measure]);
  // In that layout the results are their own scroller, so a wheel or a swipe over them would scroll the list while the
  // header stays over most of the screen. The screen takes the gesture first: down moves the screen until the header is
  // gone, up moves it back once the list is at its top. Only a gesture the screen can use is taken; the rest is the list's.
  useEffect(() => {
    const results = resultsRef.current;
    const shell = shellRef.current;
    if (!results || !shell) return;
    const take = (dy: number, cancelable: boolean): boolean => {
      if (!shell.hasAttribute("data-short") || dy === 0 || !cancelable) return false;
      const room = shell.scrollHeight - shell.clientHeight;
      const list = results.querySelector<HTMLElement>("[data-list-scroller]");
      // What scrolls under the finger is the list, or the results box itself when it holds a message (the empty state).
      const can = dy > 0 ? shell.scrollTop < room - 1 : shell.scrollTop > 0 && (list ?? results).scrollTop <= 0;
      if (can) shell.scrollTop += dy;
      return can;
    };
    const onWheel = (e: WheelEvent) => {
      const px = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaMode === 2 ? e.deltaY * shell.clientHeight : e.deltaY;
      if (take(px, e.cancelable)) e.preventDefault();
    };
    let lastY = 0;
    const onTouchStart = (e: TouchEvent) => {
      lastY = e.touches[0]?.clientY ?? 0;
    };
    const onTouchMove = (e: TouchEvent) => {
      const y = e.touches[0]?.clientY ?? lastY;
      const dy = lastY - y;
      lastY = y;
      if (take(dy, e.cancelable)) e.preventDefault();
    };
    results.addEventListener("wheel", onWheel, { passive: false });
    results.addEventListener("touchstart", onTouchStart, { passive: true });
    results.addEventListener("touchmove", onTouchMove, { passive: false });
    return () => {
      results.removeEventListener("wheel", onWheel);
      results.removeEventListener("touchstart", onTouchStart);
      results.removeEventListener("touchmove", onTouchMove);
    };
  }, []);
  // Once the whole results are loaded (one page, or all pages asked for) their unread number is the number Mark all marks.
  const unread = controls && !controls.empty && !controls.more ? controls.unread : null;
  // What the line says. The screen reader hears it once per search, when the first results have settled, not as the
  // count moves with every keystroke and every page that loads.
  const summary =
    controls?.error
      ? controls.error
      : controls && !controls.empty && controls.count > 0
      ? `${controls.count}${controls.more ? "+" : ""} ${controls.count === 1 && !controls.more ? "result" : "results"} for ${trimmed}`
      : controls?.empty
        ? `No results for ${trimmed}`
        : "";
  const searchKey = `${q}|${whereKey}|${order}`;
  const [said, setSaid] = useState<{ key: string; text: string } | null>(null);
  useEffect(() => {
    if (!ready || text !== urlQ || !summary || said?.key === searchKey) return;
    const h = setTimeout(() => setSaid({ key: searchKey, text: summary }), 700);
    return () => clearTimeout(h);
  }, [ready, text, urlQ, summary, searchKey, said]);
  const markAllText = markAllLabel(scope ?? { ...where, q }, unread);

  return (
    <div className="search-screen h-full min-h-0">
    <div ref={shellRef} className="search-shell flex h-full min-h-0 flex-col">
      <header ref={headRef} className="search-head pt-safe shrink-0 border-b border-line bg-bg pb-3">
        <div className="flex items-center px-4 pt-2">
          <h1 className="min-w-0 flex-1 text-xl font-bold" tabIndex={-1} data-route-heading>
            Search
          </h1>
          <ReadingMenu />
          <SearchHelp />
        </div>
        <form
          ref={boxRef}
          role="search"
          className="sticky top-0 z-10 bg-bg px-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <label htmlFor={inputId} className="sr-only-live">
            Search articles
          </label>
          <input
            id={inputId}
            ref={inputRef}
            data-search-input
            data-load-focus
            type="search"
            enterKeyHint="search"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="Search articles"
            value={text}
            onChange={(e) => {
              const v = e.target.value;
              setText(v);
              setEntry((prev) => ({ ...prev, typed: v }));
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") inputRef.current?.blur();
              // Down from the field goes into the results, as it moves between rows in the list itself.
              else if (e.key === "ArrowDown" && !e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.nativeEvent.isComposing && controls?.focusStop()) e.preventDefault();
            }}
            className={cn(inputCls, "mt-2")}
          />
        </form>
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 px-4">
          <div className="flex items-center gap-2">
            <label htmlFor={sortId} className="text-sm whitespace-nowrap text-fg2">
              Sort by
            </label>
            <select
              id={sortId}
              value={order}
              onChange={(e) => {
                setSearchOrder(e.target.value as SearchOrder);
                // The ordering picked here is the device's from now on; a saved search's own order was for that run.
                if (sp.has("order")) setQuery(q, ["order"]);
              }}
              className={`${inputCls} w-auto`}
            >
              {SEARCH_ORDERS.map((o) => (
                <option key={o} value={o}>
                  {SEARCH_ORDER_LABELS[o]}
                </option>
              ))}
            </select>
          </div>
          {chip ? (
            <Chip className="max-w-52" removeLabel="Search the whole library instead" onRemove={() => setQuery(q, ["feed", "folder", "view", "ss"])}>
              In {chip}
            </Chip>
          ) : null}
        </div>
        {ready ? (
          <div className="px-4">
            <p data-testid="result-count" className="mt-2 min-h-5 text-sm text-fg2 [overflow-wrap:anywhere]">
              {controls && !controls.empty && controls.count > 0 ? summary : null}
              {existing ? <span className="ml-2 font-medium text-fg">Saved</span> : null}
            </p>
            <div className="mt-1 flex flex-wrap gap-x-2">
              <Button variant="ghost" onClick={() => setSaving(true)} disabled={!existing && controls?.empty === true} title={!existing && controls?.empty ? "No results to save" : undefined} className="px-2">
                {existing ? <BookmarkCheck aria-hidden="true" /> : <BookmarkPlus aria-hidden="true" />}
                {existing ? "Edit saved search" : "Save this search"}
              </Button>
              <Button
                variant="ghost"
                className="px-2"
                onClick={() => markAll.ask(markAllText, markAllReach(scope ?? { ...where, q }, "these results"), () => controls?.markAllRead())}
                disabled={!controls || controls.count === 0 || typing}
                title={typing ? "Press Enter to search first, then mark all results as read" : undefined}
              >
                <MarkAllIcon aria-hidden="true" />
                {markAllText}
              </Button>
            </div>
          </div>
        ) : null}
        <p role="status" className="sr-only-live" data-testid="result-announce">
          {said?.key === searchKey ? said.text : ""}
        </p>
        {markAll.dialog}
      </header>
      <div ref={resultsRef} className="search-results min-h-0 flex-1 overflow-y-auto">
        {scope && !noFeeds ? (
          <ListPane key={`${q}|${whereKey}|${order}`} scope={scope} onControls={setControls} />
        ) : (
          <EmptyState
            title={noFeeds ? "Add a feed first" : trimmed.length === 0 ? "Search your articles" : "Keep typing"}
            why={noFeeds ? "Search covers the articles of your feeds, and you have none yet." : trimmed.length === 0 ? "Search covers every article Kipple has kept." : "Search needs at least two characters."}
            action={
              noFeeds ? (
                <Button variant="solid" onClick={() => navigate("/feeds", { state: { open: "add" } })}>
                  Add a feed
                </Button>
              ) : trimmed.length === 0 ? (
                <div className="flex flex-col items-center gap-2 text-sm">
                  <p className="text-fg2">
                    Try <code className="font-mono text-fg">title:bridges</code> or <code className="font-mono text-fg">&quot;exact phrase&quot;</code>.
                  </p>
                  {saved.searches.length > 0 ? (
                    <ul aria-label="Saved searches" className="flex flex-col items-center">
                      {saved.searches.slice(0, 5).map((s) => (
                        <li key={s.id}>
                          <Link to={searchRoute(s)} className="inline-flex min-h-11 items-center text-link underline underline-offset-2">
                            {s.name}
                          </Link>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </div>
              ) : null
            }
          />
        )}
      </div>
      {saving && ready ? (
        <SaveSearchDialog
          open
          onOpenChange={setSaving}
          q={q}
          scope={savedScopeOf(where)}
          order={order}
          scopeLabel={scopeLabel}
          existing={existing}
        />
      ) : null}
    </div>
    </div>
  );
}
