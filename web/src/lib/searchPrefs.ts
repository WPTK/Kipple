import { SEARCH_ORDERS, devicePrefsStore, updateDevicePrefs, type SearchOrder } from "./devicePrefs";
import { createStore, useStoreSelector } from "./store";

// The result ordering of the Search screen lives in the device profile (`client.search_order`, docs/design.md 7.1c):
// devicePrefs holds it and lib/deviceSync.ts syncs it like every other per-device setting.

export { SEARCH_ORDERS };
export type { SearchOrder };
export const SEARCH_ORDER_LABELS: Record<SearchOrder, string> = { rank: "Relevance", date: "Newest first", oldest: "Oldest first" };

export const setSearchOrder = (o: SearchOrder): void => updateDevicePrefs({ searchOrder: o });

export const useSearchOrder = (): SearchOrder => useStoreSelector(devicePrefsStore, (p) => p.searchOrder);

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
