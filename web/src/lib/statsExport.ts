import type { StatsRange } from "@/api/types";

export type ExportFormat = "csv" | "json" | "jsonl";
export type ExportContent = "raw" | "summary";
export type ExportRange = StatsRange | "custom";

export interface ExportOptions {
  format: ExportFormat;
  content: ExportContent;
  range: ExportRange;
  from: string;
  to: string;
  titles: boolean;
  /** CSV only: a byte-order mark at the start, so Excel shows accents. */
  bom?: boolean;
}

export const EXPORT_PATH = "/api/stats/export";
export const DICTIONARY_PATH = "/api/stats/dictionary";
export const DICTIONARY_MD_URL = `${DICTIONARY_PATH}?format=md`;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** A real calendar date in YYYY-MM-DD form. */
function validDate(s: string): boolean {
  if (!DATE.test(s)) return false;
  const d = new Date(`${s}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
}

/** The longest custom export range the server accepts, in days, both ends counted. */
export const MAX_EXPORT_DAYS = 3660;

/** Whole days from a to b (both valid YYYY-MM-DD). */
function dayDiff(a: string, b: string): number {
  return Math.round((Date.parse(`${b}T00:00:00Z`) - Date.parse(`${a}T00:00:00Z`)) / 86_400_000);
}

/** Why the custom dates are not usable, or null. `cap` is the export limit; deleting has none. */
export function rangeProblem(from: string, to: string, cap = false): string | null {
  if (!validDate(from) || !validDate(to)) return "Choose both a start and an end date.";
  if (from > to) return "The start date must be on or before the end date.";
  if (cap && dayDiff(from, to) > MAX_EXPORT_DAYS - 1) return "Choose a range of at most 3,660 days (about 10 years), or use All.";
  return null;
}

/** The download address for the chosen options. Summary is JSON only, whatever format was picked. */
export function exportUrl(o: ExportOptions): string {
  const q = new URLSearchParams();
  q.set("format", o.content === "summary" ? "json" : o.format);
  q.set("content", o.content);
  if (o.range === "custom") {
    q.set("from", o.from);
    q.set("to", o.to);
  } else q.set("range", o.range);
  q.set("titles", o.titles ? "1" : "0");
  if (o.bom && o.content === "raw" && o.format === "csv") q.set("bom", "1");
  return `${EXPORT_PATH}?${q.toString()}`;
}

/** A plain browser download (an attachment answer): no fetch, so nothing large is held in memory. */
export function startDownload(url: string): void {
  const a = document.createElement("a");
  a.href = url;
  a.download = "";
  a.rel = "noopener";
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
}
