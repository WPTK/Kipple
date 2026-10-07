import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { axe } from "vitest-axe";
import type { Card } from "@/api/types";
import { card } from "@/test/mockApi";
import { contrast } from "@/theme/contrast";
import { SCHEMES } from "@/theme/schemes";
import { closingLine, DEFAULT_PAPER_NAME, Gazette, paperName } from "./gazette";
import { planGazette, type GazettePlan } from "./gazettePlan";

const IMG = "https://example.com/i.jpg";
const feeds = [
  { id: "1", title: "Example Feed", folder_id: "news" },
  { id: "2", title: "Second Feed", folder_id: "news" },
];
const folders = [{ id: "news", name: "News", parent_id: null }];
const DATE = new Date(2026, 9, 7, 9, 0);

function plan(cards: Card[], over: { screen?: "wide" | "phone"; more?: boolean; favorites?: { t: "feed" | "folder"; id: string }[] } = {}): GazettePlan {
  return planGazette({
    articles: cards,
    feeds,
    folders,
    favorites: over.favorites ?? [],
    scope: {},
    screen: over.screen ?? "wide",
    more: over.more ?? false,
  });
}

function draw(cards: Card[], p: GazettePlan, screenClass: "wide" | "phone" = "wide", onOpen = vi.fn()) {
  const items = new Map(cards.map((c) => [c.id, c]));
  return render(
    <MemoryRouter>
      <Gazette plan={p} items={items} name="The Gazette" date={DATE} screen={screenClass} to={(c) => `/item/${c.id}`} onOpen={onOpen} />
    </MemoryRouter>,
  );
}

/** Ids in DOM order. */
const domOrder = (root: HTMLElement) => [...root.querySelectorAll("article[data-item-id]")].map((a) => a.getAttribute("data-item-id"));
/** Ids in the planner's reading order. */
const planOrder = (p: GazettePlan) => p.pages.flatMap((pg) => pg.blocks.flatMap((b) => [...b.slots].sort((x, y) => x.column - y.column).map((s) => s.id)));

