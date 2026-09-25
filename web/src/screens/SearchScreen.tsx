import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import type { Scope } from "@/api/types";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { ListPane, StatusBlock } from "./ListPane";

/** Search across all articles (FTS on the server). Two characters minimum. */
export function SearchScreen() {
  const [sp, setSp] = useSearchParams();
  const navigate = useNavigate();
  const urlQ = sp.get("q") ?? "";
  const [text, setText] = useState(urlQ);
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const prefs = useStore(prefsStore);

  // Debounce the URL (and so the query) at 300 ms.
  useEffect(() => {
    const h = setTimeout(() => {
      const t = text.trim();
      if (t !== urlQ) setSp(t ? { q: t } : {}, { replace: true });
    }, 300);
    return () => clearTimeout(h);
  }, [text, urlQ, setSp]);

  useEffect(() => {
    inputRef.current?.focus({ preventScroll: true });
  }, []);

  useHotkeys({ up: () => navigate(-1) }, { singleKeys: prefs.shortcuts });

  const q = urlQ.trim();
  const scope: Scope | null = useMemo(() => (q.length >= 2 ? { view: "all", q } : null), [q]);

  const header = (
    <header className="pt-safe shrink-0 border-b border-line bg-bg px-4 pb-3">
      <h1 className="pt-2 text-xl font-bold" tabIndex={-1} data-route-heading>
        Search
      </h1>
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
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") inputRef.current?.blur();
        }}
        className="mt-2 min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg placeholder:text-fg2"
      />
    </header>
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      {header}
      <div className="min-h-0 flex-1">
        {scope ? (
          <ListPane key={q} scope={scope} />
        ) : (
          <StatusBlock
            role="status"
            title={q.length === 0 ? "Search your articles" : "Keep typing"}
            body={q.length === 0 ? "Search covers every article Kipple has kept." : "Search needs at least two characters."}
          />
        )}
      </div>
    </div>
  );
}
