import { compact } from "./compact";
import type { ListLayout } from "./types";

// The Digest layout (each folder or feed as its own section). Until its page ships, it draws the Compact rows so the
// layout can be chosen and its device settings saved without breaking a list; the page replaces this entry.
export const digest: ListLayout = { ...compact, id: "digest", label: "Digest" };
