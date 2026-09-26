import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { BookmarkPlus, CheckCheck, X } from "lucide-react";
import { savedScopeOf, scopeFromSearchParams, useSavedSearches } from "@/api/savedSearches";
import { useBootstrap } from "@/api/queries";
import type { Scope } from "@/api/types";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { SEARCH_ORDERS, SEARCH_ORDER_LABELS, scopeOrder, setSearchOrder, useSearchOrder, type SearchOrder } from "@/lib/searchPrefs";
import { useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { inputCls } from "@/ui/kit";
import { ListPane, StatusBlock, type ListControls } from "./ListPane";
import { SaveSearchDialog } from "./search/SaveSearchDialog";
import { SearchHelp } from "./search/SearchHelp";

/** Debounce of the search box: the query is sent this long after the last keystroke. */
export const SEARCH_DEBOUNCE_MS = 250;

/**
 * Search across all articles (FTS on the server), or across the scope of a saved search.
 *
 * The text is sent untrimmed. While the user is typing the request carries `typing=1`, so the unfinished last word
 * also matches as a prefix; Enter (or the search key on a phone) makes it a submitted search and drops the flag, as
 * does running a saved search. "Mark all results as read" is only offered for a submitted search, because a search
 * that is still being typed shows a wider set than the exact one the server would mark.
 */
export function SearchScreen() {
  const [sp, setSp] = useSearchParams();
  const navigate = useNavigate();
  const urlQ = sp.get("q") ?? "";
  const ssId = sp.get("ss");
  const [text, setText] = useState(urlQ);
  const [typing, setTyping] = useState(false);
  const [saving, setSaving] = useState(false);
  const [controls, setControls] = useState<ListControls | null>(null);
  const inputId = useId();
  const sortId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const prefs = useStore(prefsStore);
  const pref = useSearchOrder();
  const boot = useBootstrap();
  const saved = useSavedSearches();

  const { scope: where, order } = scopeFromSearchParams(sp, pref);
  const whereKey = `${where.view}|${where.feed ?? ""}|${where.folder ?? ""}`;

  /** Change the URL, keeping the scope and saved-search params unless told otherwise. */
  const setQuery = (q: string, drop: string[] = []) => {
    const next = new URLSearchParams(sp);
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
    setTyping(false);
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

  useEffect(() => {
    inputRef.current?.focus({ preventScroll: true });
  }, []);

  useHotkeys({ up: () => navigate(-1) }, { singleKeys: prefs.shortcuts });

  const submit = () => {
    pushed.current = text;
    setTyping(false);
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
  const folderName = boot.data?.folders.find((f) => f.id === where.folder)?.name;
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

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line bg-bg px-4 pb-3">
        <div className="flex items-center pt-2">
          <h1 className="min-w-0 flex-1 text-xl font-bold" tabIndex={-1} data-route-heading>
            Search
          </h1>
          <SearchHelp />
        </div>
        <form
          role="search"
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
            type="search"
            enterKeyHint="search"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="Search articles"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setTyping(true);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") inputRef.current?.blur();
            }}
            className="mt-2 min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg placeholder:text-fg2"
          />
        </form>
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
          <div className="flex items-center gap-2">
            <label htmlFor={sortId} className="text-sm text-fg2">
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
            <span className="inline-flex min-h-11 items-center gap-1 rounded-full border border-line bg-surface pr-1 pl-3 text-sm">
              <span className="max-w-40 truncate">In {chip}</span>
              <button
                type="button"
                aria-label="Search the whole library instead"
                className="hit inline-flex items-center justify-center rounded-full hover:bg-selection"
                onClick={() => setQuery(q, ["feed", "folder", "view", "ss"])}
              >
                <X className="size-4" aria-hidden="true" />
              </button>
            </span>
          ) : null}
        </div>
        {ready ? (
          <div className="mt-1 flex flex-wrap gap-x-2">
            <Button variant="ghost" onClick={() => setSaving(true)} className="px-2">
              <BookmarkPlus aria-hidden="true" />
              Save this search
            </Button>
            <Button
              variant="ghost"
              className="px-2"
              onClick={() => controls?.markAllRead()}
              disabled={!controls || controls.count === 0 || typing}
              title={typing ? "Press Enter to search first, then mark all results as read" : undefined}
            >
              <CheckCheck aria-hidden="true" />
              Mark all results as read
            </Button>
          </div>
        ) : null}
      </header>
      <div className="min-h-0 flex-1">
        {scope ? (
          <ListPane key={`${q}|${whereKey}|${order}`} scope={scope} onControls={setControls} />
        ) : (
          <StatusBlock
            role="status"
            title={trimmed.length === 0 ? "Search your articles" : "Keep typing"}
            body={trimmed.length === 0 ? "Search covers every article Kipple has kept." : "Search needs at least two characters."}
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
  );
}
