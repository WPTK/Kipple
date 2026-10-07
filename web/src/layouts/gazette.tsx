import { Link, type To } from "react-router";
import type { Card } from "@/api/types";
import { cn } from "@/lib/cn";
import type { Block, GazettePlan, PagePlan, Slot } from "./gazettePlan";
import { PublishedTime, rowLabel } from "./parts";

// The Gazette's pages (docs/ui-decisions.md, "The Gazette"). It draws what planGazette returns, in that order: a page
// is a stack of blocks, a block is a grid of columns, and each column lists its slots top to bottom, so the DOM order
// is the reading order. Colors are theme tokens only (rules and section marks use the theme's accent). A read story
// keeps its weight and size: its picture fades and its text turns to the secondary color (which meets 4.5:1 on the
// background in every theme), so marking one read never moves the page. A broken picture keeps its box for the same
// reason.

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
}

/** The name to print: the given one trimmed, or the default when blank. */
export const paperName = (name: string) => name.trim() || DEFAULT_PAPER_NAME;

/** The closing line for a printed name: "That's the Gazette." for "The Gazette", "That's Morning Notes." otherwise. */
export function closingLine(name: string): string {
  return `That's ${/^the\s/i.test(name) ? `the${name.slice(3)}` : name}.`;
}

const longDate = (d: Date) => d.toLocaleDateString(undefined, { weekday: "long", year: "numeric", month: "long", day: "numeric" });
const shortDate = (d: Date) => d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });

export function Gazette({ plan, items, name: given, date, screen, to, onOpen }: GazetteProps) {
  const phone = screen === "phone";
  const name = paperName(given);
  const last = plan.pages.length - 1;
  return (
    <div data-gazette="" className="gazette bg-bg px-4 pb-10 text-fg">
      {plan.pages.map((page, i) => (
        <GazettePage key={page.number} page={page} items={items} name={name} date={date} phone={phone} to={to} onOpen={onOpen} ruled={i > 0} />
      ))}
      {plan.complete && last >= 0 ? (
        <p className="mt-8 border-t-[3px] border-double border-fg pt-4 text-center font-reading text-lg italic">{closingLine(name)}</p>
      ) : null}
    </div>
  );
}

interface PageProps extends Omit<GazetteProps, "plan" | "screen"> {
  page: PagePlan;
  phone: boolean;
  /** Draw the page rule above it (every page after the first). */
  ruled: boolean;
}

function GazettePage({ page, items, name, date, phone, to, onOpen, ruled }: PageProps) {
  const front = page.kind === "front";
  return (
    // A plain section with no accessible name, so a long paper does not add one landmark per page; its h2 is what
    // heading navigation finds.
    <section data-page={page.number} data-front-type={page.type ?? undefined} className={cn(ruled && "mt-8 border-t-[3px] border-double border-fg pt-2")}>
      {front ? <Masthead name={name} date={date} phone={phone} /> : <Folio name={name} date={date} number={page.number} phone={phone} />}
      <div className="flex flex-col gap-6">
        {page.blocks.map((b, i) => (
          <GazetteBlock key={i} block={b} items={items} to={to} onOpen={onOpen} phone={phone} />
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
  items: ReadonlyMap<string, Card>;
  to: (item: Card) => To;
  onOpen: (item: Card) => void;
  phone: boolean;
}

function GazetteBlock({ block, items, to, onOpen, phone }: BlockProps) {
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
              <Story key={s.id} slot={s} item={items.get(s.id)!} heading={titled ? "h4" : "h3"} to={to(items.get(s.id)!)} onOpen={onOpen} phone={phone} />
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
  phone: boolean;
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

function Story({ slot, item, heading: H, to, onOpen, phone }: StoryProps) {
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
      className={cn("relative flex min-w-0 flex-col gap-1.5", brief && "border-b border-line pb-2")}
    >
      {picture ? (
        // The box holds the picture's place, so a picture that fails to load leaves an empty box, not a gap.
        <div className={cn("w-full bg-surface", slot.kind === "photo" ? "aspect-[4/3]" : "aspect-video")}>
          <img
            src={item.image!}
            alt=""
            loading="lazy"
            decoding="async"
            className={cn("size-full object-cover", item.read && "opacity-60")}
            onError={(e) => {
              e.currentTarget.style.visibility = "hidden";
            }}
          />
        </div>
      ) : null}
      <H className={cn("font-reading font-bold", headlineClass(slot, phone), item.read ? "text-fg2" : "text-fg")}>
        <Link
          to={to}
          onClick={() => onOpen(item)}
          aria-label={rowLabel(item)}
          className="after:absolute after:inset-0 hover:underline focus-visible:after:outline-2 focus-visible:after:outline-accent"
        >
          {item.title || "Untitled"}
        </Link>
      </H>
      {standfirst && item.excerpt ? <p className={cn(standfirst, "font-reading text-base leading-normal text-fg2")}>{item.excerpt}</p> : null}
      <p className="flex items-center gap-1.5 text-xs text-fg2">
        <span className="truncate">{item.source}</span>
        {brief ? null : (
          <>
            <span aria-hidden="true">·</span>
            <PublishedTime item={item} className="shrink-0" />
          </>
        )}
      </p>
    </article>
  );
}
