import { describe, expect, it } from "vitest";
import { sanitizeArticleHtml } from "./safeHtml";
import { handleArticleClick } from "./articleDom";

function dom(html: string): HTMLElement {
  const root = document.createElement("div");
  root.innerHTML = sanitizeArticleHtml(html);
  return root;
}

describe("sanitizeArticleHtml links", () => {
  it("keeps footnote links in the article (no target, even when the server sent one)", () => {
    const root = dom('<p><a href="#kp-fn1" target="_blank">1</a> <a href="#kp-fnref1">back</a></p>');
    for (const a of root.querySelectorAll("a")) {
      expect(a.hasAttribute("target")).toBe(false);
    }
  });

  it("opens external links in a new tab with noopener", () => {
    const a = dom('<a href="https://example.com/x">x</a>').querySelector("a")!;
    expect(a.getAttribute("target")).toBe("_blank");
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("scrolls a footnote link inside the article container, not the page", () => {
    const scroller = document.createElement("div");
    const body = dom('<p><a href="#kp-fn1">1</a></p><p id="kp-fn1">note</p>');
    scroller.append(body);
    document.body.append(scroller);
    const scrollTo = (scroller.scrollTo = (() => undefined) as unknown as typeof scroller.scrollTo);
    let called = 0;
    scroller.scrollTo = ((...a: unknown[]) => {
      called++;
      void a;
    }) as typeof scroller.scrollTo;
    void scrollTo;
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "target", { value: body.querySelector("a") });
    expect(handleArticleClick(ev, body, scroller, false)).toBe(true);
    expect(ev.defaultPrevented).toBe(true);
    expect(called).toBe(1);
    scroller.remove();
  });
});
