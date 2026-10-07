import { beforeEach, describe, expect, it } from "vitest";
import { existingRestoreKeyHeaders, forgetRestoreKeyForTests, restoreKey } from "./restoreKey";

const FORM = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/;

beforeEach(() => {
  localStorage.clear();
  forgetRestoreKeyForTests();
});

describe("the restore owner key", () => {
  it("is made only when a restore call needs it", () => {
    expect(existingRestoreKeyHeaders()).toEqual({});
    expect(localStorage.getItem("kipple.restoreKey")).toBeNull();
    const k = restoreKey();
    expect(k).toMatch(FORM);
    expect(localStorage.getItem("kipple.restoreKey")).toBe(k);
    expect(existingRestoreKeyHeaders()).toEqual({ "X-Kipple-Restore-Key": k });
  });

  it("stays the same for this page when storage is cleared meanwhile", () => {
    const k = restoreKey();
    localStorage.clear();
    expect(restoreKey()).toBe(k);
    localStorage.setItem("kipple.restoreKey", "A".repeat(42) + "E");
    expect(restoreKey()).toBe(k);
  });

  it("is read from storage, so a reload keeps it", () => {
    const k = restoreKey();
    forgetRestoreKeyForTests(); // a reload
    expect(restoreKey()).toBe(k);
  });

  it("replaces a stored value of the wrong form", () => {
    for (const bad of ["short", "A".repeat(43) + "A", "A".repeat(42) + "B", "A".repeat(42) + "+"]) {
      localStorage.setItem("kipple.restoreKey", bad);
      forgetRestoreKeyForTests();
      expect(existingRestoreKeyHeaders()).toEqual({});
      const k = restoreKey();
      expect(k).not.toBe(bad);
      expect(k).toMatch(FORM);
      expect(localStorage.getItem("kipple.restoreKey")).toBe(k);
    }
  });
});
