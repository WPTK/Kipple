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
