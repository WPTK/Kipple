import { afterEach, describe, expect, it, vi } from "vitest";
import { imageNotShown, watchImages } from "./articleDom";
import { chunksOf } from "./speech";

afterEach(() => vi.unstubAllGlobals());

const body = (html: string) => {
  const d = document.createElement("div");
  d.innerHTML = html;
  document.body.append(d);
  return d;
};
const fail = (img: Element | null) => img?.dispatchEvent(new Event("error"));
/** Lets the refusal check (a promise, and for /img/ a fetch) finish. */
const settle = () => new Promise((r) => setTimeout(r, 0));
/** What a browser reports for an image whose load already failed. */
const failedAlready = (img: HTMLImageElement) => {
  Object.defineProperty(img, "complete", { value: true });
  Object.defineProperty(img, "naturalWidth", { value: 0 });
};

describe("images refused for good", () => {
  it("an image the server sent with no address becomes the cue, with its alt text and a link to the original", async () => {
    const root = body('<p>Before <img src="" alt="A chart of rivers"> after</p>');
    const stop = watchImages(root, "https://example.com/a/1", false);
    fail(root.querySelector("img"));
    await settle();
    const cue = root.querySelector(".kp-img-off");
    expect(root.querySelector("img")).toBeNull();
    expect(cue?.textContent).toBe("Image not shown: A chart of rivers Open original");
    const a = cue?.querySelector("a");
    expect(a?.getAttribute("href")).toBe("https://example.com/a/1");
    expect(a?.target).toBe("_blank");
    expect(a?.rel).toBe("noopener noreferrer");
    expect(root.querySelector("p")?.textContent?.startsWith("Before ")).toBe(true);
    stop();
    root.remove();
  });

  it("an image that failed before the watch began is found by its state", async () => {
    const root = body('<img src="" alt="early">');
    failedAlready(root.querySelector("img") as HTMLImageElement);
    watchImages(root, undefined, false);
    await settle();
    expect(root.querySelector(".kp-img-off")?.textContent).toBe("Image not shown: early");
    root.remove();
  });

  it("the image proxy's 415 is a refusal; a 502, a network failure and another site's image are not", async () => {
    const statuses: Record<string, number> = { "/img/svg": 415, "/img/down": 502 };
    const fetchMock = vi.fn((u: string) => {
      const p = new URL(u).pathname;
      return p === "/img/offline" ? Promise.reject(new TypeError("offline")) : Promise.resolve(new Response(null, { status: statuses[p] ?? 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const root = body('<img src="/img/svg" alt="svg"><img src="/img/down" alt="down"><img src="/img/offline" alt="off"><img src="https://other.example/a.png" alt="ext">');
    watchImages(root, "https://example.com/a/1", false);
    root.querySelectorAll("img").forEach((i) => fail(i));
    await settle();
    await settle();
    expect(Array.from(root.querySelectorAll(".kp-img-off > span")).map((s) => s.textContent)).toEqual(["Image not shown: svg"]);
    expect(Array.from(root.querySelectorAll("img")).map((i) => i.alt)).toEqual(["down", "off", "ext"]);
    expect(fetchMock).toHaveBeenCalledTimes(3); // never for another site's image
    root.remove();
  });

  it("asks once per image, however many times it fails", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 502 })));
    vi.stubGlobal("fetch", fetchMock);
    const root = body('<img src="/img/down">');
    watchImages(root, undefined, false);
    fail(root.querySelector("img"));
    fail(root.querySelector("img"));
    await settle();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    root.remove();
  });

  it("keeps a declared box, so nothing moves when it fails late", () => {
    const root = body('<img src="" width="600" height="300px"><img src="" width="600">');
    const [sized, half] = Array.from(root.querySelectorAll("img"));
    const cue = imageNotShown(sized as HTMLImageElement, undefined, false) as HTMLElement;
    expect(cue.style.width).toBe("600px");
    expect(cue.style.aspectRatio).toBe("600 / 300");
    expect(cue.hasAttribute("data-sized")).toBe(true);
    const loose = imageNotShown(half as HTMLImageElement, undefined, false) as HTMLElement;
    expect(loose.style.width).toBe("");
    expect(loose.hasAttribute("data-sized")).toBe(false);
    root.remove();
  });

  it("says only that the image is not shown when it has no alt text, and opens in the same tab when asked", async () => {
    const root = body('<img src="">');
    watchImages(root, "https://example.com/a/1", true);
    fail(root.querySelector("img"));
    await settle();
    expect(root.querySelector(".kp-img-off > span")?.textContent).toBe("Image not shown");
    expect(root.querySelector(".kp-img-off a")?.hasAttribute("target")).toBe(false);
    root.remove();
  });

  it("with no original (offline) there is no link; a later watch adds it to the cues already shown, and takes it away", async () => {
    const root = body('<img src="">');
    const stop = watchImages(root, undefined, false);
    fail(root.querySelector("img"));
    await settle();
    expect(root.querySelector(".kp-img-off a")).toBeNull();
    stop();
    const stop2 = watchImages(root, "https://example.com/a/1", false);
    expect(root.querySelector(".kp-img-off")?.textContent).toBe("Image not shown Open original");
    stop2();
    watchImages(root, undefined, false);
    expect(root.querySelector(".kp-img-off")?.textContent).toBe("Image not shown");
    root.remove();
  });

  it("inside a link it is plain text (no link in a link)", () => {
    const root = body('<a href="https://example.com/big.png"><img src=""></a>');
    expect(imageNotShown(root.querySelector("img") as HTMLImageElement, "https://example.com/a/1", false)).not.toBeNull();
    expect(root.querySelectorAll("a")).toHaveLength(1);
    expect(root.querySelector("a .kp-img-off")?.textContent).toBe("Image not shown");
    root.remove();
  });

  it("a picture is replaced whole", () => {
    const root = body('<picture><source srcset=""><img src="" alt="x"></picture>');
    imageNotShown(root.querySelector("img") as HTMLImageElement, undefined, false);
    expect(root.querySelector("picture, source, img")).toBeNull();
    expect(root.querySelector(".kp-img-off")).not.toBeNull();
    root.remove();
  });

  it("tracking pixels and embed thumbnails get no cue", async () => {
    const root = body('<img src="" width="1" height="1"><img src="" height="2px"><figure class="kp-embed"><img src=""></figure>');
    watchImages(root, "https://example.com/a/1", false);
    root.querySelectorAll("img").forEach((i) => fail(i));
    await settle();
    expect(root.querySelector(".kp-img-off")).toBeNull();
    expect(root.querySelectorAll("img")).toHaveLength(3);
    root.remove();
  });

  it("a size that is not a whole number of pixels is not a tracking pixel (the server's size rule)", async () => {
    const root = body('<img src="" width="1.5em"><img src="" height="2%">');
    watchImages(root, undefined, false);
    root.querySelectorAll("img").forEach((i) => fail(i));
    await settle();
    expect(root.querySelectorAll(".kp-img-off")).toHaveLength(2);
    root.remove();
  });

  it("stops watching after cleanup", async () => {
    const root = body('<img src="">');
    watchImages(root, undefined, false)();
    fail(root.querySelector("img"));
    await settle();
    expect(root.querySelector(".kp-img-off")).toBeNull();
    root.remove();
  });

  it("Listen does not read the cue", () => {
    const root = body('<p>Before <img src="" alt="A chart"> after.</p>');
    imageNotShown(root.querySelector("img") as HTMLImageElement, "https://example.com/a/1", false);
    expect(chunksOf(root).map((c) => c.text.replace(/\s+/g, " ").trim())).toEqual(["Before after."]);
    root.remove();
  });
});
