// Settings whose change the server acts on at once and cannot take back. SettingField asks first for these
// (docs/design.md 7.4: a cap of 0 purges the image cache, a lower cap evicts at once; 5: a lower
// `retention.default` enqueues a trim over every feed that inherits it).

export interface Warning {
  title: string;
  body: string;
  action: string;
}

const rank = (v: number): number => (v === 0 ? Infinity : v); // retention: 0 is unlimited

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
  if (key === "retention.default") {
    if (rank(b) < rank(a)) {
      return {
        title: "Keep fewer articles per feed?",
        body: "Articles beyond the newest ones are removed now from every feed that uses this number, read or unread. Starred articles stay. Removed articles can be restored for a while (see the setting below).",
        action: "Keep fewer",
      };
    }
    return null;
  }
  if (key === "retention.restore_days") {
    if (b < a) {
      return {
        title: "Keep restore stubs for fewer days?",
        body: b === 0 ? "Every restore stub is removed tonight and can't come back." : `Restore stubs older than ${b} day${b === 1 ? "" : "s"} are removed tonight and can't come back.`,
        action: "Keep fewer days",
      };
    }
    return null;
  }
  return null;
}
