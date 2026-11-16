import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { MemoryRouter } from "react-router";
import { card } from "@/test/mockApi";
import { cards } from "./cards";
import { magazine } from "./magazine";
import { OneFeed } from "./parts";
import type { RowLayout, RowMenuActions } from "./types";

afterEach(cleanup);

const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;

function draw(layout: RowLayout, over: Partial<ReturnType<typeof card>> = {}, swipe?: boolean) {
  const item = { ...card(1), title: "A long title that goes on", source: "Some Feed", read: false, ...over };
  const Row = layout.Row;
  render(
    <MemoryRouter>
      <OneFeed value={false}>
        <Row item={item} feed={undefined} selected={false} to={`/item/${item.id}`} onOpen={vi.fn()} onToggleStar={vi.fn()} actions={actions} swipe={swipe} />
      </OneFeed>
    </MemoryRouter>,
  );
  return screen.getByRole("article");
}

const withPicture = { image: "https://example.test/pic.jpg" };
const withoutPicture = { image: null };

describe.each([
  ["Editorial", magazine],
  ["Cards", cards],
])("%s row actions", (_name, layout) => {
  it.each([
    ["with a picture", withPicture],
    ["without a picture", withoutPicture],
  ])("puts the star and More on the source line, %s", (_label, over) => {
    const row = draw(layout, over);
    const time = row.querySelector("time")!;
    const line = time.parentElement!;
    expect(within(line).getByRole("button", { name: /^Star:/ })).toBeInTheDocument();
    expect(within(line).getByRole("button", { name: /^More actions:/ })).toBeInTheDocument();
    // No line of their own: the two buttons share one parent, and that parent is inside the source line.
    const star = within(row).getByRole("button", { name: /^Star:/ });
    const more = within(row).getByRole("button", { name: /^More actions:/ });
    expect(star.parentElement).toBe(more.parentElement);
    expect(line.contains(star.parentElement)).toBe(true);
  });

  it("keeps the buttons in the layout where the row does not swipe", () => {
    const row = draw(layout, withoutPicture, false);
    expect(row.querySelector(".row-swipe-aside")).toBeNull();
    expect(row.querySelector(".row-touch-unseen")).toBeNull();
  });

  it("takes the buttons out of the layout, but not out of reach, where the row swipes", () => {
    const row = draw(layout, withoutPicture, true);
    const star = within(row).getByRole("button", { name: /^Star:/ });
    const more = within(row).getByRole("button", { name: /^More actions:/ });
    // Named and focusable: a keyboard or a screen reader still gets both.
    expect(star).toHaveAccessibleName();
    expect(more).toHaveAccessibleName();
    expect(star).not.toHaveAttribute("disabled");
    expect(more).not.toHaveAttribute("disabled");
    expect(star.parentElement).toHaveClass("row-swipe-aside");
    expect(star).toHaveClass("row-touch-unseen");
    expect(more).toHaveClass("row-touch-unseen");
  });
});

describe("the stylesheet's swipe rule", () => {
  const css = readFileSync("src/index.css", "utf8");

  it("leaves a swiping row's actions out of the layout only where the sole pointer is a finger", () => {
    const start = css.indexOf("@media not (any-pointer: fine) {");
    expect(start).toBeGreaterThan(-1);
    const block = css.slice(start, css.indexOf("\n}\n", start));
    expect(block).toMatch(/\.row-swipe-aside \{[^}]*position:\s*absolute[^}]*pointer-events:\s*none/);
  });
});

describe("the phone picture", () => {
  it("is shorter than 16:9 while the row is stacked, and 16:9 beside the text", () => {
    const row = draw(magazine, withPicture);
    const img = row.querySelector("img")!;
    expect(img).toHaveClass("aspect-[2/1]");
    expect(img.className).toContain("@[34rem]:aspect-video");
    expect(img).not.toHaveClass("aspect-video");
  });

  it("is 2:1 on a card alone in its line and 16:9 on a card in a grid", () => {
    expect(draw(cards, withPicture, true).querySelector("img")).toHaveClass("aspect-[2/1]");
    cleanup();
    expect(draw(cards, withPicture, false).querySelector("img")).toHaveClass("aspect-video");
  });

  it("is 2:1 in a card's loading stand-in on a phone, so a card does not change height when it replaces it", () => {
    const Skeleton = cards.Skeleton!;
    const { container } = render(<Skeleton />);
    expect(container.querySelector(".animate-pulse")!.className).toContain("max-sm:aspect-[2/1]");
  });
});
