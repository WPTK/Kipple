// The restore calls of the setup wizard (before there is an account). Nothing here knows about React.
import { ApiError, api, clientKind } from "@/api/client";

export type PasswordState = "password" | "none_access" | "open";

/** What the server found in an uploaded Kipple backup zip. */
export interface BackupSummary {
  kind: "backup";
  kipple_version: string;
  created_at: string;
  feeds: number;
  items: number;
  starred: number;
  username: string;
  password_state: PasswordState;
  needs_new_password: boolean;
  new_password_reason: "" | "access_unavailable" | "open_refused";
  estimate_seconds: number;
}

/** What the server found in an uploaded OPML file. */
export interface OpmlSummary {
  kind: "opml";
  feeds: number;
}

/** What the upload answers: an OPML file is read at once; a zip is then checked on the server (poll the status). */
export type UploadResult = OpmlSummary | { state: "checking" };

/** Where a restore is: the same names in GET /api/instance and GET /api/setup/restore. */
export type RestoreState = "none" | "uploading" | "checking" | "ready" | "failed" | "confirmed";

/** GET /api/setup/restore. */
export interface RestoreStatus {
  state: RestoreState;
  /** When ready, and again once confirmed (so a reloaded page can still name the account). */
  summary?: BackupSummary;
  /** When failed. */
  error?: { code: string; message: string };
  estimate_seconds?: number;
}

export const fetchRestoreStatus = () => api<RestoreStatus>("/api/setup/restore", { anon: true, quiet: true });

/**
 * Sends the file as the raw request body. fetch cannot report upload progress, so this uses XMLHttpRequest. Answers
 * with what the server found, or throws an ApiError (status 0 when the network failed or the upload was cancelled).
 */
export function uploadRestoreFile(file: File, onProgress: (sent: number, total: number) => void, signal?: AbortSignal): Promise<UploadResult> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/setup/restore/upload");
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.setRequestHeader("X-Kipple-Client", clientKind());
    xhr.setRequestHeader("Accept", "application/json");
    xhr.withCredentials = true;
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded, e.total);
    };
    xhr.onerror = () => reject(new ApiError(0, "network"));
    xhr.onabort = () => reject(new ApiError(0, "aborted"));
    xhr.onload = () => {
      let body: Record<string, unknown> | null = null;
      try {
        body = JSON.parse(xhr.responseText) as Record<string, unknown>;
      } catch {
        /* not JSON */
      }
      if (xhr.status >= 200 && xhr.status < 300 && body) {
        onProgress(file.size, file.size);
        resolve(body as unknown as UploadResult);
        return;
      }
      const code = typeof body?.error === "string" ? body.error : "http_" + xhr.status;
      reject(new ApiError(xhr.status, code, body));
    };
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(file);
  });
}

/** Confirms the uploaded backup. The server answers, then stops so it can start again with the backup in place. */
export const confirmRestore = (newPassword?: string) =>
  api<{ restarting: boolean; estimate_seconds: number }>("/api/setup/restore/confirm", {
    method: "POST",
    body: newPassword ? { new_password: newPassword } : {},
    anon: true,
  });

/** Cancels an upload that was not confirmed, and removes it from the server. */
export const cancelRestore = () => api("/api/setup/restore", { method: "DELETE", anon: true });

/** The feeds file of the uploaded backup, as a file the import step can send. The server forgets the upload after this. */
export async function fetchBackupFeeds(): Promise<File> {
  let res: Response;
  try {
    res = await fetch("/api/setup/restore/feeds", { headers: { "X-Kipple-Client": clientKind() }, credentials: "same-origin", redirect: "manual" });
  } catch {
    throw new ApiError(0, "network");
  }
  if (!res.ok) {
    let body: Record<string, unknown> | null = null;
    let code = "http_" + res.status;
    try {
      body = (await res.json()) as Record<string, unknown>;
      if (typeof body.error === "string") code = body.error;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code, body);
  }
  return new File([await res.text()], "feeds.opml", { type: "text/x-opml" });
}

/** The server's own message for a failed restore call (it is written for the reader), else a plain fallback. */
export function restoreErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 0) return "Kipple couldn't reach the server.";
    const m = e.body?.message;
    if (typeof m === "string" && m) return m;
  }
  return "Something went wrong. Try again.";
}

/** Whole minutes, at least one, for "about N minutes". */
export const estimateMinutes = (seconds: number): number => Math.max(1, Math.ceil(seconds / 60));

/** A byte count for people: 12 MB, 1.4 GB. */
export function sizeText(bytes: number): string {
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}
