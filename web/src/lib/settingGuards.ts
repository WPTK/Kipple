// Settings whose change the server acts on at once and cannot take back. SettingField asks first for these
// (docs/design.md 7.4: a cap of 0 purges the image cache, a lower cap evicts at once; 5: a lower
// `retention.default` enqueues a trim over every feed that inherits it; that one has its own dialog, RetentionDialog).

export interface Warning {
  title: string;
  body: string;
  action: string;
}

const rank = (v: number): number => (v === 0 ? Infinity : v); // retention: 0 is unlimited

/** Whether going from `current` to `next` keeps fewer articles per feed, so a trim removes some now (0 is unlimited). The dialog is RetentionDialog, which asks the server how many. */
export function lowersRetention(current: unknown, next: unknown): boolean {
  const a = Number(current);
  const b = Number(next);
  return Number.isFinite(a) && Number.isFinite(b) && rank(b) < rank(a);
}

/**
 * What to ask before `key` goes from `current` to `next`, or null when the change is harmless. `next` is the value
 * that would be stored (a reset resolves to the default before it gets here).
 */
export function settingWarning(key: string, current: unknown, next: unknown): Warning | null {
  const a = Number(current);
  const b = Number(next);
  if (!Number.isFinite(a) || !Number.isFinite(b) || a === b) return null;
  if (key === "imgproxy.cache_mb") {
    if (b === 0) return { title: "Turn the image cache off?", body: "This removes every cached image now. Articles load their pictures from the sites again until you turn it back on.", action: "Turn off and clear" };
    if (b < a) return { title: "Make the image cache smaller?", body: "This removes the oldest cached images now, until the rest fit.", action: "Make it smaller" };
    return null;
  }
  if (key === "retention.restore_days") {
    if (b < a) {
      return {
        title: "Keep removed articles for fewer days?",
        body: b === 0 ? "No removed article can be brought back after tonight." : `Articles removed more than ${b} day${b === 1 ? "" : "s"} ago can no longer be brought back after tonight.`,
        action: "Keep fewer days",
      };
    }
    return null;
  }
  return null;
}
