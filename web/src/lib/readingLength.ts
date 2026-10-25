// The reading-time filter of a list (docs/design.md 7.1, `min_minutes`/`max_minutes`/`unknown_length`). Three named
// lengths, carried in the list's address (`?len=`), so a list keeps it through the Unread/All/Starred switch and an open
// article's way back, and a new list starts without it. The Coffee break layout cuts a list into four more lengths
// (BREAK_LENGTHS), one for each of its sections; they are in the same field of the scope and the same table, but are not
// a choice the reader makes, so the filter menu and `?len=` know only the three. A leaf module: api/queryKeys.ts imports it.

export const READING_LENGTHS = ["short", "medium", "long"] as const;
export type ReadingLength = (typeof READING_LENGTHS)[number];

export const isReadingLength = (v: unknown): v is ReadingLength => (READING_LENGTHS as readonly unknown[]).includes(v);

/** The Coffee break's sections, in the order the page draws them. */
export const BREAK_LENGTHS = ["under3", "3to7", "8up", "unknown"] as const;
export type BreakLength = (typeof BREAK_LENGTHS)[number];

/** Every length a list can be asked for. */
export type SectionLength = ReadingLength | BreakLength;

/**
 * What each length asks of the server. Reading time is whole minutes at 230 words a minute, rounded up; the server keeps
 * `(min-1, max]` minutes of words, and `unknown` is the articles with no words counted at all, which no range holds.
 */
export const READING_LENGTH_QUERY: Record<SectionLength, { min_minutes?: number; max_minutes?: number; unknown?: true }> = {
  short: { max_minutes: 5 },
  medium: { min_minutes: 6, max_minutes: 15 },
  long: { min_minutes: 16 },
  under3: { max_minutes: 2 },
  "3to7": { min_minutes: 3, max_minutes: 7 },
  "8up": { min_minutes: 8 },
  unknown: { unknown: true },
};

export const READING_LENGTH_LABELS: Record<SectionLength, string> = {
  short: "5 min or less",
  medium: "6 to 15 min",
  long: "Over 15 min",
  under3: "Under 3 minutes",
  "3to7": "3 to 7 minutes",
  "8up": "8 minutes and up",
  unknown: "Unknown length",
};
