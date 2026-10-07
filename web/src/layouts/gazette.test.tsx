import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { axe } from "vitest-axe";
import type { Card } from "@/api/types";
import { card } from "@/test/mockApi";
import { closingLine, Gazette } from "./gazette";
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
    expect(screen.getAllByRole("region")).toHaveLength(p.pages.length);
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

  it("marks unread stories bold and fades read ones without removing them", () => {
    const mixed = cards.map((c, i) => (i === 1 ? { ...c, read: true } : c));
    const { container } = draw(mixed, p);
    const read = container.querySelector(`article[data-item-id="${mixed[1]!.id}"]`)!;
    expect(read.className).toContain("opacity-60");
    expect(read.querySelector("h3, h4")!.className).toContain("font-normal");
    const unread = container.querySelector(`article[data-item-id="${mixed[0]!.id}"]`)!;
    expect(unread.className).not.toContain("opacity-60");
    expect(within(unread as HTMLElement).getByRole("link").getAttribute("aria-label")).toMatch(/^Unread, /);
    expect(domOrder(container)).toEqual(planOrder(p));
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

  it("passes axe", async () => {
    const { container } = draw(cards, p);
    expect(await axe(container)).toHaveNoViolations();
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
});

describe("closingLine", () => {
  it.each([
    ["The Gazette", "That's the Gazette."],
    ["the morning post", "That's the morning post."],
    ["Morning Notes", "That's Morning Notes."],
    ["Theory Weekly", "That's Theory Weekly."],
    ["  ", "That's the Gazette."],
  ])("%s", (name, line) => expect(closingLine(name)).toBe(line));
});
