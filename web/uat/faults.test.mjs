import assert from "node:assert/strict";
import { test } from "node:test";
import { INTERNAL_ERROR, faultReason, faultStart } from "./faults.mjs";

const f = (text, jar = true) => ({ text, jar });

test("no fault: every finding is about Kipple", () => {
  assert.equal(faultStart([f("K3 x: 2 selected"), f("Q1 count: 0")], true), -1);
  assert.equal(faultStart([], true), -1);
});

test("a finding before WebKit's internal error stays; the error and what follows are the fault", () => {
  const found = [f("desktop K3 x: 2 selected"), f(`desktop U: stopped: page.goto: ${INTERNAL_ERROR}`), f("desktop U3: no message", false)];
  assert.equal(faultStart(found, false), 1);
  assert.equal(faultReason(found[1]), "WebKit reported an internal error");
});

test("a cookie the browser dropped while the server still accepts it is a fault from the first finding after the loss", () => {
  const found = [f("desktop Q1 refused: no refusal"), f("desktop Q1 count: 0 saved searches", false), f("desktop U: 401", false)];
  assert.equal(faultStart(found, true), 1);
  assert.match(faultReason(found[1]), /dropped the session cookie/);
});

test("a session the server ended is a finding, not a fault", () => {
  assert.equal(faultStart([f("desktop U: 401", false)], false), -1);
});

test("a cookie still in the jar is a finding even when the server answers 401", () => {
  assert.equal(faultStart([f("desktop U: 401", true)], true), -1);
});

test("a finding from before sign-in (jar unknown) is not a cookie fault", () => {
  assert.equal(faultStart([f("desktop: the run stopped", null)], true), -1);
});
