import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultLinkTarget, isAppleTouch, openExternal, resolveLinkTarget } from "./links";
import { sanitizeArticleHtml } from "./safeHtml";

const IPHONE = { userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1", platform: "iPhone", maxTouchPoints: 5 };
const IPAD_DESKTOP = { userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15", platform: "MacIntel", maxTouchPoints: 5 };
const MAC = { userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15", platform: "MacIntel", maxTouchPoints: 0 };
const WIN = { userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/130.0 Safari/537.36", platform: "Win32", maxTouchPoints: 0 };

afterEach(() => vi.unstubAllGlobals());

describe("Open links in", () => {
  it("defaults to the same tab on iPhone and iPad (even an iPad posing as a Mac), a new tab elsewhere", () => {
    expect(isAppleTouch(IPHONE)).toBe(true);
    expect(isAppleTouch(IPAD_DESKTOP)).toBe(true);
    expect(isAppleTouch(MAC)).toBe(false);
    expect(isAppleTouch(WIN)).toBe(false);
    expect(defaultLinkTarget(IPHONE)).toBe("same");
    expect(defaultLinkTarget(WIN)).toBe("new");
  });

  it("the device setting wins over the platform default", () => {
    expect(resolveLinkTarget("new", IPHONE)).toBe("new");
    expect(resolveLinkTarget("same", WIN)).toBe("same");
    expect(resolveLinkTarget(null, IPHONE)).toBe("same");
  });

  it("same tab navigates this tab (no about:blank left behind); new tab opens with noopener and noreferrer", () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    openExternal("https://espn.com/story", "same");
    expect(assign).toHaveBeenCalledWith("https://espn.com/story");
    expect(open).not.toHaveBeenCalled();
    openExternal("https://espn.com/story", "new");
    expect(open).toHaveBeenCalledWith("https://espn.com/story", "_blank", "noopener,noreferrer");
    open.mockRestore();
  });

  it("article links: target=_blank only for a new tab, rel noopener noreferrer always, footnotes untouched", () => {
    const html = '<p><a href="https://example.com/x">out</a> <a href="#kp-fn1">note</a></p>';
    const same = document.createElement("div");
    same.innerHTML = sanitizeArticleHtml(html, "same");
    const a = same.querySelectorAll("a");
    expect(a[0]?.getAttribute("target")).toBeNull();
    expect(a[0]?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(a[1]?.getAttribute("target")).toBeNull();
    const fresh = document.createElement("div");
    fresh.innerHTML = sanitizeArticleHtml(html, "new");
    const b = fresh.querySelectorAll("a");
    expect(b[0]?.getAttribute("target")).toBe("_blank");
    expect(b[0]?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(b[1]?.getAttribute("target")).toBeNull();
  });
});
