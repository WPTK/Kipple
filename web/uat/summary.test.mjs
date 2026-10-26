import { describe, expect, it } from "vitest";
import { failureLines, knownFailProblems, legVerdict } from "./summary.mjs";

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

describe("knownFail", () => {
  const manifest = { chromium: ["a.mjs", "b.mjs"], webkit: ["a.mjs"], firefox: [] };
  it("accepts an empty or valid entry", () => {
    expect(knownFailProblems({}, manifest)).toEqual([]);
    expect(knownFailProblems(undefined, manifest)).toEqual([]);
    expect(knownFailProblems({ "a.mjs": { webkit: "#12" } }, manifest)).toEqual([]);
  });
  it("rejects a missing or malformed issue", () => {
    expect(knownFailProblems({ "a.mjs": { webkit: "" } }, manifest)).toHaveLength(1);
    expect(knownFailProblems({ "a.mjs": { webkit: "12" } }, manifest)).toHaveLength(1);
    expect(knownFailProblems({ "a.mjs": { webkit: "#12 later" } }, manifest)).toHaveLength(1);
    expect(knownFailProblems({ "a.mjs": { webkit: null } }, manifest)).toHaveLength(1);
  });
  it("rejects a flow not in that engine's list, and an unknown engine", () => {
    expect(knownFailProblems({ "b.mjs": { webkit: "#1" } }, manifest)).toEqual(["knownFail b.mjs: not a webkit flow"]);
    expect(knownFailProblems({ "a.mjs": { safari: "#1" } }, manifest)[0]).toMatch(/unknown engine safari/);
  });
  it("reports a failing listed flow as known and does not count it", () => {
    expect(legVerdict("a.mjs", 1, "#12")).toMatchObject({ status: "known", exit: 0, label: "known (issue #12) a.mjs (exit 1)" })
    expect(legVerdict("a.mjs", 1, "#12", undefined, "FAIL x broke").detail).toBe("known (issue #12) a.mjs: FAIL x broke");
  });
  it("fails a listed flow that passes", () => {
    expect(legVerdict("a.mjs", 0, "#12")).toEqual({
      status: "fail",
      exit: 1,
      label: "FAIL a.mjs passed but is listed in knownFail (#12): remove the entry",
    });
  });
  it("keeps a crash, exit 2 or spawn error of a listed flow a failure", () => {
    expect(legVerdict("a.mjs", 2, "#12")).toMatchObject({ status: "fail", exit: 2 });
    expect(legVerdict("a.mjs", 1, "#12", new Error("spawn ENOENT"))).toMatchObject({ status: "fail", exit: 1 });
  });
  it("rejects #0", () => {
    expect(knownFailProblems({ "a.mjs": { webkit: "#0" } }, manifest)).toHaveLength(1);
  });
  it("leaves an unlisted flow as it was", () => {
    expect(legVerdict("a.mjs", 0, undefined).status).toBe("ok");
    expect(legVerdict("a.mjs", 1, undefined)).toMatchObject({ status: "fail", exit: 1 });
  });
});
