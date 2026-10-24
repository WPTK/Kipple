import { useEffect, useRef } from "react";
import { Link, type To } from "react-router";
import { useLongPressMenu } from "@/gestures/useLongPressMenu";
import type { Card } from "@/api/types";
import { cn } from "@/lib/cn";
import { articleTo } from "@/lib/routes";
import { Button } from "@/ui/button";
import { compact } from "./compact";
import { ListImage, PublishedTime, RowMenu, UnreadDot, rowLinkProps, useImageFailure } from "./parts";
import { ROW_KEYSHORTCUTS, RowTabStop, stopId } from "./rowTabStop";
import type { DigestSectionView, ListLayout, PageProps, RowMenuActions } from "./types";

// The Digest (docs/maintainers/ui-decisions.md, "The Digest"). A page of sections, one for each folder (or feed): the
// section's name in the theme's accent, its first few unread articles, and "N more in <name>" to open it out in place.
// A section loads on its own, so one that is slow or failing never holds up the rest; it draws what digestSource.ts hands
// it, in that order, so the DOM order is the reading order. A read article keeps its place: its title turns to the
// secondary color (which meets 4.5:1 in every theme) and its picture fades.

interface RowProps {
  item: Card;
  to: To;
  onOpen: (item: Card) => void;
  actions: RowMenuActions;
  /** Show the picture on the right, when the article has one. */
  images: boolean;
  selected: boolean;
  /** This article's link is the page's one Tab stop. */
  tabStop: boolean;
}

/** An article: its title first, then its source and time, and a picture on the right when there is one and pictures are on. */
function DigestRow({ item, to, onOpen, actions, images, selected, tabStop }: RowProps) {
  const press = useLongPressMenu(item.id);
  const failure = useImageFailure(item.image);
  const unread = !item.read;
  const picture = images && !!item.image;
  return (
    <article
      data-item-id={item.id}
      data-read={item.read || undefined}
      data-selected={selected || undefined}
      {...press}
      // The menu button is laid over the row's corner, so it takes no room (and a hidden one none at all).
      className={cn(
        "row-host relative flex min-h-[var(--row-min)] items-start gap-3 border-b border-line py-[var(--row-py)] pr-4 pl-3 [-webkit-touch-callout:none]",
        selected ? "bg-selection" : "hover:bg-surface",
      )}
    >
      <UnreadDot unread={unread} className="mt-2" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <h3 className={cn("font-reading text-base leading-snug", unread ? "font-bold text-fg" : "font-normal text-fg2")}>
          <Link
            to={to}
            tabIndex={tabStop ? undefined : -1}
            onClick={() => onOpen(item)}
            aria-keyshortcuts={ROW_KEYSHORTCUTS}
            {...rowLinkProps(item)}
          >
            {item.title || "Untitled"}
          </Link>
        </h3>
        <p className="flex items-center gap-1.5 text-xs text-fg2">
          <span className="truncate">{item.source}</span>
          <span aria-hidden="true">·</span>
          <PublishedTime item={item} className="shrink-0" />
        </p>
      </div>
      {picture ? <ListImage src={item.image!} failure={failure} className={cn("size-16 shrink-0 rounded-lg bg-surface object-cover", item.read && "opacity-60")} /> : null}
      <RowTabStop.Provider value={tabStop}>
        <RowMenu item={item} actions={actions} revealOnHover className="row-touch-unseen absolute right-1 bottom-1 bg-bg/80" />
      </RowTabStop.Provider>
    </article>
  );
}

/** A button between the articles that the arrow keys visit, like an article, and Tab does not (the page is one Tab stop). */
function Stop({ id, children }: { id: string; children: React.ReactNode }) {
  return (
    <div data-stop-id={id} className="flex justify-start px-1 py-1">
      {children}
    </div>
  );
}

function SectionSkeleton({ rows }: { rows: number }) {
  return (
    <div aria-hidden="true" data-testid="digest-skeleton">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex min-h-[var(--row-min)] flex-col gap-2 border-b border-line px-4 py-3">
          <div className="h-4 w-11/12 rounded bg-surface" />
          <div className="h-3 w-1/3 rounded bg-surface" />
        </div>
      ))}
    </div>
  );
}

/** "7 more in Tech", or "More in Tech" where the number is not known. */
export const moreLabel = (s: Pick<DigestSectionView, "title" | "moreCount">): string => (s.moreCount ? `${s.moreCount} more in ${s.title}` : `More in ${s.title}`);

interface SectionProps {
  section: DigestSectionView;
  rows: number;
  images: boolean;
  scope: PageProps["scope"];
  onOpen: (item: Card) => void;
  actions: RowMenuActions;
  selectedId: string | undefined;
  tabStopId: string | undefined;
  expand: (key: string) => void;
  retry: (key: string) => void;
}

