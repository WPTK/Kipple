import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// What a crawler or an audit reads before any script runs.
describe("the page head", () => {
  const html = readFileSync("index.html", "utf8");

  it("describes the app in one sentence", () => {
    expect(html).toMatch(/<meta name="description" content="[^"]{20,160}" \/>/);
  });
});

describe("robots.txt", () => {
  it("asks every crawler to stay out: Kipple is one person's reader behind a sign-in", () => {
    expect(readFileSync("public/robots.txt", "utf8")).toBe("User-agent: *\nDisallow: /\n");
  });
});
