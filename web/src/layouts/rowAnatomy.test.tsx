import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { card } from "@/test/mockApi";
import { compact } from "./compact";
import { headlines } from "./headlines";
import { inbox } from "./inbox";
import { magazine } from "./magazine";
import { OneFeed, ListImage, rowState, titleTone, useImageFailure } from "./parts";
import { results } from "./results";
import type { RowLayout, RowMenuActions } from "./types";

afterEach(cleanup);

const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;

function draw(layout: RowLayout, over: Partial<ReturnType<typeof card>> = {}, oneFeed = false) {
  const item = { ...card(1), title: "A long title that goes on", source: "Some Feed", read: false, ...over };
  const Row = layout.Row;
  render(
    <MemoryRouter>
      <OneFeed value={oneFeed}>
        <Row item={item} feed={undefined} selected={false} to={`/item/${item.id}`} onOpen={vi.fn()} onToggleStar={vi.fn()} actions={actions} />
      </OneFeed>
    </MemoryRouter>,
  );
  return screen.getByRole("article");
}

describe("the unread cue", () => {
  it("is one helper: bold full strength while unread, regular secondary once read", () => {
    expect(titleTone(true)).toBe("font-bold text-fg");
    expect(titleTone(false)).toBe("font-normal text-fg2");
  });

  it.each([inbox, compact, headlines, magazine])("$id bolds the title, not the source", (layout) => {
    const row = draw(layout);
    expect(row.querySelector("h3")).toHaveClass("font-bold");
    const source = within(row).queryByText("Some Feed");
    if (source) expect(source).not.toHaveClass("font-bold");
    expect(row.querySelector('[data-unread="true"]')).not.toBeNull();
  });
});

describe("selected, hovered and focused rows differ", () => {
  it("marks the selected row with a bar as well as the tone", () => {
    expect(rowState(true)).toContain("bg-selection");
    expect(rowState(true)).toContain("shadow-[inset_3px_0_0");
    expect(rowState(false)).toBe("hover:bg-surface");
  });
});

describe("Inbox row", () => {
  it("puts the title first, up to two lines, and the source on the meta line", () => {
    const row = draw(inbox);
    const body = row.querySelector("h3")!.parentElement!;
    expect(body.firstElementChild).toBe(row.querySelector("h3"));
    expect(row.querySelector("h3")).toHaveClass("line-clamp-2");
    expect(row.querySelector("h3")).not.toHaveClass("truncate");
    expect(within(row).getByText("Some Feed").closest("div")!.textContent).toContain("Some Feed");
  });

  it("names no source in a list of one feed", () => {
    const row = draw(inbox, {}, true);
    expect(within(row).queryByText("Some Feed")).toBeNull();
    expect(row.querySelector("time")).not.toBeNull();
  });

  it("leaves Headlines' source column out in a list of one feed", () => {
    expect(within(draw(headlines)).getByText("Some Feed")).toBeInTheDocument();
    cleanup();
    expect(within(draw(headlines, {}, true)).queryByText("Some Feed")).toBeNull();
  });
});

describe("row controls", () => {
  it("Compact shows its star at rest, starred or not", () => {
    const row = draw(compact);
    const star = within(row).getByRole("button", { name: /^Star:/ });
    expect(star.className).not.toMatch(/row-fine-only|row-reveal|opacity/);
  });

  it("draws the unstarred star at full strength", () => {
    const star = within(draw(inbox)).getByRole("button", { name: /^Star:/ });
    expect(star).toHaveClass("text-fg2");
    expect(star.className).not.toMatch(/opacity/);
  });

  it("Inbox, Compact and Editorial do not hide More at rest", () => {
    for (const l of [inbox, compact, magazine]) {
      expect(within(draw(l)).getByRole("button", { name: /^More actions:/ }).className).not.toContain("row-reveal");
      cleanup();
    }
  });
});

describe("search results rows", () => {
  it("are Inbox rows with no picture and a one-line snippet", () => {
    const row = draw(results, { image: "/img/x.jpg", excerpt: "A snippet that matches" });
    expect(row.querySelector("img")).toBeNull();
    expect(within(row).getByText("A snippet that matches")).toHaveClass("line-clamp-1");
  });
});

describe("a lead picture's fit", () => {
  function Pic({ src }: { src: string }) {
    const failure = useImageFailure(src);
    return <ListImage src={src} failure={failure} shaped frame={{ className: "frame" }} className="pic" />;
  }
  const load = (img: HTMLImageElement, nw: number, nh: number, cw: number, ch: number) => {
    for (const [k, v] of Object.entries({ naturalWidth: nw, naturalHeight: nh, clientWidth: cw, clientHeight: ch })) Object.defineProperty(img, k, { value: v, configurable: true });
    fireEvent.load(img);
  };

  it("fills the frame when its shape is close to it, and sits whole in it when it is not", () => {
    const { container } = render(
      <>
        <Pic src="/shape-wide.jpg" />
        <Pic src="/shape-tall.jpg" />
        <Pic src="/shape-pano.jpg" />
        <Pic src="/shape-square.jpg" />
      </>,
    );
    const [wide, tall, pano, square] = [...container.querySelectorAll("img")];
    load(wide!, 1600, 900, 320, 180);
    load(tall!, 600, 900, 320, 180);
    load(pano!, 3000, 1000, 320, 180);
    load(square!, 800, 800, 320, 180);
    expect(wide).toHaveClass("object-cover");
    expect(tall).toHaveClass("object-contain");
    expect(pano).toHaveClass("object-contain");
    expect(square).toHaveClass("object-contain");
  });

  it("starts filled and never changes the frame", () => {
    const { container } = render(<Pic src="/shape-frame.jpg" />);
    expect(container.querySelector("img")).toHaveClass("object-cover");
    expect(container.querySelector(".frame")).not.toBeNull();
  });
});