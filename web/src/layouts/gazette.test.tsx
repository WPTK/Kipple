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

  it("is one Tab stop: the first story in reading order, else the selected one", () => {
    const tabbable = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>("article[data-item-id] a")].filter((a) => a.tabIndex >= 0).map((a) => a.closest("article")!.getAttribute("data-item-id"));
    const { container, rerender } = draw(cards, p);
    expect(tabbable(container)).toEqual([planOrder(p)[0]]);
    const picked = planOrder(p)[10]!;
    rerender(
      <MemoryRouter>
        <Gazette plan={p} items={new Map(cards.map((c) => [c.id, c]))} name="The Gazette" date={DATE} screen="wide" to={(c) => `/item/${c.id}`} onOpen={vi.fn()} selectedId={picked} />
      </MemoryRouter>,
    );
    expect(tabbable(container)).toEqual([picked]);
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
    expect(within(unread as HTMLElement).getByRole("link").getAttribute("aria-label")).toMatch(/, unread$/);
    expect(unread.querySelector("h3")!.classList).toContain("text-fg");
  });

  it("drops a picture that cannot be shown, with the box that held its place", () => {
    const { container } = draw(cards, p);
    const lead = container.querySelector<HTMLElement>('article[data-slot="lead"]')!;
    const img = lead.querySelector<HTMLImageElement>("img")!;
    expect(img.parentElement!.className).toContain("aspect-video");
    fireEvent.error(img);
    expect(lead.querySelector("img")).toBeNull();
    expect(lead.querySelector(".aspect-video")).toBeNull();
    expect(within(lead).getByRole("link")).toBeTruthy();
  });

  it("lays a story whose picture failed out as a story without a picture", () => {
    // A picture that failed is remembered by address, so each failing test uses its own.
    const { container } = draw(cards.map((c) => (c.image ? { ...c, image: `${IMG}?t1` } : c)), p);
    const lead = container.querySelector<HTMLElement>('article[data-slot="lead"]')!;
    expect(lead.querySelector("h3")!.className).toContain("text-[1.625rem]");
    fireEvent.error(lead.querySelector("img")!);
    expect(lead.querySelector("h3")!.className).not.toContain("text-[1.625rem]");
  });

  it("lays a photo whose picture failed out as an ordinary story with its standfirst", () => {
    // No favorites and several pictures: a busy front page with a row of photos under a text lead.
    const busy = Array.from({ length: 40 }, (_, i) => card(i + 1, { image: i % 4 === 0 ? `${IMG}?t2` : null }));
    const bp = plan(busy);
    const { container } = draw(busy, bp);
    const photo = container.querySelector<HTMLElement>('article[data-slot="photo"]')!;
    const id = photo.getAttribute("data-item-id")!;
    expect(photo.querySelector("img")).not.toBeNull();
    expect(photo.querySelector("p.font-reading")).toBeNull();
    expect(photo.querySelector("h3, h4")!.className).toContain("text-base");
    fireEvent.error(photo.querySelector("img")!);
    const after = container.querySelector<HTMLElement>(`article[data-item-id="${id}"]`)!;
    expect(after.getAttribute("data-slot")).toBe("story");
    expect(after.querySelector("img")).toBeNull();
    expect(after.querySelector("h3, h4")!.className).toContain("text-lg");
    expect(after.querySelector("p.font-reading")!.textContent).toBe(busy.find((c) => c.id === id)!.excerpt);
    expect(domOrder(container)).toEqual(planOrder(bp));
  });

  it("lays a co-lead whose picture failed out as a headline lead", () => {
    const two = Array.from({ length: 40 }, (_, i) => card(i + 1, { image: i < 2 ? `${IMG}?t3` : null, feed_id: i % 2 ? "2" : "1" }));
    const tp = plan(two, { favorites: [{ t: "feed", id: "1" }, { t: "feed", id: "2" }] });
    expect(tp.pages[0]!.type).toBe("co-leads");
    const { container } = draw(two, tp);
    const co = container.querySelector<HTMLElement>('article[data-slot="co-lead"]')!;
    const lead = container.querySelector<HTMLElement>('article[data-slot="lead"]')!;
    expect(co.querySelector("img")).not.toBeNull();
    expect(co.querySelector("h3")!.className).toContain("text-[1.625rem]");
    expect(co.querySelector("p.font-reading")).not.toBeNull();
    fireEvent.error(co.querySelector("img")!);
    expect(co.getAttribute("data-slot")).toBe("co-lead");
    expect(co.querySelector("img")).toBeNull();
    expect(co.querySelector("h3")!.className).toContain("text-[2.5rem]");
    // Only the failed one changes; the other lead keeps its picture and size.
    expect(lead.querySelector("img")).not.toBeNull();
    expect(lead.querySelector("h3")!.className).toContain("text-[1.625rem]");
  });

  it("passes axe with read and unread stories", async () => {
    const mixed = cards.map((c, i) => (i % 3 === 0 ? { ...c, read: true } : c));
    const { container } = draw(mixed, p);
    expect(await axe(container)).toHaveNoViolations();
  });

  it("opens a story through its link", async () => {
    const onOpen = vi.fn();
    draw(cards, p, "wide", onOpen);
    const link = screen.getByRole("link", { name: `${cards[0]!.title}, unread` });
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
