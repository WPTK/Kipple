import { createStore, useStore } from "./store";

// The result ordering of the Search screen, kept per device. It has no key in the server's device profile
// (docs/design.md 7.1c allows `client.order`, newest or oldest, for lists but nothing for search), so it lives in
// this browser's localStorage only; a device is a browser, so that is the right scope even if it does not sync.

export const SEARCH_ORDERS = ["rank", "date", "oldest"] as const;
export type SearchOrder = (typeof SEARCH_ORDERS)[number];
export const SEARCH_ORDER_LABELS: Record<SearchOrder, string> = { rank: "Relevance", date: "Newest first", oldest: "Oldest first" };
export const SEARCH_ORDER_KEY = "kipple.searchOrder.v1";

const isOrder = (v: unknown): v is SearchOrder => (SEARCH_ORDERS as readonly unknown[]).includes(v);

function read(): SearchOrder {
  try {
    const v = localStorage.getItem(SEARCH_ORDER_KEY);
    return isOrder(v) ? v : "rank";
  } catch {
    return "rank";
  }
}

export const searchOrderStore = createStore<SearchOrder>(read());

export function setSearchOrder(o: SearchOrder): void {
  searchOrderStore.set(o);
  try {
    localStorage.setItem(SEARCH_ORDER_KEY, o);
  } catch {
    /* not remembered */
  }
}

export const useSearchOrder = (): SearchOrder => useStore(searchOrderStore);

/** The list scope's `order` for a search ordering: newest first is the server default and is not sent. */
export const scopeOrder = (o: SearchOrder): "rank" | "oldest" | undefined => (o === "rank" ? "rank" : o === "oldest" ? "oldest" : undefined);

// Whether the sidebar's "Saved searches" section is open, per device (localStorage, like the folders' collapse state
// would be if it had a profile key: the profile has none for this).
const OPEN_KEY = "kipple.savedSearchesOpen.v1";
export const savedOpenStore = createStore<boolean>(
  (() => {
    try {
      return localStorage.getItem(OPEN_KEY) !== "0";
    } catch {
      return true;
    }
  })(),
);
export function setSavedOpen(open: boolean): void {
  savedOpenStore.set(open);
  try {
    localStorage.setItem(OPEN_KEY, open ? "1" : "0");
  } catch {
    /* not remembered */
  }
}
