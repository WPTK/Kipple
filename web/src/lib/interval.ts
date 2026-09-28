/** A feed check interval in minutes, as words ("30 minutes", "1 hour", "6 hours", "1 day"). */
export const intervalLabel = (m: number): string =>
  m < 60 ? `${m} minutes` : m === 60 ? "1 hour" : m < 1440 ? `${m / 60} hours` : m === 1440 ? "1 day" : `${m / 1440} days`;

/** The same, as how often: "Every 30 minutes", "Every hour", "Every day". */
export function everyLabel(m: number): string {
  const l = intervalLabel(m);
  return l.startsWith("1 ") ? `Every ${l.slice(2)}` : `Every ${l}`;
}
