import { createStore } from "./store";

/** The feed id the global feed editor should show, or null when it is closed. Opened from an article's "Manage
 * this feed" menu action, mounted once in `FeedEditorHost`. Feed Health and Manage Feeds keep their own local
 * feed editor instance, since they already have the full `Feed` object in hand. */
export const feedEditorStore = createStore<string | null>(null);
export const openFeedEditor = (feedId: string): void => feedEditorStore.set(feedId);
export const closeFeedEditor = (): void => feedEditorStore.set(null);
