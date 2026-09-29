import { describe, expect, it } from "vitest";
import { everyLabel, intervalLabel } from "./interval";

describe("intervalLabel", () => {
  it("formats clean multiples of an hour or a day exactly as before", () => {
    expect(intervalLabel(15)).toBe("15 minutes");
    expect(intervalLabel(30)).toBe("30 minutes");
    expect(intervalLabel(60)).toBe("1 hour");
    expect(intervalLabel(120)).toBe("2 hours");
    expect(intervalLabel(1440)).toBe("1 day");
    expect(intervalLabel(10080)).toBe("7 days");
  });

  it("formats a minute count that isn't a clean multiple as combined units, not a raw decimal", () => {
    expect(intervalLabel(90)).toBe("1 hour 30 minutes");
    expect(intervalLabel(100)).toBe("1 hour 40 minutes");
    expect(intervalLabel(200)).toBe("3 hours 20 minutes");
    expect(intervalLabel(1500)).toBe("1 day 1 hour");
  });
});

describe("everyLabel", () => {
  it("drops the leading '1' only for a single, whole unit", () => {
    expect(everyLabel(30)).toBe("Every 30 minutes");
    expect(everyLabel(60)).toBe("Every hour");
    expect(everyLabel(1440)).toBe("Every day");
  });

  it("keeps the leading '1' when it's part of a combined label", () => {
    expect(everyLabel(100)).toBe("Every 1 hour 40 minutes");
  });
});
