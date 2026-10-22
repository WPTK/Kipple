import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { dayLabel, weekBucketLabel, withHeaders } from "./format";

// Local calendar arithmetic, so the zone is pinned: New York has a 23-hour day (March) and a 25-hour day (November).
let zone: string | undefined;
beforeAll(() => {
  zone = process.env.TZ;
  process.env.TZ = "America/New_York";
});
afterAll(() => {
  if (zone === undefined) delete process.env.TZ;
  else process.env.TZ = zone;
});

const at = (y: number, mo: number, d: number, h = 12, mi = 0) => new Date(y, mo - 1, d, h, mi).getTime();
const unix = (ms: number) => ms / 1000;

describe("weekBucketLabel", () => {
  it("cuts at local midnight and at the local Monday", () => {
    const now = at(2026, 6, 10); // a Wednesday
    const label = (ms: number) => weekBucketLabel(unix(ms), now);
    expect(label(at(2026, 6, 10, 0, 0))).toBe("Today");
    expect(label(at(2026, 6, 10, 23, 59))).toBe("Today");
    expect(label(at(2026, 6, 9, 23, 59))).toBe("Yesterday");
    expect(label(at(2026, 6, 9, 0, 0))).toBe("Yesterday");
    expect(label(at(2026, 6, 8, 23, 59))).toBe("This week"); // Monday
    expect(label(at(2026, 6, 8, 0, 0))).toBe("This week");
    expect(label(at(2026, 6, 7, 23, 59))).toBe("Earlier"); // Sunday
  });

  it("puts an article dated after now under Today", () => {
    expect(weekBucketLabel(unix(at(2026, 6, 12)), at(2026, 6, 10))).toBe("Today");
  });

  it("on a Monday has no This week: the days before are last week", () => {
    const now = at(2026, 6, 8);
    expect(weekBucketLabel(unix(at(2026, 6, 7)), now)).toBe("Yesterday");
    expect(weekBucketLabel(unix(at(2026, 6, 6)), now)).toBe("Earlier");
  });

  it("holds across the 23-hour day of the spring DST change (2026-03-08)", () => {
    const now = at(2026, 3, 11); // Wednesday
    const label = (ms: number) => weekBucketLabel(unix(ms), now);
    expect(label(at(2026, 3, 10, 0, 30))).toBe("Yesterday");
    expect(label(at(2026, 3, 9, 0, 30))).toBe("This week");
    expect(label(at(2026, 3, 8, 23, 30))).toBe("Earlier"); // the short Sunday
    expect(label(at(2026, 3, 8, 1, 30))).toBe("Earlier");
    // Yesterday is the day before even when it was 23 hours long.
    const monday = at(2026, 3, 9, 8);
    expect(weekBucketLabel(unix(at(2026, 3, 8, 1, 30)), monday)).toBe("Yesterday");
    expect(weekBucketLabel(unix(at(2026, 3, 7, 23, 30)), monday)).toBe("Earlier");
  });

  it("holds across the 25-hour day of the autumn DST change (2026-11-01)", () => {
    const now = at(2026, 11, 2, 9); // Monday
    const label = (ms: number) => weekBucketLabel(unix(ms), now);
    expect(label(at(2026, 11, 2, 0, 0))).toBe("Today");
    expect(label(at(2026, 11, 1, 23, 30))).toBe("Yesterday");
    expect(label(at(2026, 11, 1, 0, 30))).toBe("Yesterday");
    expect(label(at(2026, 10, 31, 23, 59))).toBe("Earlier");
  });

  it("holds across a year boundary", () => {
    const now = at(2027, 1, 1); // Friday
    const label = (ms: number) => weekBucketLabel(unix(ms), now);
    expect(label(at(2026, 12, 31, 23, 59))).toBe("Yesterday");
    expect(label(at(2026, 12, 28, 0, 0))).toBe("This week"); // the Monday of the week that holds January 1st
    expect(label(at(2026, 12, 27, 23, 59))).toBe("Earlier");
  });
});

describe("withHeaders", () => {
  const item = (id: string, ms: number) => ({ id, sort_at: unix(ms) });
  const now = at(2026, 6, 10);
  const newest = [item("a", at(2026, 6, 10, 9)), item("b", at(2026, 6, 10, 8)), item("c", at(2026, 6, 9, 20)), item("d", at(2026, 6, 8, 20)), item("e", at(2026, 6, 2, 20)), item("f", at(2026, 6, 1, 20))];
  const shape = (rows: ReturnType<typeof withHeaders<(typeof newest)[number]>>) => rows.map((r) => (r.kind === "header" ? `# ${r.label}` : r.item.id));

  it("day: one header per local day, the default", () => {
    expect(shape(withHeaders(newest, "day", now))).toEqual([
      "# Today",
      "a",
      "b",
      "# Yesterday",
      "c",
      `# ${dayLabel(unix(at(2026, 6, 8)), now)}`,
      "d",
      `# ${dayLabel(unix(at(2026, 6, 2)), now)}`,
      "e",
      `# ${dayLabel(unix(at(2026, 6, 1)), now)}`,
      "f",
    ]);
  });

  it("week: Today, Yesterday, This week, Earlier, each once", () => {
    expect(shape(withHeaders(newest, "week", now))).toEqual(["# Today", "a", "b", "# Yesterday", "c", "# This week", "d", "# Earlier", "e", "f"]);
  });

  it("week, oldest first: the same buckets in time order", () => {
    expect(shape(withHeaders([...newest].reverse(), "week", now))).toEqual(["# Earlier", "f", "e", "# This week", "d", "# Yesterday", "c", "# Today", "b", "a"]);
  });

  it("none: only the articles, in order", () => {
    expect(shape(withHeaders(newest, "none", now))).toEqual(["a", "b", "c", "d", "e", "f"]);
  });

  it("gives every header a distinct key", () => {
    const keys = withHeaders(newest, "week", now).map((r) => r.key);
    expect(new Set(keys).size).toBe(keys.length);
  });
});
