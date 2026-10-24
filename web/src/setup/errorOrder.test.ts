import { describe, expect, it } from "vitest";
import { ApiError } from "@/api/client";
import { accountFailure } from "./api";
import { restoreErrorText } from "./restoreApi";

function kipple(status: number, code: string, body: Record<string, unknown> | null = null) {
  const e = new ApiError(status, code, body);
  e.fromKipple = true;
  return e;
}

describe("an answer from Kipple keeps its own message, a missing answer is said by cause", () => {
  it("503 access_unavailable still says Cloudflare Access cannot be checked", () => {
    expect(accountFailure(kipple(503, "access_unavailable"))).toEqual({ kind: "form", message: "Kipple can't check your Cloudflare Access sign-in right now. Try again in a moment." });
  });

  it("507 no_space keeps the server's own words", () => {
    expect(restoreErrorText(kipple(507, "no_space", { message: "Not enough room: 40 MB needed, 10 MB free." }))).toBe("Not enough room: 40 MB needed, 10 MB free.");
  });

  it("no answer at all is said by cause", () => {
    expect(accountFailure(new ApiError(0, "network"))).toMatchObject({ kind: "form", message: expect.stringMatching(/^(You're offline|Kipple can't be reached)\.$/) });
    expect(restoreErrorText(new ApiError(0, "network"))).toMatch(/^(You're offline|Kipple can't be reached)\.$/);
  });
});
