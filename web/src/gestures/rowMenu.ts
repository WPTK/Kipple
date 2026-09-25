import { createStore } from "@/lib/store";

/** Id of the row whose menu is open (long-press, swipe "More", or the More button). One at a time. */
export const rowMenuStore = createStore<string | null>(null);

export function openRowMenu(id: string): void {
  rowMenuStore.set(id);
}
export function closeRowMenu(): void {
  rowMenuStore.set(null);
}
