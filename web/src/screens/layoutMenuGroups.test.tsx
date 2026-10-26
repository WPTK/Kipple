import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// The layout menu is grouped Pictures / Lists / Pages; Pictures in lists lives in its Pictures group (the same
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
  it("groups the layouts as Pictures, Lists and Pages, and the trigger is not named like List actions", async () => {
    go("/l/unread");
    const menu = await openMenu();
    const labels = within(menu).getAllByText(/^(Pictures|Lists|Pages)$/).map((e) => e.textContent);
    expect(labels).toEqual(["Pictures", "Lists", "Pages"]);
    expect(screen.getByRole("button", { name: "List actions" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^List options/ })).toBeNull();
  });

  it("exposes Pictures, Lists and Pages as groups, and Show and Hide are not in a layout group", async () => {
    go("/l/unread");
    const menu = await openMenu();
    for (const g of ["Pictures", "Lists", "Pages"]) {
      const group = within(menu).getByRole("group", { name: `Layout, ${g}` });
      expect(within(group).queryByRole("menuitemradio", { name: /^Show/ })).toBeNull();
    }
    expect(within(within(menu).getByRole("group", { name: "Layout, Pictures" })).getAllByRole("menuitemradio")).toHaveLength(2);
    expect(within(within(menu).getByRole("group", { name: "Layout, Pages" })).getAllByRole("menuitemradio")).toHaveLength(2);
    expect(within(menu).queryByRole("group", { name: /opens in/i })).toBeNull();
  });

  it("puts Opens in under Pages, before Order, on a feed", async () => {
    go("/l/unread?feed=1");
    const menu = await openMenu();
    const opens = within(menu).getByRole("group", { name: /opens in/i });
    const pages = within(menu).getByRole("group", { name: "Layout, Pages" });
    const order = within(menu).getByRole("group", { name: /^Order/ });
    expect(pages.compareDocumentPosition(opens) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(opens.compareDocumentPosition(order) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("holds Pictures in lists, the same setting as Settings, and changing it hides the Inbox thumbnails", async () => {
    updateDevicePrefs({ layout: "inbox" });
    go("/l/unread");
    const menu = await openMenu();
    const group = within(menu).getByRole("group", { name: "Pictures in lists" });
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
