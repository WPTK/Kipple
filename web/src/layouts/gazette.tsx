import { useMemo, useState } from "react";
import { Link, type To } from "react-router";
import { useLongPressMenu } from "@/gestures/useLongPressMenu";
import { useBootstrap } from "@/api/queries";
import type { Card } from "@/api/types";
import { cn } from "@/lib/cn";
import { useDevicePrefs } from "@/lib/devicePrefs";
import { useFavorites } from "@/lib/favorites";
import { articleTo } from "@/lib/routes";
import { planGazette, type Block, type GazettePlan, type PagePlan, type Slot } from "./gazettePlan";
import { magazine } from "./magazine";
import { ROW_KEYSHORTCUTS } from "./rowTabStop";
import { ListImage, useImageFailure, PublishedTime, RowMenu, UnreadDot, rowLinkProps, titleTone, useOneFeed } from "./parts";
import { RowTabStop } from "./rowTabStop";
import type { ListLayout, PageProps, RowMenuActions } from "./types";

// The Gazette's pages (docs/maintainers/ui-decisions.md, "The Gazette"). It draws what planGazette returns, in that order: a page
// is a stack of blocks, a block is a grid of columns, and each column lists its slots top to bottom, so the DOM order
// is the reading order. Colors are theme tokens only (rules and section marks use the theme's accent). A read story
// keeps its weight and size: its picture fades and its text turns to the secondary color (which meets 4.5:1 on the
// background in every theme), so marking one read never moves the page. A picture that cannot be shown is dropped with its box, and its story is
// laid out as the story it is without one (a lead as a headline lead, a photo as a story).

/** The paper's name when none is set. */
export const DEFAULT_PAPER_NAME = "The Gazette";

export interface GazetteProps {
  plan: GazettePlan;
  /** The loaded articles by id. A slot whose article is missing is skipped. */
  items: ReadonlyMap<string, Card>;
  /** The paper's name, shown in the masthead and on every folio line. Blank means DEFAULT_PAPER_NAME. */
  name: string;
  /** The edition's date. */
  date: Date;
  /** The planner's screen class: a phone gets one column, a compact masthead and no folio row. */
  screen: "wide" | "phone";
  /** Where a story's link goes (the article route). */
  to: (item: Card) => To;
  onOpen: (item: Card) => void;
  /** What a story's menu does: a long press on touch, the More button or the context menu with a pointer. */
  actions: RowMenuActions;
  /** The story selected (arrow keys) or open in the reader pane: it is outlined. */
  selectedId?: string;
  /** The story that last had focus: the paper's one Tab stop when it is still there. */
  focusedId?: string;
}

/** The name to print: the given one trimmed, or the default when blank. */
export const paperName = (name: string) => name.trim() || DEFAULT_PAPER_NAME;

/** The closing line for a printed name: "That's the Gazette." for "The Gazette", "That's Morning Notes." otherwise. */
export function closingLine(name: string): string {
  return `That's ${/^the\s/i.test(name) ? `the${name.slice(3)}` : name}.`;
}

const longDate = (d: Date) => d.toLocaleDateString(undefined, { weekday: "long", year: "numeric", month: "long", day: "numeric" });
const shortDate = (d: Date) => d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });

export function Gazette({ plan, items, name: given, date, screen, to, onOpen, actions, selectedId, focusedId }: GazetteProps) {
  const phone = screen === "phone";
  const name = paperName(given);
  const last = plan.pages.length - 1;
  // The paper is one Tab stop, like a list (rowTabStop.ts): the story that last had focus, else the selected one, else
  // the first one in reading order.
  const tabStopId = [focusedId, selectedId].find((id) => id && items.has(id)) ?? firstStory(plan, items);
  return (
    <div data-gazette="" className="gazette bg-bg px-4 pb-10 text-fg">
      {plan.pages.map((page, i) => (
        <GazettePage
          key={page.number}
          page={page}
          items={items}
          name={name}
          date={date}
          phone={phone}
          to={to}
          onOpen={onOpen}
          actions={actions}
          selectedId={selectedId}
          tabStopId={tabStopId}
          ruled={i > 0}
        />
      ))}
      {plan.complete && last >= 0 ? (
        <p className="mt-8 border-t-[3px] border-double border-fg pt-4 text-center font-reading text-lg italic">{closingLine(name)}</p>
      ) : null}
    </div>
  );
}

/** The paper's first story in reading order (page by page, block by block, column by column). */
function firstStory(plan: GazettePlan, items: ReadonlyMap<string, Card>): string | undefined {
  for (const page of plan.pages) {
    for (const block of page.blocks) {
      const shown = block.slots.filter((s) => items.has(s.id));
      if (shown.length) return shown.reduce((a, b) => (b.column < a.column ? b : a)).id;
    }
  }
  return undefined;
}

