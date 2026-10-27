import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { LAYOUT_HINTS, LAYOUT_IDS, LAYOUT_LABELS, devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// The layout menu is one flat radio group of the layouts, then Show and sort: Order, Group by and Pictures (the same
// device setting as Settings, Appearance, Lists); Order and Group by stay in every layout.

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
  });
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
  wide();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function openMenu() {
  await screen.findByText("Article number 1");
  await userEvent.click(screen.getByRole("button", { name: /^Show and sort, /i }));
  return await screen.findByRole("menu");
}

describe("the layout menu", () => {
  it("is one flat radio group of the seven layouts, each with its hint, and no category headings", async () => {
    go("/l/unread");
    const menu = await openMenu();
    const group = within(menu).getByRole("group", { name: "Layout" });
    const radios = within(group).getAllByRole("menuitemradio");
    expect(radios.map((r) => r.textContent)).toEqual(LAYOUT_IDS.map((id) => `${LAYOUT_LABELS[id]} ${LAYOUT_HINTS[id]}`));
    for (const id of LAYOUT_IDS) expect(within(group).getByRole("menuitemradio", { name: `${LAYOUT_LABELS[id]} ${LAYOUT_HINTS[id]}` })).toBeInTheDocument();
    expect(within(menu).queryByText(/^(Lists|Pages|Picture in list)$/)).toBeNull();
    expect(within(menu).getAllByRole("group", { name: /^Layout/ })).toHaveLength(1);
    expect(menu.querySelectorAll("[data-layout-glyph][aria-hidden='true']")).toHaveLength(7);
    expect(screen.getByRole("button", { name: "List actions" })).toBeInTheDocument();
  });

  it("checks the layout in effect, and choosing each layout selects it", async () => {
    go("/l/unread");
    for (const id of LAYOUT_IDS) {
      const menu = await openMenu();
      await userEvent.click(within(menu).getByRole("menuitemradio", { name: new RegExp(`^${LAYOUT_LABELS[id]} `) }));
      expect(devicePrefsStore.get().layout).toBe(id);
      expect(await screen.findByRole("button", { name: `Show and sort, ${LAYOUT_LABELS[id]} layout` })).toBeInTheDocument();
    }
    const menu = await openMenu();
    expect(within(menu).getAllByRole("menuitemradio", { checked: true }).filter((r) => r.textContent?.includes(LAYOUT_HINTS.digest))).toHaveLength(1);
  });

  it("moves with the arrow keys, selects with Enter and closes with Escape", async () => {
    updateDevicePrefs({ layout: "compact" });
    go("/l/unread");
    await openMenu();
    const user = userEvent.setup();
    await user.keyboard("{Home}");
    expect(screen.getByRole("menuitemradio", { name: /^Compact / })).toHaveFocus();
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(screen.getByRole("menuitemradio", { name: /^Inbox / })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(devicePrefsStore.get().layout).toBe("inbox");
    await openMenu();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("puts Order, Group by and Pictures under Show and sort", async () => {
    go("/l/unread");
    const menu = await openMenu();
    const heading = within(menu).getByText("Show and sort");
    for (const name of ["Order", "Group by", "Pictures"]) {
      const g = within(menu).getByRole("group", { name });
      expect(heading.compareDocumentPosition(g) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });
  it("puts Opens in under Show and sort, after Group by, on a feed", async () => {
    go("/l/unread?feed=1");
    const menu = await openMenu();
    const opens = within(menu).getByRole("group", { name: /opens in/i });
    const order = within(menu).getByRole("group", { name: /^Order/ });
    expect(order.compareDocumentPosition(opens) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("holds Pictures, the same setting as Settings, and changing it hides the Inbox thumbnails", async () => {
    updateDevicePrefs({ layout: "inbox" });
    go("/l/unread");
    const menu = await openMenu();
    const group = within(menu).getByRole("group", { name: "Pictures" });
    expect(within(group).getByRole("menuitemradio", { name: /^Show/ })).toBeChecked();
    await userEvent.click(within(group).getByRole("menuitemradio", { name: "Hide" }));
    expect(devicePrefsStore.get().listImages).toBe(false);
  });

  it("keeps Order and Group by in a layout that sets its own, greyed and focusable with the reason", async () => {
    updateDevicePrefs({ layout: "digest" });
    go("/l/unread");
    const menu = await openMenu();
    const order = within(menu).getByRole("group", { name: "Order" });
    const group = within(menu).getByRole("group", { name: "Group by" });
    for (const radio of [...within(order).getAllByRole("menuitemradio"), ...within(group).getAllByRole("menuitemradio")]) {
      expect(radio).toHaveAttribute("aria-disabled", "true");
      expect(radio).not.toHaveAttribute("disabled");
    }
    expect(within(order).getByRole("menuitemradio", { name: "Oldest first" })).toHaveAccessibleDescription("The Digest is always newest first.");
    expect(within(group).getByRole("menuitemradio", { name: "None" })).toHaveAccessibleDescription("The Digest groups by section.");
  });

  it("leaves Order and Group by live in a layout that follows them", async () => {
    updateDevicePrefs({ layout: "compact" });
    go("/l/unread");
    const menu = await openMenu();
    await userEvent.click(within(menu).getByRole("menuitemradio", { name: "Oldest first" }));
    expect(devicePrefsStore.get().order).toBe("oldest");
  });
});
