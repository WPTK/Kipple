import { describe, expect, it } from "vitest";
import { safeHttpUrl } from "./safeUrl";

describe("safeHttpUrl", () => {
  it("passes absolute http and https addresses", () => {
    expect(safeHttpUrl("https://example.com/a?b=1#c")).toBe("https://example.com/a?b=1#c");
    expect(safeHttpUrl("  http://example.com  ")).toBe("http://example.com/");
    expect(safeHttpUrl("HTTPS://EXAMPLE.COM/")).toBe("https://example.com/");
  });

  it("refuses every other scheme, relative paths and nothing", () => {
    for (const bad of ["javascript:alert(1)", " JavaScript:alert(1)", "data:text/html,<p>x</p>", "vbscript:x", "file:///etc/passwd", "/local", "example.com", "", null, undefined]) {
      expect(safeHttpUrl(bad)).toBeUndefined();
    }
  });
});
