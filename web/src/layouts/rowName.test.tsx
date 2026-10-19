import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { setSearchHighlight } from "@/lib/searchTerms";
import { card } from "@/test/mockApi";
import { cards } from "./cards";
import { compact } from "./compact";
import { headlines } from "./headlines";
import { inbox } from "./inbox";
import { magazine } from "./magazine";
import type { ListLayout, RowMenuActions } from "./types";

// #513: a row link used to carry aria-label="<title>, unread". With a search word marked in a title that a narrow screen
// cuts short, the browser's visible text loses the marked words past the edge and no longer sits inside the label (axe
// label-content-name-mismatch). The name now comes from the content: the title, then hidden "unread" text.

afterEach(() => {
  setSearchHighlight(undefined, {});
  cleanup();
});

const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;

const layouts: ListLayout[] = [magazine, cards, compact, inbox, headlines];

describe.each(layouts.map((l) => [l.id, l] as const))("%s row link", (_id, layout) => {
  const draw = (read: boolean) => {
    const item = { ...card(1), title: "Don’t stop – the plan for the week", read };
    const Row = layout.Row!;
    render(
      <MemoryRouter>
        <Row item={item} feed={undefined} selected={false} to={`/item/${item.id}`} onOpen={vi.fn()} onToggleStar={vi.fn()} actions={actions} />
      </MemoryRouter>,
    );
    return item;
  };

  it("is named by its content, with no aria-label to compare with the visible text", () => {
    setSearchHighlight("the", {}); // a marked word is what made the visible text differ from the label
    const item = draw(false);
    const link = screen.getByRole("link", { name: `${item.title}, unread` });
    expect(link.hasAttribute("aria-label")).toBe(false);
  });

  it("adds nothing to the name of a read article", () => {
    const item = draw(true);
    const link = screen.getByRole("link", { name: item.title });
    expect(link.hasAttribute("aria-label")).toBe(false);
    expect(link.querySelector(".sr-only")).toBeNull();
  });
});
