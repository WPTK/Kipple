import { describe, expect, it } from "vitest";
import { SCHEMES } from "./schemes";
import { pickSchemes } from "./serverThemes";

describe("pickSchemes", () => {
  it("offers every local scheme until the server's list is known", () => {
    expect(pickSchemes(undefined)).toBe(SCHEMES);
    expect(pickSchemes([])).toBe(SCHEMES);
  });

  it("the server's ids are authoritative: names and colors come from the local file", () => {
    const got = pickSchemes(["paper", "midnight", "not-in-this-build"]);
    expect(got.map((s) => s.id)).toEqual(["paper", "midnight"]);
    expect(got[0]?.name).toBe("Paper");
  });

  it("falls back to the local list when the server names none this build has", () => {
    expect(pickSchemes(["only-new-ones"])).toBe(SCHEMES);
  });
});
