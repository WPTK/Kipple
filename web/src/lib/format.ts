const DAY = 86_400;

/** Short relative time for a list row: "now", "5m", "3h", "2d", then "Sep 12". Unix seconds in. */
export function relativeTime(unix: number, nowMs: number = Date.now()): string {
  const diff = Math.floor(nowMs / 1000) - unix;
  if (diff < 60) return "now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < DAY) return `${Math.floor(diff / 3600)}h`;
  if (diff < 7 * DAY) return `${Math.floor(diff / DAY)}d`;
  const d = new Date(unix * 1000);
  const sameYear = d.getFullYear() === new Date(nowMs).getFullYear();
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(sameYear ? {} : { year: "numeric" }) });
}

export function fullDate(unix: number): string {
  return new Date(unix * 1000).toLocaleString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function localDayStart(ms: number): number {
  const d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** "Today", "Yesterday", or "Wed, Sep 23" for the local day of a unix-seconds time. */
export function dayLabel(unix: number, nowMs: number = Date.now()): string {
  const days = Math.round((localDayStart(nowMs) - localDayStart(unix * 1000)) / (DAY * 1000));
  if (days <= 0) return "Today";
  if (days === 1) return "Yesterday";
  const d = new Date(unix * 1000);
  const sameYear = d.getFullYear() === new Date(nowMs).getFullYear();
  return d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", ...(sameYear ? {} : { year: "numeric" }) });
}

export type Row<T> = { kind: "header"; key: string; label: string } | { kind: "item"; key: string; item: T };

/** Interleave day headers into an ordered list (by each item's sort time). */
export function withDayHeaders<T extends { id: string; sort_at: number }>(items: T[], nowMs: number = Date.now()): Row<T>[] {
  const rows: Row<T>[] = [];
  let last = "";
  for (const item of items) {
    const label = dayLabel(item.sort_at, nowMs);
    if (label !== last) {
      rows.push({ kind: "header", key: `h:${label}`, label });
      last = label;
    }
    rows.push({ kind: "item", key: item.id, item });
  }
  return rows;
}

/** "3 hours ago" or "in 20 minutes" for a unix-seconds time; "never" for null. */
export function whenLabel(unix: number | null | undefined, nowMs: number = Date.now()): string {
  if (!unix) return "Never";
  const diff = Math.round(unix - nowMs / 1000);
  const abs = Math.abs(diff);
  const [n, unit] = abs < 60 ? [abs, "second"] : abs < 3600 ? [Math.round(abs / 60), "minute"] : abs < DAY ? [Math.round(abs / 3600), "hour"] : [Math.round(abs / DAY), "day"];
  if (abs < 45) return diff <= 0 ? "Just now" : "Any moment";
  const span = `${n} ${unit}${n === 1 ? "" : "s"}`;
  return diff < 0 ? `${span} ago` : `in ${span}`;
}

export function bytesLabel(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}
