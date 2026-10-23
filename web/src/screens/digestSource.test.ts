import { describe, expect, it } from "vitest";
import { oldestAsOf } from "./digestSource";

describe("oldestAsOf", () => {
  it("is the smallest as_of, compared as numbers", () => {
    expect(oldestAsOf(["900", "1000", "95"])).toBe("95");
  });
  it("ignores sections without one, and one that is not a number", () => {
    expect(oldestAsOf([undefined, "12", "x", "30"])).toBe("12");
  });
  it("is undefined when no section has one", () => {
    expect(oldestAsOf([undefined, undefined])).toBeUndefined();
    expect(oldestAsOf([])).toBeUndefined();
  });
});
