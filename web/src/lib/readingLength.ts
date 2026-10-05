// The reading-time filter of a list (docs/design.md 7.1, `min_minutes`/`max_minutes`). Three named lengths, carried in
// the list's address (`?len=`), so a list keeps it through the Unread/All/Starred switch and an open article's way back,
// and a new list starts without it. A leaf module: api/queryKeys.ts imports it.

export const READING_LENGTHS = ["short", "medium", "long"] as const;
export type ReadingLength = (typeof READING_LENGTHS)[number];

export const isReadingLength = (v: unknown): v is ReadingLength => (READING_LENGTHS as readonly unknown[]).includes(v);

/** Reading time is whole minutes at 230 words a minute, rounded up; the server keeps `(min-1, max]` minutes of words. */
export const READING_LENGTH_MINUTES: Record<ReadingLength, { min_minutes?: number; max_minutes?: number }> = {
  short: { max_minutes: 5 },
  medium: { min_minutes: 6, max_minutes: 15 },
  long: { min_minutes: 16 },
};

export const READING_LENGTH_LABELS: Record<ReadingLength, string> = {
  short: "5 min or less",
  medium: "6 to 15 min",
  long: "Over 15 min",
};
