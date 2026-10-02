// Settings, feed management, health, account and backup calls (docs/design.md 7.1).
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./client";
import { keys } from "./queries";
import type { Bootstrap, Feed, Folder, Me } from "./types";

// ---- Settings -----------------------------------------------------------------

export type SettingKind = "bool" | "enum" | "int" | "text" | "json";
export type SettingGroup = "reading" | "sync" | "library" | "images" | "stats" | "account" | "advanced";
export type SettingSurface = "reader_menu" | "settings" | "hidden";

export interface SettingOption {
  value: string | number;
  label: string;
  css?: Record<string, unknown>;
}

export interface SettingMeta {
  key: string;
  value: unknown;
  default: unknown;
  label: string;
  description: string;
  group: SettingGroup;
  kind: SettingKind;
  options?: SettingOption[];
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  surface: SettingSurface;
  /** global: one value for the account. device: the row is only the default for devices; each device has its own control. */
  scope?: "global" | "device" | "both";
}

export interface SettingsResponse {
  settings: SettingMeta[];
  values: Record<string, unknown>;
}

/** The 400 body of PATCH /api/settings. */
export interface SettingsIssues {
  keys: string[];
  issues: { key: string; message: string }[];
}

export const settingsKey = ["settings"] as const;

export function useSettings() {
  return useQuery({
    queryKey: settingsKey,
    queryFn: ({ signal }) => api<SettingsResponse>("/api/settings", { signal }),
    staleTime: 60_000,
  });
}

/** Issues from a failed PATCH /api/settings (`keys`, `issues`), or null when the error is something else. */
export function settingsIssues(e: unknown): SettingsIssues | null {
  if (!(e instanceof ApiError) || e.status !== 400 || !e.body) return null;
  const b = e.body as Partial<SettingsIssues>;
  return { keys: Array.isArray(b.keys) ? b.keys : [], issues: Array.isArray(b.issues) ? b.issues : [] };
}

/** PATCH one or more settings (null resets to the default). Optimistic; rolls back on an error. */
export function usePatchSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: Record<string, unknown>) => api<SettingsResponse>("/api/settings", { method: "PATCH", body: patch }),
    onMutate: async (patch) => {
      await qc.cancelQueries({ queryKey: settingsKey });
      const prev = qc.getQueryData<SettingsResponse>(settingsKey);
      if (prev) {
        const resolved = (k: string, v: unknown) => (v === null ? prev.settings.find((s) => s.key === k)?.default : v);
        qc.setQueryData<SettingsResponse>(settingsKey, {
          settings: prev.settings.map((s) => (s.key in patch ? { ...s, value: resolved(s.key, patch[s.key]) } : s)),
          values: { ...prev.values, ...Object.fromEntries(Object.entries(patch).map(([k, v]) => [k, resolved(k, v)])) },
        });
      }
      return { prev };
    },
    onError: (_e, _p, ctx) => {
      if (ctx?.prev) qc.setQueryData(settingsKey, ctx.prev);
    },
    onSuccess: (res) => {
      qc.setQueryData(settingsKey, res);
      // Timezone, week start and the stats switch all change the summary; do not leave it stale for its 60 s.
      void qc.invalidateQueries({ queryKey: ["stats"] });
      qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => (old ? { ...old, settings: res.values } : old));
    },
  });
}

// ---- Feeds ----------------------------------------------------------------------

/** What add and edit calls return: the bootstrap feed plus the editable fields. */
export interface FeedDetail extends Feed {
  url: string;
  url_original: string | null;
  custom_title: string | null;
  position: number;
  enabled: boolean;
  disabled_reason: string | null;
  dedup_mode: "auto" | "link" | "link_title";
  rekey_pending: boolean;
  user_agent: string | null;
  has_http_auth: boolean;
  ignore_http_cache: boolean;
  disable_http2: boolean;
  allow_insecure_tls: boolean;
  allow_private_net: boolean;
  next_fetch_at: number;
}

export interface Candidate {
  url: string;
  title: string;
  type: string;
}

export interface FetchOutcome {
  outcome?: string;
  new_items?: number;
  error_class?: string | null;
  error?: string | null;
  pending?: boolean;
}

