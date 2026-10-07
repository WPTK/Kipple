import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { StatsSource } from "@/api/types";
import {
  durationLabel,
  feedRows,
  folderRows,
  heatLevel,
  hourLabel,
  mostStarred,
  parseDay,
  pctLabel,
  readRateNote,
  shortDate,
  sortRows,
  weekOrder,
} from "./statsFormat";

describe("readRateNote", () => {
  const r = (items_read: number, new_items: number) => ({ items_read, new_items, rate: new_items ? Math.min(1, items_read / new_items) : null });
  it("gives the counts, and the window's first day when it starts after the range", () => {
    expect(readRateNote(r(31, 50), "2026-09-01", "2026-09-01")).toBe("31 of 50 new");
    expect(readRateNote(r(31, 50), "2026-09-05", "2026-09-01")).toBe(`31 of 50 new since ${shortDate("2026-09-05")}`);
    expect(readRateNote(r(12, 10), "2026-09-01", "2026-09-01")).toBe("12 read, 10 new, some arrived earlier");
  });
  it("says why there is no rate", () => {
    expect(readRateNote(r(0, 0), null, "2026-09-01")).toBe("Not enough history");
    expect(readRateNote(undefined, "2026-09-01", "2026-09-01")).toBe("Not enough history");
    expect(readRateNote(r(2, 0), "2026-09-01", "2026-09-01")).toBe("Nothing new arrived");
  });
  it("a folder has no rate, a feed carries its own", () => {
    const base = { feed_id: "1", feed_title: "A", folder_id: "9", folder_name: "F", items_read: 1, opens: 1, active_seconds: 0, avg_read_seconds: null, bounce_rate: null, open_original_rate: null, stars: 0, subscribed: true, timed_seconds: 0, timed_items: 0 };
    const src: StatsSource[] = [{ ...base, read_rate: r(1, 4) }, { ...base, feed_id: "2" }];
    expect(feedRows(src).map((x) => x.read_rate)).toEqual([0.25, null]);
    expect(folderRows(src)[0]!.read_rate).toBeNull();
  });
});

describe("durationLabel", () => {
  it("rounds minutes and spells hours", () => {
    expect(durationLabel(0)).toBe("0 min");
    expect(durationLabel(20)).toBe("<1 min");
    expect(durationLabel(60)).toBe("1 min");
    expect(durationLabel(89)).toBe("1 min");
    expect(durationLabel(90)).toBe("2 min");
    expect(durationLabel(3599)).toBe("1h");
    expect(durationLabel(4800)).toBe("1h 20m");
    expect(durationLabel(7200)).toBe("2h");
    expect(durationLabel(null)).toBe("-");
  });
});

describe("small helpers", () => {
  it("formats percentages, hours and days", () => {
    expect(pctLabel(0.456)).toBe("46%");
    expect(pctLabel(null)).toBe("-");
    expect(hourLabel(0)).toBe("12 am");
    expect(hourLabel(12)).toBe("12 pm");
    expect(hourLabel(20)).toBe("8 pm");
    expect(parseDay("2026-09-01").getDate()).toBe(1);
    expect(weekOrder("sunday")).toEqual([0, 1, 2, 3, 4, 5, 6]);
    expect(weekOrder("monday")).toEqual([1, 2, 3, 4, 5, 6, 0]);
    expect(heatLevel(0, 10)).toBe(0);
    expect(heatLevel(1, 10)).toBe(1);
    expect(heatLevel(10, 10)).toBe(4);
  });
});

const src = (o: Partial<StatsSource>): StatsSource => ({
  feed_id: "1",
  feed_title: "F",
  folder_id: "10",
  folder_name: "Tech",
  items_read: 0,
  opens: 0,
  active_seconds: 0,
  timed_seconds: 0,
  timed_items: 0,
  avg_read_seconds: null,
  bounce_rate: null,
  open_original_rate: null,
  stars: 0,
  subscribed: true,
  ...o,
});

