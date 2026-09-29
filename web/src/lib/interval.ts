/** A feed check interval in minutes, as words ("30 minutes", "1 hour", "1 hour 40 minutes", "1 day"). Any
 * minute count is handled, not just exact multiples of 60/1440 — a custom per-feed interval need not be one. */
export function intervalLabel(m: number): string {
  const days = Math.floor(m / 1440);
  const hours = Math.floor((m % 1440) / 60);
  const mins = m % 60;
  const parts: string[] = [];
  if (days > 0) parts.push(`${days} day${days === 1 ? "" : "s"}`);
  if (hours > 0) parts.push(`${hours} hour${hours === 1 ? "" : "s"}`);
  if (mins > 0) parts.push(`${mins} minute${mins === 1 ? "" : "s"}`);
  return parts.length ? parts.join(" ") : "0 minutes";
}

/** The same, as how often: "Every 30 minutes", "Every hour", "Every day". */
export function everyLabel(m: number): string {
  const l = intervalLabel(m);
  const singular = /^1 (minute|hour|day)$/.exec(l);
  return singular ? `Every ${singular[1]}` : `Every ${l}`;
}
