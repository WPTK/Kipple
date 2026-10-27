import { useCallback, useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLocation } from "react-router";
import { api } from "@/api/client";
import { keys } from "@/api/queryKeys";
import { useBootstrap } from "@/api/queries";
import type { Feed, Folder } from "@/api/types";
import { groupFeeds } from "@/layouts/digestPlan";
import { byFolder } from "./sections";
import { createStore, useStore } from "./store";
import { useLatestRef } from "./useLatestRef";

// The New page (docs/design.md, "The New page"). One mark for the account, kept on the server: the newest article the
// page has shown. A visit starts when the page opens (the server says where the mark is, and how many articles
// arrived after it in each section) and ends when the reader leaves, which moves the mark to the newest article the
// visit knew of. Opening one of the page's articles is not leaving it; closing the tab or reloading is not either, so
// the page then shows the same articles again.

/** Articles a section of the New page shows before "N more". */
export const NEW_ROWS = 3;

/** What one visit to the New page knows: the mark it started from and the newest article it knew of. */
export interface CatchupVisit {
  /** Which visit this is: a new visit under the same mark is a new page (digestSource). */
  id: number;
  /** The article id the account had seen up to when the visit began; the page lists what arrived after it. */
  mark: string;
  /** The newest article the server knew of then; leaving moves the mark here. */
  asOf: string;
  /** The visit began on an article of the page (its address) and the page itself has not been shown since: leaving it moves nothing. */
  adopted?: boolean;
  /** Articles that arrived after the mark, by section key (digestPlan). A section missing here has none. */
  counts: ReadonlyMap<string, number>;
}

interface CatchupAnswer {
  mark: string;
  as_of: string;
  sections: { group: string; count: number }[];
}

let visits = 0;
export const visitStore = createStore<CatchupVisit | null>(null);
/** Forget the visit without moving the mark (tests). */
export function resetCatchup(): void {
  visitStore.set(null);
  pendingSeen = Promise.resolve();
}

/** The last "seen" post: a visit that begins right after a leave waits for it, so it starts from the mark that leave set. */
let pendingSeen: Promise<unknown> = Promise.resolve();

/** A section key of digestPlan as the server names the group. */
const groupOf = (key: string): string => (key.startsWith("folder:") ? `d${key.slice(7)}` : `f${key.slice(5)}`);
const keyOf = (group: string): string => (group.startsWith("d") ? `folder:${group.slice(1)}` : `feed:${group.slice(1)}`);

/** The sections the New page has, as the server names them: each top-level folder, and any feed outside the folder tree. */
export function catchupGroups(feeds: readonly Pick<Feed, "id" | "title" | "folder_id" | "unread" | "starred_count" | "is_archive">[], folders: readonly Pick<Folder, "id" | "name" | "parent_id">[]): string[] {
  const defs = groupFeeds(feeds, folders, byFolder(folders, feeds, {}), "unread");
  return defs.map((d) => groupOf(d.key)).slice(0, 500);
}

/** The visit the leave of the page ends: the mark moves, and the next visit waits for that to land. */
function leave(v: CatchupVisit): void {
  visitStore.set(null);
  if (v.adopted || v.asOf === v.mark) return; // nothing to move: the page itself was not opened in an adopted visit, and this one has nothing past it
  pendingSeen = api("/api/catchup/seen", { method: "POST", body: { as_of: v.asOf } }).catch(() => {
    // The mark stays where it was: the page shows the same articles next time. Nothing the reader did is lost.
  });
}

/** Is `pathname` with `search` still the visit's page: the page itself, or an article opened from it. */
export function withinVisit(v: CatchupVisit, pathname: string, search: string): boolean {
  if (pathname === "/l/new") return true;
  if (!pathname.startsWith("/i/")) return false;
  return new URLSearchParams(search).get("from")?.split("|").includes(`after:${v.mark}`) ?? false;
}

/** Leaves the visit when the location is no longer its page. Mounted once, in the app shell. */
export function useCatchupLeave(): void {
  const { pathname, search } = useLocation();
  useEffect(() => {
    const v = visitStore.get();
    if (v && !withinVisit(v, pathname, search)) leave(v);
  }, [pathname, search]);
}

export type VisitState = { status: "loading" } | { status: "error"; error: unknown; retry: () => void } | { status: "ready"; visit: CatchupVisit };

/**
 * The visit of the New page while `active` (the page is open). It begins by asking the server for the mark and the
 * counts; the answer is kept for as long as the reader stays on the page or its articles.
 */
export function useCatchupVisit(active: boolean, adopt?: string): VisitState {
  const qc = useQueryClient();
  const visit = useStore(visitStore);
  const boot = useBootstrap();
  const ready = !!boot.data;
  const bootRef = useLatestRef(boot.data);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const retry = useCallback(() => {
    setFailure(null);
    setAttempt((n) => n + 1);
  }, []);
  // An earlier failure is not this visit's: the page opening again asks again.
  const [wasActive, setWasActive] = useState(active);
  if (wasActive !== active) {
    setWasActive(active);
    if (active) setFailure(null);
  }
  useEffect(() => {
    const b = bootRef.current;
    if (!active || visit || !ready || !b) return;
    let live = true;
    void pendingSeen
      .then(() => api<CatchupAnswer>("/api/catchup", { params: { groups: catchupGroups(b.feeds, b.folders).join(","), ...(adopt ? { mark: adopt } : {}) } }))
      .then((r) => {
        if (!live) return;
        // A visit is a fresh look: whatever an earlier visit under the same mark left in the cache is not shown.
        // `adopt` is a page opened on an article of the New page (a reload or a new tab): it carries the mark its link
        // names, the counts are asked for that mark, and leaving it moves nothing, so the page is still new on the next
        // visit (the reader going to the page itself ends it: see below).
        const mark = adopt ?? r.mark;
        const tail = `|after:${mark}`;
        if (!adopt) qc.removeQueries({ queryKey: keys.itemsAll, predicate: (q) => String(q.queryKey[1]).includes(tail) });
        visitStore.set({ id: ++visits, mark, asOf: r.as_of, adopted: !!adopt, counts: new Map(r.sections.map((s) => [keyOf(s.group), s.count])) });
      })
      .catch((error) => live && setFailure({ error }));
    return () => {
      live = false;
    };
  }, [active, visit, ready, attempt, qc, bootRef, adopt]);
  // The page itself opening over a visit that began on one of its articles (Back after a reload) is a new visit: the
  // adopted one holds the link's mark, which may be behind the account's, so the page asks again and its leaving moves
  // the mark like any other.
  useEffect(() => {
    if (active && !adopt && visit?.adopted) visitStore.set(null);
  }, [active, adopt, visit]);
  if (visit) return { status: "ready", visit };
  if (failure) return { status: "error", error: failure.error, retry };
  return { status: "loading" };
}
