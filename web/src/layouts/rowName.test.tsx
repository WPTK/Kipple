import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { setSearchHighlight } from "@/lib/searchTerms";
import { card } from "@/test/mockApi";
import { cards } from "./cards";
import { compact } from "./compact";
import { Gazette } from "./gazette";
import { planGazette } from "./gazettePlan";
import { headlines } from "./headlines";
import { inbox } from "./inbox";
import { magazine } from "./magazine";
import type { ListLayout, RowMenuActions } from "./types";

// #513: a row link used to carry aria-label="<title>, unread". With a search word marked in a title that a narrow screen
// cuts short, the browser's visible text loses the marked words past the edge and no longer sits inside the label (axe
// label-content-name-mismatch). The link now has no label: it is named by its content, and an unread row's link carries
// data-row-unread, which index.css turns into the generated text ", unread" after the title. jsdom does not compute
// generated text, so the computed name is checked in a browser (uat/row-name.mjs); here, the contract the CSS relies on.

afterEach(() => {
  setSearchHighlight(undefined, {});
  cleanup();
});

const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;
const TITLE = "Don’t stop – the plan for the week";

const rows: ListLayout[] = [magazine, cards, compact, inbox, headlines];

describe.each(rows.map((l) => [l.id, l] as const))("%s row link", (_id, layout) => {
  const draw = (read: boolean) => {
    const item = { ...card(1), title: TITLE, read };
    const Row = layout.Row!;
    render(
      <MemoryRouter>
        <Row item={item} feed={undefined} selected={false} to={`/item/${item.id}`} onOpen={vi.fn()} onToggleStar={vi.fn()} actions={actions} />
      </MemoryRouter>,
    );
    return screen.getByRole("link", { name: TITLE });
  };

  it("has no aria-label and flags an unread article, with its text left as the title alone", () => {
    setSearchHighlight("the", {}); // a marked word is what made the visible text differ from the label
    const link = draw(false);
    expect(link.hasAttribute("aria-label")).toBe(false);
    expect(link.hasAttribute("data-row-unread")).toBe(true);
    expect(link.textContent).toBe(TITLE);
  });

  it("does not flag a read article", () => {
    const link = draw(true);
    expect(link.hasAttribute("aria-label")).toBe(false);
    expect(link.hasAttribute("data-row-unread")).toBe(false);
  });
});

describe("gazette story links", () => {
  it("have no aria-label, and only the unread stories are flagged", () => {
    const list = Array.from({ length: 6 }, (_, i) => ({ ...card(i + 1), title: `${TITLE} ${i + 1}`, read: i % 2 === 1 }));
    const plan = planGazette({ articles: list, feeds: [], folders: [], favorites: [], scope: {}, screen: "wide", more: false });
    render(
      <MemoryRouter>
        <Gazette plan={plan} items={new Map(list.map((c) => [c.id, c]))} name="The Gazette" date={new Date(2026, 9, 7, 9, 0)} screen="wide" to={(c) => `/item/${c.id}`} onOpen={vi.fn()} actions={actions} />
      </MemoryRouter>,
    );
    for (const c of list) {
      const link = screen.getByRole("link", { name: c.title });
      expect(link.hasAttribute("aria-label")).toBe(false);
      expect(link.hasAttribute("data-row-unread")).toBe(!c.read);
      expect(link.textContent).toBe(c.title);
    }
  });
});