describe("rollup", () => {
  const list = [
    src({ feed_id: "1", feed_title: "A", items_read: 30, opens: 40, active_seconds: 1800, timed_seconds: 1800, timed_items: 30, avg_read_seconds: 60, bounce_rate: 0.5, open_original_rate: 0.1, stars: 2, tracked_opens: 40, bounces: 20, items_opened: 30, items_original: 3 }),
    src({ feed_id: "2", feed_title: "B", items_read: 10, opens: 60, active_seconds: 3000, timed_seconds: 3000, timed_items: 10, avg_read_seconds: 300, bounce_rate: 0.1, open_original_rate: 0.3, stars: 1, tracked_opens: 60, bounces: 6, items_opened: 10, items_original: 3 }),
    src({ feed_id: "3", feed_title: "C", folder_id: null, folder_name: null, items_read: 4, opens: 4, stars: 0 }),
  ];
  it("adds counts and weights the averages", () => {
    const rows = folderRows(list);
    const tech = rows.find((r) => r.name === "Tech")!;
    expect(tech).toMatchObject({ items_read: 40, opens: 100, active_seconds: 4800, stars: 3, count: 2 });
    expect(tech.avg_read_seconds).toBeCloseTo(4800 / 40); // timed seconds over timed items
    expect(tech.bounce_rate).toBeCloseTo(26 / 100);
    expect(tech.open_original_rate).toBeCloseTo(6 / 40);
    expect(rows.find((r) => r.name === "No folder")?.bounce_rate).toBeNull();
  });
  it("rolls the average up as timed seconds over timed items, like the server", () => {
    const legacyHeavy = [
      src({ feed_id: "1", items_read: 100, active_seconds: 600, timed_seconds: 600, timed_items: 10, avg_read_seconds: 60 }),
      src({ feed_id: "2", items_read: 5, active_seconds: 300, timed_seconds: 300, timed_items: 5, avg_read_seconds: 60 }),
    ];
    expect(folderRows(legacyHeavy)[0]?.avg_read_seconds).toBe(60); // not 900 / 105
    expect(folderRows([src({ items_read: 4 })])[0]?.avg_read_seconds).toBeNull();
  });
  it("sorts by the chosen measure and lists most starred", () => {
    expect(sortRows(feedRows(list), "items").map((r) => r.name)).toEqual(["A", "B", "C"]);
    expect(sortRows(feedRows(list), "minutes").map((r) => r.name)[0]).toBe("B");
    expect(mostStarred(feedRows(list)).map((r) => r.name)).toEqual(["A", "B"]);
  });
});

describe("comparison and months", () => {
  it("names the period before a range", async () => {
    const { previousPeriod, changeLabel, monthlyBars } = await import("./statsFormat");
    // Today (the range end) is left out of both sides: 29 complete days against the 29 before them.
    expect(previousPeriod({ key: "month", from: "2026-08-28", to: "2026-09-26" })).toEqual({ current: { from: "2026-08-28", to: "2026-09-25" }, from: "2026-07-30", to: "2026-08-27", label: "the previous 29 days" });
    expect(previousPeriod({ key: "week", from: "2026-09-20", to: "2026-09-22" })).toEqual({ current: { from: "2026-09-20", to: "2026-09-21" }, from: "2026-09-13", to: "2026-09-14", label: "the same days last week" });
    expect(previousPeriod({ key: "week", from: "2026-09-20", to: "2026-09-20" })).toBeNull(); // only today: no complete day yet
    expect(previousPeriod({ key: "all", from: "2026-01-01", to: "2026-07-19" })).toBeNull();
    const n = (x: number) => String(x);
    expect(changeLabel(142, 120, n)).toBe("+18% from 120");
    expect(changeLabel(38, 40, n)).toBe("-5% from 40");
    expect(changeLabel(7, 7, n)).toBe("no change from 7");
    expect(changeLabel(3, 0, n)).toBe("up from 0");
    const bars = monthlyBars([
      { date: "2026-01-30", items_read: 2, active_seconds: 10 },
      { date: "2026-01-31", items_read: 1, active_seconds: 5 },
      { date: "2026-03-01", items_read: 4, active_seconds: 0 },
    ]);
    expect(bars.map((b) => [b.month, b.items_read])).toEqual([["2026-01", 3], ["2026-02", 0], ["2026-03", 4]]);
  });
});

describe("day arithmetic with a pinned time zone", () => {
  const prevTz = process.env.TZ;
  beforeAll(() => {
    process.env.TZ = "America/New_York";
  });
  afterAll(() => {
    if (prevTz === undefined) delete process.env.TZ;
    else process.env.TZ = prevTz;
  });

  it("steps whole calendar days across daylight saving changes and Feb 29", async () => {
    const { addDays, previousPeriod } = await import("./statsFormat");
    expect(addDays("2026-03-07", 1)).toBe("2026-03-08");
    expect(addDays("2026-03-08", 1)).toBe("2026-03-09"); // spring forward: a 23-hour day
    expect(addDays("2026-11-01", 1)).toBe("2026-11-02"); // fall back: a 25-hour day
    expect(addDays("2026-11-02", -1)).toBe("2026-11-01");
    expect(addDays("2028-02-28", 2)).toBe("2028-03-01");
    expect(addDays("2028-03-01", -1)).toBe("2028-02-29");
    expect(previousPeriod({ key: "month", from: "2026-03-20", to: "2026-04-18" })).toEqual({ current: { from: "2026-03-20", to: "2026-04-17" }, from: "2026-02-19", to: "2026-03-19", label: "the previous 29 days" });
    expect(previousPeriod({ key: "year", from: "2028-03-01", to: "2029-02-28" })?.to).toBe("2028-02-29");
  });
});
