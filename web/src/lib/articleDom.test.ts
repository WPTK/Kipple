import { describe, expect, it } from "vitest";
import { embedSrc, footnoteTarget, handleArticleClick, loadEmbed } from "./articleDom";

describe("embedSrc", () => {
  it("allows only YouTube (nocookie) and Vimeo with sane ids", () => {
    expect(embedSrc("youtube", "abc_DEF-123")).toBe("https://www.youtube-nocookie.com/embed/abc_DEF-123?autoplay=1");
    expect(embedSrc("vimeo", "12345")).toBe("https://player.vimeo.com/video/12345?dnt=1&autoplay=1");
    expect(embedSrc("youtube", 'x"><script>')).toBeNull();
    expect(embedSrc("youtube", "../evil")).toBeNull();
    expect(embedSrc("vimeo", "12/../3")).toBeNull();
    expect(embedSrc("dailymotion", "1")).toBeNull();
    expect(embedSrc(undefined, "1")).toBeNull();
  });
});

describe("embedSrc player parameters", () => {
  it("keeps a playlist, a start time and a Vimeo unlisted hash", () => {
    expect(embedSrc("youtube", "videoseries", { list: "PLx_Y-9" })).toBe(
      "https://www.youtube-nocookie.com/embed/videoseries?autoplay=1&list=PLx_Y-9",
    );
    expect(embedSrc("youtube", "abc", { list: "PL1", start: "90" })).toBe(
      "https://www.youtube-nocookie.com/embed/abc?autoplay=1&list=PL1&start=90",
    );
    expect(embedSrc("vimeo", "123", { h: "8a1b2c3d4e" })).toBe("https://player.vimeo.com/video/123?h=8a1b2c3d4e&dnt=1&autoplay=1");
  });

  it("drops values that fail their pattern and refuses a playlist player without a list", () => {
    expect(embedSrc("youtube", "videoseries")).toBeNull();
    expect(embedSrc("youtube", "videoseries", { list: "PL&x=1" })).toBeNull();
    expect(embedSrc("youtube", "abc", { list: 'a"b', start: "1e3" })).toBe("https://www.youtube-nocookie.com/embed/abc?autoplay=1");
    expect(embedSrc("vimeo", "123", { h: "../x" })).toBe("https://player.vimeo.com/video/123?dnt=1&autoplay=1");
  });

  it("loadEmbed reads the parameters from the placeholder's data attributes", () => {
    const fig = document.createElement("figure");
    fig.dataset.provider = "vimeo";
    fig.dataset.id = "123";
    fig.dataset.h = "abc";
    expect(loadEmbed(fig)?.src).toBe("https://player.vimeo.com/video/123?h=abc&dnt=1&autoplay=1");
    const pl = document.createElement("figure");
    pl.dataset.provider = "youtube";
    pl.dataset.id = "videoseries";
    pl.dataset.list = "PL1";
    expect(loadEmbed(pl)?.src).toBe("https://www.youtube-nocookie.com/embed/videoseries?autoplay=1&list=PL1");
  });
});

describe("loadEmbed", () => {
  it("does nothing for a placeholder with a bad id, and loads once for a good one", () => {
    const bad = document.createElement("figure");
    bad.dataset.provider = "youtube";
    bad.dataset.id = "a b";
    expect(loadEmbed(bad)).toBeNull();
    expect(bad.querySelector("iframe")).toBeNull();
    const ok = document.createElement("figure");
    ok.dataset.provider = "youtube";
    ok.dataset.id = "abc";
    expect(loadEmbed(ok)).not.toBeNull();
    expect(loadEmbed(ok)).toBeNull();
    expect(ok.querySelectorAll("iframe")).toHaveLength(1);
  });
});

describe("footnoteTarget", () => {
  it("only resolves kp- ids inside the article", () => {
    const root = document.createElement("div");
    root.innerHTML = '<p id="kp-fn1">note</p><p id="other">x</p>';
    expect(footnoteTarget(root, "#kp-fn1")?.id).toBe("kp-fn1");
    expect(footnoteTarget(root, "#other")).toBeNull();
    expect(footnoteTarget(root, "#kp-missing")).toBeNull();
    expect(footnoteTarget(root, "https://x")).toBeNull();
  });
});

describe("malformed footnote hrefs", () => {
  it("does not throw on a bad percent escape and still resolves a raw id", () => {
    const root = document.createElement("div");
    root.innerHTML = '<p id="kp-100%">note</p>';
    expect(() => footnoteTarget(root, "#kp-100%")).not.toThrow();
    expect(footnoteTarget(root, "#kp-100%")?.id).toBe("kp-100%");
    expect(footnoteTarget(root, "#kp-%E0%A4%A")).toBeNull();
  });

  it("still calls preventDefault for a click on such a link", () => {
    const body = document.createElement("div");
    body.innerHTML = '<a id="l" href="#kp-%zz">x</a>';
    document.body.append(body);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    const link = body.querySelector("a") as HTMLAnchorElement;
    Object.defineProperty(ev, "target", { value: link });
    expect(() => handleArticleClick(ev, body, null, false)).not.toThrow();
    expect(ev.defaultPrevented).toBe(true);
    body.remove();
  });
});