export type AddFeedResult =
  | { status: "exists"; feed: FeedDetail }
  | { status: "choose"; candidates: Candidate[] }
  | { status: "ok"; feed: FeedDetail; fetch?: FetchOutcome };

export const addFeed = (body: { url: string; title?: string; folder_id?: string }) =>
  api<AddFeedResult>("/api/feeds", { method: "POST", body });

export const patchFeed = (id: string, body: Record<string, unknown>) =>
  api<FeedDetail>(`/api/feeds/${id}`, { method: "PATCH", body });

/** The editor's full FeedDetail (custom_title, dedup_mode, user agent, the flags). */
export const loadFeedDetail = (f: Feed) => api<FeedDetail>(`/api/feeds/${f.id}`);

/** Reorder in one transaction: folders in the given order, and/or the feeds of each listed folder. */
export const reorder = (body: { folders?: string[]; feeds?: { folder_id: string; ids: string[] }[] }) =>
  api<{ changed_feeds: string[]; changed_folders: string[] }>("/api/reorder", { method: "POST", body });

export const deleteFeed = (id: string, deleteStarred: boolean) =>
  api(`/api/feeds/${id}`, { method: "DELETE", params: deleteStarred ? { delete_starred: 1 } : undefined });

export const refreshFeed = (id: string, full = false) =>
  api<FetchOutcome>(`/api/feeds/${id}/refresh`, { method: "POST", params: full ? { full: 1 } : undefined });

export const createFolder = (name: string) => api<Folder>("/api/folders", { method: "POST", body: { name } });
export const patchFolder = (id: string, body: { name?: string; position?: number }) =>
  api<Folder>(`/api/folders/${id}`, { method: "PATCH", body });
export const deleteFolder = (id: string) => api(`/api/folders/${id}`, { method: "DELETE" });

export const invalidateFeeds = (qc: QueryClient) => {
  void qc.invalidateQueries({ queryKey: keys.bootstrap });
  // Saved searches that lost a deleted feed or folder as their scope are refetched on the server's
  // feed.changed / folder.changed events (api/events.ts), not here.
  void qc.invalidateQueries({ queryKey: ["health"] });
};

export interface OpmlResult {
  folders_created: number;
  feeds_added: number;
  feeds_existing: { url: string; feed_id: string }[];
  folders_merged_case: { kept: string; merged: string }[];
  memberships_dropped: { url: string; kept: string; dropped: string[] }[];
  /** Outlines that could not be imported (a bad URL). */
  skipped?: { url: string; reason: string }[];
  /** "<feed url>: <what was wrong>" for kipple:* attributes with bad values. */
  invalid_attrs?: string[];
  /** "<feed url>: kipple:allow_private_net | kipple:allow_insecure_tls": valid but never applied by an import. */
  ignored_attrs?: string[];
  run_id?: string;
}

/** Multipart OPML upload: api() passes FormData through unchanged. */
export function importOpml(file: File, markReadOlderThanDays?: number): Promise<OpmlResult> {
  const fd = new FormData();
  fd.append("file", file);
  return api<OpmlResult>("/api/opml", {
    method: "POST",
    body: fd,
    params: { mark_read_older_than_days: markReadOlderThanDays || undefined },
  });
}

// ---- Health ---------------------------------------------------------------------

export interface HealthFeed {
  id: string;
  title: string;
  url: string;
  url_original: string | null;
  status: string;
  redirect_pending: boolean;
  notices: string[];
  enabled: boolean;
  disabled_reason: string | null;
  last_success_at: number | null;
  last_fetch_at: number | null;
  last_error_at: number | null;
  last_error_class: string | null;
  last_error: string | null;
  last_status: number | null;
  consecutive_failures: number;
  current_delay_s: number | null;
  next_fetch_at: number | null;
  redirect_to: string | null;
  redirect_kind: string | null;
  redirect_count: number;
  last_new_items_at: number | null;
  trimmed_unread_count: number;
  trimmed_unread_since: number | null;
  host_throttled_until: number | null;
}

