import { describe, expect, it } from "vitest";
import { cancelledLoad } from "./common.mjs";

// What Playwright's WebKit hands to `pageerror` for a console message: the text before the first colon is the name.
const webkitMessage = (text) => {
  const i = text.indexOf(":");
  const e = new Error(text.slice(i + 1).trimStart());
  e.name = text.slice(0, i);
  return e;
};
const origin = "http://127.0.0.1:1919";

describe("cancelledLoad", () => {
  it("is WebKit's message for a same-origin load a navigation cancelled", () => {
    expect(cancelledLoad(webkitMessage("Fetch API cannot load http://127.0.0.1:1919/api/saved-searches due to access control checks."), origin)).toBe(true);
    // Playwright's split eats a slash; the text arrives as "http: /127.0.0.1:1919/...".
    expect(cancelledLoad("Fetch API cannot load http: /127.0.0.1:1919/api/items?view=all due to access control checks.", origin)).toBe(true);
  });
  it("still counts another origin's refusal, a real unhandled rejection and any other error", () => {
    expect(cancelledLoad(webkitMessage("Fetch API cannot load http://127.0.0.1:9/x due to access control checks."), origin)).toBe(false);
    expect(cancelledLoad(webkitMessage("Unhandled Promise Rejection: TypeError: Load failed"), origin)).toBe(false);
    expect(cancelledLoad(new TypeError("Cannot read properties of undefined"), origin)).toBe(false);
  });
});
