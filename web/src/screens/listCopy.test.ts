import { describe, expect, it } from "vitest";
import type { Scope } from "@/api/types";
import { emptyCopy } from "./listCopy";

const all: Scope = { view: "all" };

describe("emptyCopy: a list with no articles yet names its own place", () => {
  it("says this feed on a feed's list, never these feeds", () => {
    expect(emptyCopy({ ...all, feed: "3" }).body).toBe("Kipple hasn't fetched anything from this feed yet.");
  });

  it("says the feeds in this folder on a folder's list", () => {
    expect(emptyCopy({ ...all, folder: "2" }, 0, undefined, 4).body).toBe("Kipple hasn't fetched anything from the feeds in this folder yet.");
  });

  it("says your feeds on the whole library", () => {
    expect(emptyCopy(all).body).toBe("Kipple hasn't fetched anything from your feeds yet.");
  });
});
