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

describe("image-map areas", () => {
  it("get the same link rules as anchors", () => {
    const root = document.createElement("div");
    root.innerHTML = sanitizeArticleHtml(
      '<img usemap="#kp-m" src="https://e.com/i.png"><map name="kp-m"><area href="https://e.com/x" target="_self" shape="rect"><area href="#kp-fn1" target="_blank"></map>',
    );
    const [out, fn] = [...root.querySelectorAll("area")];
    expect(out?.getAttribute("target")).toBe("_blank");
    expect(out?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(fn?.hasAttribute("target")).toBe(false);
  });

  it("scroll a footnote area inside the article like an anchor", () => {
    const scroller = document.createElement("div");
    const body = dom('<map name="kp-m"><area href="#kp-fn1" shape="rect"></map><p id="kp-fn1">note</p>');
    scroller.append(body);
    document.body.append(scroller);
    let called = 0;
    scroller.scrollTo = (() => {
      called++;
    }) as typeof scroller.scrollTo;
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "target", { value: body.querySelector("area") });
    expect(handleArticleClick(ev, body, scroller, false)).toBe(true);
    expect(ev.defaultPrevented).toBe(true);
    expect(called).toBe(1);
    scroller.remove();
  });

  it("drop the target in same-tab mode but keep rel", () => {
    const root = document.createElement("div");
    root.innerHTML = sanitizeArticleHtml('<map name="kp-m"><area href="https://e.com/x" target="_blank"></map>', "same");
    const area = root.querySelector("area");
    expect(area?.hasAttribute("target")).toBe(false);
    expect(area?.getAttribute("rel")).toBe("noopener noreferrer");
  });
});

describe("Same tab links", () => {
  it("leaves external links without a target but keeps in-page and footnote anchors as they are", () => {
    const root = document.createElement("div");
    root.innerHTML = sanitizeArticleHtml('<p><a href="https://e.com/x">out</a> <a href="#kp-fn1" target="_blank">1</a> <a href="#section">jump</a></p>', "same");
    const [out, fn, jump] = [...root.querySelectorAll("a")];
    expect(out?.hasAttribute("target")).toBe(false);
    expect(out?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(fn?.hasAttribute("target")).toBe(false);
    expect(fn?.getAttribute("href")).toBe("#kp-fn1");
    expect(jump?.getAttribute("href")).toBe("#section");
    expect(jump?.hasAttribute("target")).toBe(false);
  });
});