interface PagePlanProps extends Omit<GazetteProps, "plan" | "screen"> {
  page: PagePlan;
  phone: boolean;
  /** The one story whose link is in the Tab order. */
  tabStopId: string | undefined;
  /** Draw the page rule above it (every page after the first). */
  ruled: boolean;
}

function GazettePage({ page, items, name, date, phone, to, onOpen, actions, selectedId, tabStopId, ruled }: PagePlanProps) {
  const front = page.kind === "front";
  return (
    // A plain section with no accessible name, so a long paper does not add one landmark per page; its h2 is what
    // heading navigation finds.
    <section data-page={page.number} data-front-type={page.type ?? undefined} className={cn(ruled && "mt-8 border-t-[3px] border-double border-fg pt-2")}>
      {front ? <Masthead name={name} date={date} phone={phone} /> : <Folio name={name} date={date} number={page.number} phone={phone} />}
      <div className="flex flex-col gap-6">
        {page.blocks.map((b, i) => (
          <GazetteBlock key={i} block={b} items={items} to={to} onOpen={onOpen} actions={actions} phone={phone} selectedId={selectedId} tabStopId={tabStopId} />
        ))}
      </div>
    </section>
  );
}

function Masthead({ name, date, phone }: { name: string; date: Date; phone: boolean }) {
  return (
    <header className="mb-5 border-b-[3px] border-double border-fg pt-4 pb-2 text-center">
      <h2 className={cn("font-reading leading-none font-bold tracking-tight", phone ? "text-3xl" : "text-6xl")}>
        {name}
      </h2>
      <p className="mt-2 border-t border-accent pt-1 text-xs tracking-wide text-fg2 uppercase">{longDate(date)}</p>
    </header>
  );
}

/** An inner page's header line: name, page number, date. A phone has no folio row; the page keeps a hidden heading. */
function Folio({ name, date, number, phone }: { name: string; date: Date; number: number; phone: boolean }) {
  if (phone) {
    return (
      <h2 className="sr-only">
        Page {number}
      </h2>
    );
  }
  return (
    <header className="mb-4 grid grid-cols-3 items-baseline border-b border-line pb-1 text-xs tracking-wide text-fg2 uppercase">
      <span className="truncate font-reading normal-case italic">{name}</span>
      <h2 className="text-center font-normal">
        Page {number}
      </h2>
      <span className="text-right">{shortDate(date)}</span>
    </header>
  );
}

interface BlockProps {
  block: Block;
  tabStopId: string | undefined;
  items: ReadonlyMap<string, Card>;
  to: (item: Card) => To;
  onOpen: (item: Card) => void;
  actions: RowMenuActions;
  phone: boolean;
  selectedId: string | undefined;
}

