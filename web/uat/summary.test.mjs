import { describe, expect, it } from "vitest";
import { failureLines } from "./summary.mjs";

describe("failureLines", () => {
  it("keeps the FAIL lines of a flow that printed some", () => {
    expect(failureLines("noise\nFAIL a\nFAIL b\n", 1)).toEqual(["FAIL a", "FAIL b"]);
  });
  it("shows the last non-empty stderr line of a crash", () => {
    expect(failureLines("at x\nTypeError: boom\n\n", 1)).toEqual(["exit 1 with no FAIL line; last stderr: TypeError: boom"]);
    expect(failureLines("", 1)).toEqual(["exit 1 with no FAIL line and no stderr"]);
  });
  it("shows the spawn error, and nothing for a clean exit", () => {
    expect(failureLines("", 2, new Error("spawn ENOENT"))).toEqual(["could not run: spawn ENOENT"]);
    expect(failureLines("", 0)).toEqual([]);
  });
});
