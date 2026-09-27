import { describe, expect, it } from "vitest";
import type { StatsSource } from "@/api/types";
import { durationLabel, feedRows, folderRows, heatLevel, hourLabel, mostStarred, parseDay, pctLabel, sortRows, weekOrder } from "./statsFormat";

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
