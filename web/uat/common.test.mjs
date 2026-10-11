import { EventEmitter } from "node:events";
import { describe, expect, it } from "vitest";
import { accessControlUrl, unloadRefusal } from "./common.mjs";

// What Playwright's WebKit driver hands to `pageerror` for a console error: its splitErrorMessage cuts the text at the
// first colon, the part before is the name and the part from two characters after it is the message.
const webkitError = (text) => {
  const i = text.indexOf(":");
  const e = new Error(i !== -1 && i + 2 <= text.length ? text.substring(i + 2) : text);
  e.name = i !== -1 ? text.substring(0, i) : "";
  return e;
};
const refusal = (url) => webkitError(`Fetch API cannot load ${url} due to access control checks.`);
const origin = "http://127.0.0.1:1919";

describe("accessControlUrl", () => {
  it("reads the URL back out of the split message", () => {
    expect(String(refusal(`${origin}/api/saved-searches`))).toBe("Fetch API cannot load http: /127.0.0.1:1919/api/saved-searches due to access control checks.");
    expect(accessControlUrl(refusal(`${origin}/api/saved-searches`))?.href).toBe(`${origin}/api/saved-searches`);
    expect(accessControlUrl(refusal(`${origin}/api/items?view=all`))?.origin).toBe(origin);
  });
  it("gives the real origin of a URL that only looks like ours", () => {
    expect(accessControlUrl(refusal("https://127.0.0.1:1919/x"))?.origin).not.toBe(origin); // https, not http
    expect(accessControlUrl(refusal("http://127.0.0.1:9/x"))?.origin).not.toBe(origin); // another port
    expect(accessControlUrl(refusal("http://127.0.0.1:1919@other.invalid/x"))?.origin).toBe("http://other.invalid"); // userinfo
  });
  it("is null for any other text", () => {
    expect(accessControlUrl(webkitError(`Fetch API cannot load ${origin}/x due to access control checks. And more`))).toBeNull();
    expect(accessControlUrl(webkitError("Unhandled Promise Rejection: TypeError: Load failed"))).toBeNull();
    expect(accessControlUrl(new TypeError("Cannot read properties of undefined"))).toBeNull();
  });
});

describe("unloadRefusal", () => {
  // Just enough of a Playwright page: its events and its main frame.
  const fakePage = () => {
    const page = new EventEmitter();
    page.main = { name: "main" };
    page.mainFrame = () => page.main;
    page.nav = (url = `${origin}/feeds`) => page.emit("request", { isNavigationRequest: () => true, frame: () => page.main, url: () => url });
    page.commit = () => page.emit("framenavigated", page.main);
    return page;
  };
  it("drops a same-origin refusal only between the navigation request and the commit", () => {
    const page = fakePage();
    const is = unloadRefusal(page, origin);
    const err = refusal(`${origin}/api/saved-searches`);
    expect(is(err)).toBe(false); // no navigation: it counts
    page.nav();
    expect(is(err)).toBe(true);
    page.commit();
    expect(is(err)).toBe(false);
  });
  it("still counts everything else during a navigation", () => {
    const page = fakePage();
    const is = unloadRefusal(page, origin);
    page.nav();
    expect(is(refusal("http://127.0.0.1:9/x"))).toBe(false);
    expect(is(refusal("https://127.0.0.1:1919/x"))).toBe(false);
    expect(is(refusal("http://127.0.0.1:1919@other.invalid/x"))).toBe(false);
    expect(is(webkitError("Unhandled Promise Rejection: TypeError: Load failed"))).toBe(false);
  });
  it("ignores subresource and child-frame requests, and a failed navigation ends the window", () => {
    const page = fakePage();
    const is = unloadRefusal(page, origin);
    const err = refusal(`${origin}/api/saved-searches`);
    page.emit("request", { isNavigationRequest: () => false, frame: () => page.main, url: () => `${origin}/api/x` });
    page.emit("request", { isNavigationRequest: () => true, frame: () => ({ name: "child" }), url: () => `${origin}/frame` });
    expect(is(err)).toBe(false);
    page.nav();
    page.emit("requestfailed", { isNavigationRequest: () => true, frame: () => page.main, url: () => `${origin}/feeds` });
    expect(is(err)).toBe(false);
  });
});
