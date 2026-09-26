// A filter the user just edited, disabled or deleted. The server cancels a running apply of that rule and ends the
// run with an error, which is what the user asked for and not a failure to report (docs/design.md 7.1b).

const touched = new Map<string, number>();
/** Long enough for the cancelled run to report back. */
const WINDOW_MS = 60_000;

export function noteFilterTouched(id: string, now: number = Date.now()): void {
  touched.set(String(id), now);
  for (const [k, t] of touched) if (now - t > WINDOW_MS) touched.delete(k);
}

export function wasFilterTouched(id: string | undefined, now: number = Date.now()): boolean {
  if (id === undefined) return false;
  const t = touched.get(String(id));
  return t !== undefined && now - t <= WINDOW_MS;
}

/** Tests. */
export function resetFilterTouched(): void {
  touched.clear();
}