describe("Gazette pages", () => {
  // A front page with a picture lead from a favorite, then two inner pages and the closing briefs page.
  const cards = Array.from({ length: 70 }, (_, i) => card(i + 1, { image: i % 4 === 0 ? IMG : null, feed_id: i % 2 ? "2" : "1" }));
  const p = plan(cards, { favorites: [{ t: "feed", id: "1" }] });

  it("draws every planned story once, in reading order (column by column)", () => {
    const { container } = draw(cards, p);
    const order = domOrder(container);
    expect(order).toEqual(planOrder(p));
    expect(new Set(order).size).toBe(cards.length);
  });

  it("has a masthead on the front page, a folio line on inner pages and the closing line", () => {
    draw(cards, p);
    expect(screen.getByRole("heading", { level: 2, name: "The Gazette" })).toBeInTheDocument();
    expect(screen.getByText(DATE.toLocaleDateString(undefined, { weekday: "long", year: "numeric", month: "long", day: "numeric" }))).toBeInTheDocument();
    for (let n = 2; n <= p.pages.length; n++) expect(screen.getByRole("heading", { level: 2, name: `Page ${n}` })).toBeInTheDocument();
    expect(screen.getByText("That's the Gazette.")).toBeInTheDocument();
    // Pages are found by their headings; a long paper adds no landmark per page.
    expect(screen.queryAllByRole("region")).toHaveLength(0);
  });

  it("prints the default name for a blank one, in the masthead and the closing line", () => {
    const items = new Map(cards.map((c) => [c.id, c]));
    render(
      <MemoryRouter>
        <Gazette plan={p} items={items} name="   " date={DATE} screen="wide" to={(c) => `/item/${c.id}`} onOpen={vi.fn()} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { level: 2, name: DEFAULT_PAPER_NAME })).toBeInTheDocument();
    expect(screen.getByText("That's the Gazette.")).toBeInTheDocument();
  });

  it("shows the lead's picture and a headline-sized title", () => {
    const { container } = draw(cards, p);
    const lead = container.querySelector('article[data-slot="lead"]')!;
    expect(lead.querySelector("img")).not.toBeNull();
    expect(lead.querySelector("h3")!.className).toContain("text-[1.625rem]");
  });

  it("grows a lead without a picture to a big headline", () => {
    const text = Array.from({ length: 10 }, (_, i) => card(i + 1));
    const tp = plan(text);
    expect(tp.pages[0]!.type).toBe("text-only");
    const { container } = draw(text, tp);
    const lead = container.querySelector('article[data-slot="lead"]')!;
    expect(lead.querySelector("img")).toBeNull();
    expect(lead.querySelector("h3")!.className).toContain("text-[2.5rem]");
  });

  it("fades a read story in place: the picture dims, the text turns secondary, nothing that sets size changes", () => {
    // Every class of every element in a story, minus the text colors that tell read from unread.
    const shape = (root: Element) =>
      [root, ...root.querySelectorAll("*")].map((el) => [el.tagName, ...[...el.classList].filter((c) => c !== "text-fg" && c !== "text-fg2").sort()].join(" "));
    const { container, rerender } = draw(cards, p);
    const before = new Map(cards.map((c) => [c.id, shape(container.querySelector(`article[data-item-id="${c.id}"]`)!)]));
    const read = cards.map((c) => ({ ...c, read: true }));
    rerender(
      <MemoryRouter>
        <Gazette plan={p} items={new Map(read.map((c) => [c.id, c]))} name="The Gazette" date={DATE} screen="wide" to={(c) => `/item/${c.id}`} onOpen={vi.fn()} />
      </MemoryRouter>,
    );
    for (const c of read) {
      const el = container.querySelector(`article[data-item-id="${c.id}"]`)!;
      // Only the picture's opacity may differ, and opacity does not change size.
      expect(shape(el).map((s) => s.replace(" opacity-60", ""))).toEqual(before.get(c.id));
      expect(el.className).not.toMatch(/opacity|transition/);
      expect(el.querySelector("h3, h4")!.classList).toContain("text-fg2");
      expect(el.querySelector("h3, h4")!.classList).toContain("font-bold");
      const img = el.querySelector("img");
      if (img) expect(img.classList).toContain("opacity-60");
    }
    expect(domOrder(container)).toEqual(planOrder(p));
  });

  it("names unread stories as unread", () => {
    const { container } = draw(cards, p);
    const unread = container.querySelector(`article[data-item-id="${cards[0]!.id}"]`)!;
    expect(within(unread as HTMLElement).getByRole("link").getAttribute("aria-label")).toMatch(/^Unread, /);
    expect(unread.querySelector("h3")!.classList).toContain("text-fg");
  });

  it("keeps a broken picture's box", () => {
    const { container } = draw(cards, p);
    const img = container.querySelector<HTMLImageElement>('article[data-slot="lead"] img')!;
    fireEvent.error(img);
    expect(img.style.visibility).toBe("hidden");
    expect(img.style.display).toBe("");
    expect(img.parentElement!.className).toContain("aspect-video");
  });

  it("passes axe with read and unread stories", async () => {
    const mixed = cards.map((c, i) => (i % 3 === 0 ? { ...c, read: true } : c));
    const { container } = draw(mixed, p);
    expect(await axe(container)).toHaveNoViolations();
  });

  it("opens a story through its link", async () => {
    const onOpen = vi.fn();
    draw(cards, p, "wide", onOpen);
    const link = screen.getByRole("link", { name: `Unread, ${cards[0]!.title}, Example Feed` });
    expect(link.getAttribute("href")).toBe(`/item/${cards[0]!.id}`);
    await userEvent.click(link);
    expect(onOpen).toHaveBeenCalledWith(cards[0]);
  });

  it("has no closing line while the server has more", () => {
    const more = Array.from({ length: 130 }, (_, i) => card(i + 1));
    draw(more, plan(more, { more: true }));
    expect(screen.queryByText(/^That's/)).toBeNull();
  });

  it("skips a slot whose article is not loaded", () => {
    const items = cards.slice(1);
    const { container } = draw(items, p);
    expect(domOrder(container)).toEqual(planOrder(p).filter((id) => id !== cards[0]!.id));
  });
});

describe("Gazette on a phone", () => {
  const cards = Array.from({ length: 70 }, (_, i) => card(i + 1, { image: i % 4 === 0 ? IMG : null }));
  const p = plan(cards, { screen: "phone" });

  it("draws one column, a compact masthead and no folio row", () => {
    const { container } = draw(cards, p, "phone");
    for (const grid of container.querySelectorAll<HTMLElement>(".grid")) {
      if (grid.style.gridTemplateColumns) expect(grid.style.gridTemplateColumns).toBe("repeat(1, minmax(0, 1fr))");
    }
    expect(screen.getByRole("heading", { level: 2, name: "The Gazette" }).className).toContain("text-3xl");
    // Inner pages keep a heading for screen readers but draw no folio line.
    expect(screen.getByRole("heading", { level: 2, name: "Page 2" }).className).toContain("sr-only");
    expect(screen.queryByText(DATE.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }))).toBeNull();
    expect(domOrder(container)).toEqual(planOrder(p));
  });

  it("passes axe with read stories", async () => {
    const mixed = cards.map((c, i) => (i % 3 === 0 ? { ...c, read: true } : c));
    const { container } = draw(mixed, p, "phone");
    expect(await axe(container)).toHaveNoViolations();
  });
});

describe("closingLine", () => {
  it.each([
    ["The Gazette", "That's the Gazette."],
    ["the morning post", "That's the morning post."],
    ["Morning Notes", "That's Morning Notes."],
    ["Theory Weekly", "That's Theory Weekly."],
  ])("%s", (name, line) => expect(closingLine(name)).toBe(line));

  it("prints the default for a blank name", () => {
    expect(paperName("  ")).toBe(DEFAULT_PAPER_NAME);
    expect(paperName(" Morning Notes ")).toBe("Morning Notes");
  });
});

// jsdom's axe cannot compute colors, so check the read text's colors here: a read story's headline, standfirst and
// meta line are text2 on bg at full opacity, and that must meet 4.5:1 in every scheme.
describe("read text contrast", () => {
  it.each(SCHEMES.map((s) => [s.id, s.tokens] as const))("%s: text2 on bg is at least 4.5:1", (_, t) => {
    expect(contrast(t.text2, t.bg)).toBeGreaterThanOrEqual(4.5);
  });
});