function GazetteBlock({ block, items, to, onOpen, actions, phone, selectedId, tabStopId }: BlockProps) {
  const columns = phone ? 1 : block.columns;
  const byColumn: Slot[][] = Array.from({ length: columns }, () => []);
  for (const s of block.slots) if (items.has(s.id)) byColumn[Math.min(s.column, columns - 1)]!.push(s);
  if (!byColumn.some((c) => c.length)) return null;
  const titled = block.title !== null;
  return (
    <div>
      {titled ? (
        <h3 className="mb-3 border-t-2 border-accent pt-1 text-xs font-bold tracking-widest text-fg uppercase">{block.title}</h3>
      ) : null}
      <div
        className="grid gap-x-5 gap-y-4"
        style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}
      >
        {byColumn.map((slots, c) => (
          <div key={c} className={cn("flex min-w-0 flex-col gap-4", c > 0 && "border-l border-line pl-5")}>
            {slots.map((s) => (
              <Story
                key={s.id}
                slot={s}
                item={items.get(s.id)!}
                heading={titled ? "h4" : "h3"}
                to={to(items.get(s.id)!)}
                onOpen={onOpen}
                actions={actions}
                phone={phone}
                selected={s.id === selectedId}
                tabStop={s.id === tabStopId}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

interface StoryProps {
  slot: Slot;
  item: Card;
  heading: "h3" | "h4";
  to: To;
  onOpen: (item: Card) => void;
  actions: RowMenuActions;
  phone: boolean;
  selected: boolean;
  /** This story's link is the paper's one Tab stop. */
  tabStop: boolean;
}

/** Headline sizes by slot: a picture lead about 26 px, a headline lead about 40 px (smaller on a phone). */
function headlineClass(slot: Slot, phone: boolean): string {
  switch (slot.kind) {
    case "lead":
    case "co-lead":
      return slot.picture ? "text-[1.625rem] leading-tight" : phone ? "text-[1.75rem] leading-tight" : "text-[2.5rem] leading-[1.1]";
    case "photo":
      return "text-base leading-snug";
    case "brief":
      return "text-sm leading-snug";
    default:
      return "text-lg leading-snug";
  }
}

function Story({ slot: planned, item, heading: H, to, onOpen, actions, phone, selected, tabStop }: StoryProps) {
  const press = useLongPressMenu(item.id);
  const oneFeed = useOneFeed();
  const failure = useImageFailure(item.image);
  // A picture that cannot be shown takes the slot back to what it is without one: a lead without a picture is the
  // headline lead, a photo cell is an ordinary story with its standfirst.
  const slot: Slot = !failure.failed ? planned : planned.kind === "photo" ? { ...planned, kind: "story" } : { ...planned, picture: false };
  const lead = slot.kind === "lead" || slot.kind === "co-lead";
  const picture = !!item.image && (slot.kind === "photo" || (lead && slot.picture));
  const brief = slot.kind === "brief";
  // A lead without a picture runs a longer standfirst; a story a short one; photos and briefs none.
  const standfirst = lead ? (slot.picture ? "clamp-snippet" : "line-clamp-5") : slot.kind === "story" ? "line-clamp-3" : null;
  return (
    <article
      data-item-id={item.id}
      data-slot={slot.kind}
      data-read={item.read || undefined}
      data-selected={selected || undefined}
      {...press}
      // The selection is an outline outside the story's box, so selecting one never moves the page. The story is a row
      // host for the menu button, and holds off the browser's own touch menu for the long press.
      className={cn("row-host relative flex min-w-0 flex-col gap-1.5 [-webkit-touch-callout:none]", brief && "border-b border-line pb-2", selected && "outline-2 outline-offset-4 outline-accent")}
    >
      {picture ? (
        <ListImage
          src={item.image!}
          failure={failure}
          shaped
          frame={{ className: cn("w-full bg-surface", slot.kind === "photo" ? "aspect-[4/3]" : "aspect-video") }}
          className={cn("size-full", item.read && "opacity-60")}
        />
      ) : null}
      <H className={cn("font-reading", headlineClass(slot, phone), titleTone(!item.read))}>
        <Link
          to={to}
          tabIndex={tabStop ? undefined : -1}
          onClick={() => onOpen(item)}
          aria-keyshortcuts={ROW_KEYSHORTCUTS}
          {...rowLinkProps(item, "hover:underline")}
        >
          {item.title || "Untitled"}
        </Link>
      </H>
      {standfirst && item.excerpt ? <p className={cn(standfirst, "font-reading text-base leading-normal break-words text-fg2")}>{item.excerpt}</p> : null}
      <p className="flex items-center gap-1.5 text-xs text-fg2">
        <UnreadDot unread={!item.read} />
        {oneFeed ? null : <span className="truncate">{item.source}</span>}
        {brief ? null : (
          <>
            {oneFeed ? null : <span aria-hidden="true">·</span>}
            <PublishedTime item={item} className="shrink-0" />
          </>
        )}
      </p>
      {/* Laid over the story's corner, so the menu never takes room on the page (a story keeps its place whatever it does). */}
      <RowTabStop.Provider value={tabStop}>
        <RowMenu item={item} actions={actions} ranges={false} revealOnHover className="row-touch-unseen absolute right-0 bottom-0 bg-bg/80" />
      </RowTabStop.Provider>
    </article>
  );
}

/** A list narrower than this (a phone, or the column beside the reader pane) gets the phone page. */
export const PHONE_BELOW = 600;

/**
 * The Gazette as a list layout: it plans the loaded list and draws the pages. The edition's date is the time the list
 * was opened. Until the planner has a front page (it waits for the lead window, or the end of the list) it says the
 * front page is being laid out; the list screen keeps loading meanwhile.
 */
function GazetteList({ items, scope, more, width, selectedId, tabStopId, onOpen, actions }: PageProps) {
  const boot = useBootstrap();
  const { favorites } = useFavorites();
  const { paperName: name } = useDevicePrefs();
  const [date] = useState(() => new Date());
  const screen = width < PHONE_BELOW ? "phone" : "wide";
  const feeds = boot.data?.feeds;
  const folders = boot.data?.folders;
  const { feed, folder } = scope;
  const plan = useMemo(
    () => planGazette({ articles: items, feeds: feeds ?? [], folders: folders ?? [], favorites, scope: { feed, folder }, screen, more }),
    [items, feeds, folders, favorites, feed, folder, screen, more],
  );
  const byId = useMemo(() => new Map(items.map((i) => [i.id, i])), [items]);
  // Nothing is laid out for real before the list has a width.
  if (width === 0) return null;
  if (!plan.pages.length) {
    return (
      <p role="status" className="px-4 py-16 text-center font-reading text-lg text-fg2 italic">
        Laying out the front page
      </p>
    );
  }
  return <Gazette plan={plan} items={byId} name={name} date={date} screen={screen} to={(c) => articleTo(c.id, scope)} onOpen={onOpen} actions={actions} selectedId={selectedId} focusedId={tabStopId} />;
}

/** A search is never a paper (a paper is planned from a newest-first list): it reads in the results rows. */
export const gazette: ListLayout = { ...magazine, id: "gazette", label: "Gazette", Page: GazetteList };
