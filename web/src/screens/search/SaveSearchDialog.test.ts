import { describe, expect, it } from "vitest";
import { ApiError } from "@/api/client";
import { defaultSearchName, savedSearchError } from "./SaveSearchDialog";

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

describe("savedSearchError", () => {
  it("names the saved search that already runs the same search", () => {
    const e = new ApiError(409, "already_saved", { message: "this search is already saved as Rust" });
    expect(savedSearchError(e)).toBe("This search is already saved as Rust.");
    expect(savedSearchError(new ApiError(409, "already_saved"))).toBe("This search is already saved.");
  });
});
