import { describe, expect, it } from "vitest";
import { embedSrc, footnoteTarget, loadEmbed } from "./articleDom";

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
