import { describe, expect, it } from "vitest";
import { STEPS, STEP_COUNT, WELCOME_ENTRY, stepById, welcomeGuard, welcomePath } from "./steps";

describe("the wizard's steps", () => {
  it("are two: the account before sign-in, the feeds after it", () => {
    expect(STEP_COUNT).toBe(2);
    expect(STEPS.map((s) => [s.id, s.n, s.phase])).toEqual([
      ["account", 1, "setup"],
      ["feeds", 2, "welcome"],
    ]);
    expect(stepById("feeds").title).toBe("Choose your feeds");
  });

  it("send every /welcome address but the feeds step to the feeds step", () => {
    expect(WELCOME_ENTRY).toBe(welcomePath("feeds"));
    expect(welcomeGuard("feeds")).toBeNull();
    for (const segment of [undefined, "", "timezone", "theme", "import", "address", "finish", "account", "nowhere"]) {
      expect(welcomeGuard(segment)).toBe("/welcome/feeds");
    }
  });
});
