import { describe, expect, it } from "vitest";
import { embedSrc, enhanceImages, enhanceTables, footnoteTarget, handleArticleClick, loadEmbed } from "./articleDom";

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

describe("enhanceTables", () => {
  it("marks numbers and short cells, and leaves long text and rich cells alone", () => {
    const root = document.createElement("div");
    root.innerHTML =
      "<table><tr><th>Name</th><th>Price</th><th>Notes</th></tr>" +
      "<tr><td>Widget</td><td>$1,234.50</td><td>A longer sentence that should wrap like a paragraph.</td></tr>" +
      "<tr><td>Gadget</td><td>-12%</td><td><p>Block</p></td></tr></table>";
    enhanceTables(root);
    const kp = [...root.querySelectorAll("td, th")].map((c) => (c as HTMLElement).dataset.kp ?? "");
    expect(kp).toEqual(["short", "short", "short", "short", "num", "", "short", "num", ""]);
  });
});
describe("enhanceImages", () => {
  const kinds = (html: string) => {
    const root = document.createElement("div");
    root.innerHTML = html;
    enhanceImages(root);
    return [...root.querySelectorAll("img")].map((i) => i.dataset.kp ?? "");
  };
  it("marks a picture that stands alone on its line, whatever wraps it", () => {
    expect(kinds('<img src="a.png">')).toEqual(["block"]);
    expect(kinds('<p><img src="a.png"></p>')).toEqual(["block"]);
    expect(kinds('<div><img src="a.png"></div>')).toEqual(["block"]);
    expect(kinds('<picture><source><img src="a.png"></picture>')).toEqual(["block"]);
    expect(kinds('<p><a href="/x"><img src="a.png"></a></p>')).toEqual(["block"]);
    expect(kinds('<figure><img src="a.png"><figcaption>Caption</figcaption></figure>')).toEqual(["block"]);
    expect(kinds('<p>Text</p><img src="a.png"><p>More</p>')).toEqual(["block"]);
  });
  it("leaves a picture in a line of text or in a row of icons alone", () => {
    expect(kinds('<p>Text <img src="a.png"> more</p>')).toEqual([""]);
    expect(kinds('<p><img src="a.png"> <img src="b.png"> <img src="c.png"></p>')).toEqual(["", "", ""]);
    expect(kinds('<p><img src="a.png"> caption</p>')).toEqual([""]);
    expect(kinds('<p><img src="a.png"><br><img src="b.png"></p>')).toEqual(["block", "block"]);
  });
});