function DigestSection({ section: s, rows, images, scope, onOpen, actions, selectedId, tabStopId, expand, retry }: SectionProps) {
  const box = useRef<HTMLElement>(null);
  // Pressing "N more" or Retry takes its button away (or moves it): focus goes to the first article the press brought in.
  // The press is forgotten once its request has settled without bringing any (a failed Retry), or after a while, so a
  // later refetch never takes focus from where the reader has gone.
  const from = useRef<{ at: number; started: boolean } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => {
    const f = from.current;
    if (!f) return;
    if (s.cards.length > f.at) {
      const id = s.cards[f.at]!.id;
      from.current = null;
      box.current?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"] a`)?.focus({ preventScroll: true });
    } else if (s.busy) f.started = true;
    else if (f.started) from.current = null;
  });
  useEffect(() => () => clearTimeout(timer.current), []);
  // A press while the section is already loading is ignored (aria-disabled does not stop a click).
  const press = (act: (key: string) => void) => {
    if (s.loadingMore || (s.state === "loading" && s.error == null)) return;
    from.current = { at: s.cards.length, started: false };
    clearTimeout(timer.current);
    timer.current = setTimeout(() => (from.current = null), 10_000);
    act(s.key);
  };
  return (
    // A plain section with no accessible name, so a long page does not add a landmark for each; its h2 is what heading navigation finds.
    <section ref={box} data-section={s.key} className="pt-4">
      <h2 className="px-4 pb-1 text-sm font-semibold tracking-wide text-accent uppercase">{s.title}</h2>
      {s.error != null ? (
        <div role="alert" className="flex items-center gap-2 border-b border-line px-4 py-2 text-sm text-fg2">
          <span>Couldn&apos;t load {s.title}.</span>
          <Stop id={stopId("retry", s.key)}>
            <Button variant="ghost" tabIndex={-1} aria-keyshortcuts={ROW_KEYSHORTCUTS} onClick={() => press(retry)}>
              Retry
            </Button>
          </Stop>
        </div>
      ) : s.state === "loading" ? (
        <SectionSkeleton rows={Math.max(1, Math.min(rows, s.moreCount ?? rows))} />
      ) : (
        s.cards.map((item) => (
          <DigestRow
            key={item.id}
            item={item}
            to={articleTo(item.id, scope)}
            onOpen={onOpen}
            actions={actions}
            images={images}
            selected={item.id === selectedId}
            tabStop={item.id === tabStopId}
          />
        ))
      )}
      {s.state === "ready" && s.moreFailed ? (
        <div role="alert" className="flex items-center gap-2 px-4 py-1 text-sm text-fg2">
          <span>Couldn&apos;t load more of {s.title}.</span>
          <Stop id={stopId("retry", s.key)}>
            <Button variant="ghost" tabIndex={-1} aria-keyshortcuts={ROW_KEYSHORTCUTS} onClick={() => press(retry)}>
              Retry
            </Button>
          </Stop>
        </div>
      ) : s.state === "ready" && s.hasMore ? (
        <Stop id={stopId("more", s.key)}>
          <Button variant="ghost" tabIndex={-1} aria-keyshortcuts={ROW_KEYSHORTCUTS} aria-busy={s.loadingMore || undefined} aria-disabled={s.loadingMore || undefined} onClick={() => press(expand)} className="text-link">
            {s.loadingMore ? "Loading" : moreLabel(s)}
          </Button>
        </Stop>
      ) : null}
    </section>
  );
}

/**
 * The Digest as a page layout: the sections digestSource.ts asks for and loads, in order. The page keeps one Tab stop
 * (the article that last had focus, else the selected one, else the first); its "N more" and Retry buttons are reached
 * with the arrow keys, between the articles, in the order they are drawn.
 */
function DigestPage({ items, scope, selectedId, tabStopId, onOpen, actions, digest }: PageProps) {
  if (!digest) return null;
  const present = new Set(items.map((i) => i.id));
  const stop = [tabStopId, selectedId].find((id) => id && present.has(id)) ?? items[0]?.id;
  return (
    <div data-digest="" className="bg-bg pb-10 text-fg">
      {digest.sections.map((s) => (
        <DigestSection
          key={s.key}
          section={s}
          rows={digest.rows}
          images={digest.images}
          scope={scope}
          onOpen={onOpen}
          actions={actions}
          selectedId={selectedId}
          tabStopId={stop}
          expand={digest.expand}
          retry={digest.retry}
        />
      ))}
    </div>
  );
}

/** In a search, which has no sections, the Digest shows Compact rows. */
export const digest: ListLayout = { ...compact, id: "digest", label: "Digest", Page: DigestPage };
