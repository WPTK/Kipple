import { useQuery, type QueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./client";
import { noteFilterTouched } from "./filterEdits";
import { keys } from "./queries";
import type { Card } from "./types";

// Filters (docs/design.md 7.1b). A filter mutes, marks read, stars or highlights the articles its terms match.

export type FilterAction = "mute" | "mark_read" | "star" | "highlight";
export type FilterScope = "global" | "folder" | "feed";
export type FilterKind = "text" | "regex";
export type FilterField = "title" | "author" | "content" | "url" | "category" | "feed";

export interface Filter {
  id: string;
  name: string;
  enabled: boolean;
  /** Why Kipple switched the rule off itself (it no longer meets the current limits), else null. */
  disabled_reason?: string | null;
  scope: FilterScope;
  folder_id: string | null;
  feed_id: string | null;
  kind: FilterKind;
  terms: string[];
  fields: FilterField[];
  case_sensitive: boolean;
  whole_word: boolean;
  fold_diacritics: boolean;
  invert: boolean;
  action: FilterAction;
  position: number;
  hits: number;
  last_hit_at: number | null;
  created_at: number;
  updated_at: number;
  /** How many articles this filter mutes right now. */
  muted_items: number;
}

/** What the editor changes: every writable field of a filter. */
export interface FilterDraft {
  name: string;
  enabled: boolean;
  scope: FilterScope;
  folder_id: string | null;
  feed_id: string | null;
  kind: FilterKind;
  terms: string[];
  fields: FilterField[];
  case_sensitive: boolean;
  whole_word: boolean;
  fold_diacritics: boolean;
  invert: boolean;
  action: FilterAction;
}

/** The server's limits (internal/filter/rule.go); the editor checks them before it sends. */
export const LIMITS = { rules: 200, termsPerRule: 50, termRunes: 100, regexPatterns: 5, regexBytes: 256, nameBytes: 200 } as const;

export const FIELDS: readonly { id: FilterField; label: string }[] = [
  { id: "title", label: "Title" },
  { id: "content", label: "Article text" },
  { id: "author", label: "Author" },
  { id: "url", label: "Link" },
  { id: "category", label: "Category or tag" },
  { id: "feed", label: "Feed name" },
];
export const fieldLabel = (f: string): string => FIELDS.find((x) => x.id === f)?.label ?? f;

export const ACTIONS: readonly { id: FilterAction; label: string; help: string }[] = [
  { id: "mute", label: "Mute", help: "Hides matching articles from Unread, All and search, and marks them read. They are kept in Muted, and you can restore any of them." },
  { id: "mark_read", label: "Mark as read", help: "Marks matching articles read as they arrive. They stay in All and search." },
  { id: "star", label: "Star", help: "Stars matching articles as they arrive so they are easy to find. A starred article is never muted." },
  { id: "highlight", label: "Highlight", help: "Draws the matching words in a colored mark in lists and articles. Nothing is changed or hidden." },
];
export const actionLabel = (a: string): string => ACTIONS.find((x) => x.id === a)?.label ?? a;

export function emptyDraft(over: Partial<FilterDraft> = {}): FilterDraft {
  return {
    name: "",
    enabled: true,
    scope: "global",
    folder_id: null,
    feed_id: null,
    kind: "text",
    terms: [],
    fields: ["title"],
    case_sensitive: false,
    whole_word: true,
    fold_diacritics: true,
    invert: false,
    action: "mute",
    ...over,
  };
}

export const draftOf = (f: Filter): FilterDraft => ({
  name: f.name,
  enabled: f.enabled,
  scope: f.scope,
  folder_id: f.folder_id,
  feed_id: f.feed_id,
  kind: f.kind,
  terms: [...f.terms],
  fields: [...f.fields],
  case_sensitive: f.case_sensitive,
  whole_word: f.whole_word,
  fold_diacritics: f.fold_diacritics,
  invert: f.invert,
  action: f.action,
});

/** `s` cut to at most `max` UTF-8 bytes, on a character boundary. */
export function truncateBytes(s: string, max: number): string {
  const enc = new TextEncoder();
  if (enc.encode(s).length <= max) return s;
  let out = "";
  let used = 0;
  for (const ch of s) {
    const n = enc.encode(ch).length;
    if (used + n > max) break;
    out += ch;
    used += n;
  }
  return out;
}

/** A name that fits the server's limit (200 bytes, not characters: CJK takes three bytes each). */
export function clipName(s: string): string {
  if (new TextEncoder().encode(s).length <= LIMITS.nameBytes) return s;
  return `${truncateBytes(s, LIMITS.nameBytes - 3)}…`;
}

export const filtersKey = ["filters"] as const;

export function useFilters() {
  return useQuery({
    queryKey: filtersKey,
    queryFn: ({ signal }) => api<{ filters: Filter[] }>("/api/filters", { signal }).then((r) => r.filters),
    staleTime: 30_000,
  });
}

/** The body for a write: the editable fields, with the target of the scope only. */
export function bodyOf(d: FilterDraft): Record<string, unknown> {
  return {
    name: d.name.trim(),
    enabled: d.enabled,
    scope: d.scope,
    folder_id: d.scope === "folder" ? d.folder_id : null,
    feed_id: d.scope === "feed" ? d.feed_id : null,
    kind: d.kind,
    terms: d.terms,
    fields: d.fields,
    case_sensitive: d.case_sensitive,
    whole_word: d.whole_word,
    fold_diacritics: d.fold_diacritics,
    invert: d.invert,
    action: d.action,
  };
}

export interface ApplyRun {
  id: string;
  kind: "filter_apply";
  filter_id: string;
  done: number;
  total: number;
  changed: number;
  errors: number;
}

export interface CreateResult {
  filter: Filter;
  /** null: not asked; a run: started; {error:"busy"}: another apply is running (the filter was created anyway). */
  applied: ApplyRun | { error: "busy" } | null;
}

export const createFilter = (d: FilterDraft, applyExisting?: { include_read: boolean }) =>
  api<CreateResult>("/api/filters", { method: "POST", body: { ...bodyOf(d), ...(applyExisting ? { apply_existing: applyExisting } : {}) } });

export const updateFilter = (id: string, d: FilterDraft) => {
  noteFilterTouched(id);
  return api<{ filter: Filter }>(`/api/filters/${id}`, { method: "PATCH", body: bodyOf(d) });
};

export const setFilterEnabled = (id: string, enabled: boolean) => {
  noteFilterTouched(id);
  return api<{ filter: Filter }>(`/api/filters/${id}`, { method: "PATCH", body: { enabled } });
};

export type Unmute = "keep" | "read" | "unread";
export const deleteFilter = (id: string, unmute: Unmute) => {
  noteFilterTouched(id);
  return api<{ changed: number; made_unread: number; done: boolean }>(`/api/filters/${id}`, { method: "DELETE", params: { unmute } });
};

export const applyFilter = (id: string, includeRead: boolean) => api<ApplyRun>(`/api/filters/${id}/apply`, { method: "POST", body: { include_read: includeRead } });

export interface Preview {
  matches: number;
  scanned: number;
  truncated: boolean;
  sample: Card[];
  warnings: { code: string; message: string }[];
}

/** A dry run of an unsaved rule (`bodyOf(draft)`, overlaid on the saved one when `id` is given): what it would do to stored articles. */
export const previewFilter = (body: Record<string, unknown>, opts: { id?: string; includeRead: boolean; signal?: AbortSignal }) =>
  api<Preview>("/api/filters/preview", {
    method: "POST",
    body: { ...(opts.id ? { id: opts.id } : {}), filter: body, include_read: opts.includeRead },
    signal: opts.signal,
  });

/** `{error:"bad_filter", field, message}` from a 400, or null. */
export function badFilter(e: unknown): { field: string; message: string } | null {
  if (!(e instanceof ApiError) || e.status !== 400 || e.code !== "bad_filter") return null;
  const b = e.body ?? {};
  return { field: typeof b.field === "string" ? b.field : "", message: typeof b.message === "string" ? b.message : "The filter isn't valid." };
}

/** The editor field a server `field` belongs to ("terms[2]" is the terms list). */
export function fieldGroup(field: string): "name" | "scope" | "kind" | "terms" | "fields" | "action" | "options" | "target" | "other" {
  const base = field.replace(/\[\d+\]$/, "");
  if (base === "name") return "name";
  if (base === "scope") return "scope";
  if (base === "folder_id" || base === "feed_id") return "target";
  if (base === "kind") return "kind";
  if (base === "terms") return "terms";
  if (base === "fields") return "fields";
  if (base === "action") return "action";
  if (base === "case_sensitive" || base === "whole_word" || base === "fold_diacritics" || base === "invert") return "options";
  return "other";
}

/** After a write: the filters, the bootstrap (highlights, counts) and the lists a mute or un-mute changes. */
export function invalidateFilterData(qc: QueryClient): void {
  void qc.invalidateQueries({ queryKey: filtersKey });
  void qc.invalidateQueries({ queryKey: keys.bootstrap });
}
