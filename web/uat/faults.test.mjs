// Run by vitest with the web app's tests (`npm test`): the rules of faults.mjs, which need no browser.
import { describe, expect, it } from "vitest";
import { INTERNAL_ERROR, faultReason, faultStart } from "./faults.mjs";

const f = (text, jar = true) => ({ text, jar });

describe("faultStart", () => {
  it("no fault: every finding is about Kipple", () => {
    expect(faultStart([f("K3 x: 2 selected"), f("Q1 count: 0")], true)).toBe(-1);
    expect(faultStart([], true)).toBe(-1);
  });

  it("a finding before WebKit's internal error stays; the error and what follows are the fault", () => {
    const found = [f("desktop K3 x: 2 selected"), f(`desktop U: stopped: page.goto: ${INTERNAL_ERROR}`), f("desktop U3: no message", false)];
    expect(faultStart(found, false)).toBe(1);
    expect(faultReason(found[1])).toBe("WebKit reported an internal error");
  });

  it("a cookie gone from the browser while the server still accepts it is a fault from the first finding after the loss", () => {
    const found = [f("desktop Q1 refused: no refusal"), f("desktop Q1 count: 0 saved searches", false), f("desktop U: 401", false)];
    expect(faultStart(found, true)).toBe(1);
    expect(faultReason(found[1])).toMatch(/vanished .*cause unproven: the browser or Kipple/);
  });

  it("a session the server ended is a finding, not a fault", () => {
    expect(faultStart([f("desktop U: 401", false)], false)).toBe(-1);
  });

  it("a cookie still in the jar is a finding even when the server answers 401", () => {
    expect(faultStart([f("desktop U: 401", true)], true)).toBe(-1);
  });

  it("a finding from before sign-in (jar unknown) is not a cookie fault", () => {
    expect(faultStart([f("desktop: the run stopped", null)], true)).toBe(-1);
  });
});
