import { api } from "./client";

// Auto-read catch-up (docs/design.md 7.1d). Changing a threshold never marks anything; these two calls are the only
// way to mark what already crossed it: a dry run that counts, and a run that needs `confirm` above `confirm_above`.

export interface AutoReadPreview {
  total: number;
  /** Only feeds with something to mark, biggest first. `feed_id` is a string on the wire. */
  feeds: { feed_id: string; title: string; days: number; count: number }[];
  global_days: number;
  confirm_above: number;
}

export interface AutoReadRequest {
  /** Limit it to one feed. */
  feed_id?: string;
  /** "What if": use this many days (0 to 365) instead of the stored one, without saving it. */
  days?: number;
}

export const previewAutoRead = (body: AutoReadRequest = {}) => api<AutoReadPreview>("/api/library/auto-read/preview", { method: "POST", body });

export const runAutoRead = (body: AutoReadRequest & { confirm?: boolean; expect_total?: number }) =>
  api<{ id: string; kind: "auto_read"; done: number; total: number; changed: number; new_items: number; errors: number }>("/api/library/auto-read/run", {
    method: "POST",
    body,
  });

/** The days presets of the "Mark old articles as read after" control and the feed editor's select. */
export const AUTO_READ_PRESETS = [0, 30, 60, 90, 180, 365] as const;
export const autoReadLabel = (n: number): string => (n === 0 ? "Off" : `${n} days`);
