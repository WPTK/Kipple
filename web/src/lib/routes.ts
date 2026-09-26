import type { To } from "react-router";
import type { Scope, View } from "@/api/types";
import { parseScopeKey, scopeKey } from "@/api/queries";

/** List route for a scope: /l/:view?feed=&folder= */
export function listTo(scope: Scope): To {
  const sp = new URLSearchParams();
  if (scope.feed) sp.set("feed", scope.feed);
  if (scope.folder) sp.set("folder", scope.folder);
  const search = sp.toString();
  return { pathname: `/l/${scope.view}`, search: search ? `?${search}` : "" };
}

/** Article route. `from` carries the list it came from, so back returns to it. */
export function articleTo(id: string, scope: Scope): To {
  return { pathname: `/i/${id}`, search: `?${new URLSearchParams({ from: scopeKey(scope) }).toString()}` };
}

export function scopeFromSearch(search: URLSearchParams, fallback: Scope = { view: "unread" }): Scope {
  const from = search.get("from");
  return from ? parseScopeKey(from) : fallback;
}

export function scopeFromList(view: string | undefined, search: URLSearchParams): Scope {
  const v: View = view === "all" || view === "starred" || view === "muted" ? view : "unread";
  const s: Scope = { view: v };
  const feed = search.get("feed");
  const folder = search.get("folder");
  if (feed) s.feed = feed;
  else if (folder) s.folder = folder;
  return s;
}
