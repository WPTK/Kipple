import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { closeRowMenu } from "@/gestures/rowMenu";
import { LONG_PRESS_MS } from "@/gestures/tracking";
import { card } from "@/test/mockApi";
import { Gazette } from "./gazette";
import { planGazette } from "./gazettePlan";
import type { RowMenuActions } from "./types";

// The row menu on the Gazette's stories (#314): the More button or the context menu with a pointer, a long press on touch.

const cards = Array.from({ length: 8 }, (_, i) => card(i + 1, { image: i % 2 ? "https://example.com/i.jpg" : null }));

function draw(onOpen = vi.fn()) {
  const fns = new Map<string, ReturnType<typeof vi.fn>>();
  const actions = new Proxy({}, { get: (_, k: string) => fns.get(k) ?? fns.set(k, vi.fn()).get(k) }) as unknown as RowMenuActions;
  const plan = planGazette({ articles: cards, feeds: [], folders: [], favorites: [], scope: {}, screen: "wide", more: false });
  render(
    <MemoryRouter>
      <Gazette plan={plan} items={new Map(cards.map((c) => [c.id, c]))} name="The Gazette" date={new Date(2026, 9, 7, 9, 0)} screen="wide" to={(c) => `/item/${c.id}`} onOpen={onOpen} actions={actions} />
    </MemoryRouter>,
  );
  return { actions, onOpen };
}
const story = (n: number) => document.querySelector<HTMLElement>(`article[data-item-id="${cards[n - 1]!.id}"]`)!;
const touch = (type: string, init: PointerEventInit = {}) => ({ pointerType: "touch", pointerId: 1, clientX: 20, clientY: 20, bubbles: true, ...init, type });

beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
afterEach(() => {
  act(() => closeRowMenu());
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("a Gazette story's menu", () => {
  it("opens from the More button with the row actions except mark above and below", async () => {
    vi.useRealTimers();
    const { actions } = draw();
    const user = userEvent.setup();
    await user.click(within(story(3)).getByRole("button", { name: `More actions: ${cards[2]!.title}` }));
    const menu = await screen.findByRole("menu");
    const names = within(menu).getAllByRole("menuitem").map((m) => m.textContent);
    expect(names).toEqual(expect.arrayContaining(["Star", "Mark as read", "Mute similar…", "Edit feed", "Open original", "Copy link"]));
    expect(names.some((n) => /above|below/.test(n ?? ""))).toBe(false);
    await user.click(within(menu).getByRole("menuitem", { name: "Star" }));
    expect(actions.toggleStar).toHaveBeenCalledWith(cards[2]);
  });

  it("holds no room on the page: the button is laid over the story's corner", () => {
    draw();
    const more = within(story(1)).getByRole("button", { name: /More actions/ });
    expect(more.className).toContain("absolute");
  });

  it("opens from the context menu with a mouse", async () => {
    draw();
    fireEvent.contextMenu(within(story(2)).getByRole("link"), { button: 2 });
    expect(await screen.findByRole("menu")).toBeInTheDocument();
  });

  it("opens from a long press on touch, and the release does not open the story", async () => {
    const { onOpen } = draw();
    const link = within(story(4)).getByRole("link");
    fireEvent(link, new PointerEvent("pointerdown", touch("pointerdown")));
    act(() => void vi.advanceTimersByTime(LONG_PRESS_MS + 20));
    expect(await screen.findByRole("menu")).toBeInTheDocument();
    fireEvent(link, new PointerEvent("pointerup", touch("pointerup")));
    fireEvent.contextMenu(link); // the browser's own menu is not wanted
    fireEvent.click(link);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("does not open when the finger moves (a scroll) or lifts early, and a tap still opens the story", () => {
    const { onOpen } = draw();
    const link = within(story(5)).getByRole("link");
    fireEvent(link, new PointerEvent("pointerdown", touch("pointerdown")));
    fireEvent(link, new PointerEvent("pointermove", touch("pointermove", { clientY: 60 })));
    act(() => void vi.advanceTimersByTime(LONG_PRESS_MS + 20));
    expect(screen.queryByRole("menu")).toBeNull();
    fireEvent(link, new PointerEvent("pointerup", touch("pointerup")));
    fireEvent(link, new PointerEvent("pointerdown", touch("pointerdown")));
    act(() => void vi.advanceTimersByTime(100));
    fireEvent(link, new PointerEvent("pointerup", touch("pointerup")));
    fireEvent.click(link);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onOpen).toHaveBeenCalledWith(cards[4]);
  });
});
