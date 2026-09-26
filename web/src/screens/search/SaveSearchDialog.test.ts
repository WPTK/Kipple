import { describe, expect, it } from "vitest";
import { defaultSearchName } from "./SaveSearchDialog";

describe("defaultSearchName", () => {
  it("cuts at 60 code points, as the server counts, never inside a surrogate pair", () => {
    const q = "a".repeat(59) + "😀😀";
    const name = defaultSearchName(q);
    expect(Array.from(name)).toHaveLength(60);
    expect(name.endsWith("😀")).toBe(true);
    expect(/[\uD800-\uDBFF]$/.test(name)).toBe(false);
    expect(defaultSearchName("😀".repeat(70))).toBe("😀".repeat(60));
    expect(defaultSearchName("short")).toBe("short");
  });
});