export interface HealthResponse {
  feeds: HealthFeed[];
  clients: { family: string; last_seen_at: number }[];
  snapshot: { last_at: number | null; last_error: string | null };
  clock: { ahead_s: number };
  db: { db_bytes: number; wal_bytes: number; backup_bytes: number; imgcache_bytes: number };
  unread_total: number;
}

export interface FetchLogRow {
  id: string;
  trigger: string;
  started_at: number;
  duration_ms: number;
  outcome: string;
  http_status: number | null;
  error_class: string | null;
  error: string | null;
  new_items: number;
  updated_items: number;
  trimmed_items: number;
  first_item_id: string | null;
  last_item_id: string | null;
  bytes: number | null;
  final_url: string | null;
  note: string | null;
  keep: boolean;
}

export const useHealth = () =>
  useQuery({ queryKey: ["health", "feeds"], queryFn: ({ signal }) => api<HealthResponse>("/api/health/feeds", { signal }), staleTime: 15_000 });

export const useFeedLog = (id: string | null) =>
  useQuery({
    queryKey: ["health", "log", id],
    queryFn: ({ signal }) => api<{ log?: FetchLogRow[]; rows?: FetchLogRow[] } | FetchLogRow[]>(`/api/health/feeds/${id}/log`, { signal }),
    enabled: !!id,
    select: (d): FetchLogRow[] => (Array.isArray(d) ? d : (d.log ?? d.rows ?? [])),
  });

// ---- Account and backup ---------------------------------------------------------

export const changePassword = (current: string, next: string) =>
  api("/api/account/password", { method: "POST", body: { current, new: next } });
/** The account as the server sees this request (live: the service worker never answers it from its cache). */
export const fetchMe = () => api<Me>("/api/auth/me");
/** Removes the web password; the server allows it only through a verified Cloudflare Access sign-in. */
export const removePassword = (current: string) => api("/api/account/password", { method: "POST", body: { current, remove: true } });
/** `current` is the web password; an account in open mode has none and sends nothing. */
export const generateApiPassword = (current?: string) =>
  api<{ api_password: string }>("/api/account/api-password", { method: "POST", body: current === undefined ? { generate: true } : { current, generate: true } });
export const applyRetention = () => api<{ run_id: string; total: number }>("/api/retention/apply", { method: "POST" });

export interface BackupInfo {
  status?: "ready";
  token: string;
  url: string;
  filename: string;
  bytes: number;
  expires_at: number;
  expires_in: number;
  warning: string;
  contents: { kipple_version: string; schema_version: number; created_at: string; feeds: number; items: number; starred: number; db_bytes: number };
}

type BackupAnswer = BackupInfo | { status: "building"; job_id?: string } | { status: "failed"; error: string; message?: string };

const FAILED_STATUS: Record<string, number> = { no_space: 507, too_large: 413, busy: 409 };

/**
 * Build a backup. POST /api/backup answers 200 with the ready payload when the export finishes within about
 * five seconds, else 202 {job_id}: then GET /api/backup/jobs/{id} is polled until it is ready or failed. The job
 * outlives the request, so a slow proxy or a closed tab does not cancel it. Failures throw an ApiError with the
 * same status the synchronous path uses (507 no_space, 413 too_large, 409 busy).
 */
export async function exportBackup(opts: { intervalMs?: number; maxMs?: number; signal?: AbortSignal } = {}): Promise<BackupInfo> {
  const { intervalMs = 2000, maxMs = 12 * 60_000, signal } = opts;
  let ans = await api<BackupAnswer>("/api/backup", { method: "POST", signal });
  const started = Date.now();
  const jobId = ans.status === "building" && "job_id" in ans ? ans.job_id : undefined;
  while (jobId && ans.status === "building") {
    if (Date.now() - started > maxMs) throw new ApiError(504, "timeout");
    await new Promise((r) => setTimeout(r, intervalMs));
    ans = await api<BackupAnswer>(`/api/backup/jobs/${jobId}`, { signal });
  }
  if (ans.status === "failed") {
    const body: Record<string, unknown> = { error: ans.error, message: ans.message };
    throw new ApiError(FAILED_STATUS[ans.error] ?? 500, ans.error, body);
  }
  if (ans.status === "building") throw new ApiError(500, "internal");
  return ans;
}
