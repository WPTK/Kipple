import { describe, expect, it } from "vitest";
import { buttonVariants } from "./button";

describe("buttonVariants", () => {
  it("never puts danger text on the selection fill: it is under 4.5:1 in six schemes", () => {
    const danger = buttonVariants({ variant: "danger" });
    expect(danger).toContain("text-danger");
    expect(danger).not.toContain("bg-selection");
  });
});
