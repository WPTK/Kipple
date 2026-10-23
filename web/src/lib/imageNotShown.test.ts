import { afterEach, describe, expect, it, vi } from "vitest";
import { imageNotShown, watchImages } from "./articleDom";
import { chunksOf } from "./speech";
import { setBrowserOnline } from "./offlineState";

afterEach(() => {
  vi.unstubAllGlobals();
  setBrowserOnline(true);
});

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

  it("a picture that fails while the browser is offline says it is out of reach, with no request", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    setBrowserOnline(false);
    const root = body('<img src="/img/a" alt="Harbour">');
    watchImages(root, undefined, false);
    fail(root.querySelector("img"));
    await settle();
    expect(root.querySelector(".kp-img-off")?.textContent).toBe("Picture not available offline: Harbour");
    expect(fetchMock).not.toHaveBeenCalled();
    root.remove();
  });

  it("puts the picture back when the network returns, with the same watch or a new one", async () => {
    vi.stubGlobal("fetch", vi.fn());
    setBrowserOnline(false);
    const root = body('<img src="/img/a" alt="Harbour">');
    const stop = watchImages(root, undefined, false);
    fail(root.querySelector("img"));
    await settle();
    expect(root.querySelector(".kp-img-off")).not.toBeNull();
    setBrowserOnline(true);
    expect(root.querySelector(".kp-img-off")).toBeNull();
    expect(root.querySelector("img")?.getAttribute("src")).toBe("/img/a");
    stop();
    // The article pane attaches a fresh watch on the flip: it restores what the old one could not hear.
    setBrowserOnline(false);
    const root2 = body('<img src="/img/b" alt="Quay">');
    const stop2 = watchImages(root2, undefined, false);
    fail(root2.querySelector("img"));
    await settle();
    stop2();
    setBrowserOnline(true);
    watchImages(root2, "https://example.com/a/1", false);
    expect(root2.querySelector(".kp-img-off")).toBeNull();
    expect(root2.querySelector("img")).not.toBeNull();
    root.remove();
    root2.remove();
  });

  it("an image with no address gets the cue when the watch begins, with no error event and no request", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const root = body('<img src=" " alt="early">');
    watchImages(root, undefined, false);
    await settle();
    expect(root.querySelector(".kp-img-off")?.textContent).toBe("Image not shown: early");
    expect(fetchMock).not.toHaveBeenCalled();
    root.remove();
  });

  it("an image that only looks failed (lazy, below the fold, not loaded yet) is not fetched when the watch begins", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 415 })));
    vi.stubGlobal("fetch", fetchMock);
    const root = body('<img src="/img/a" loading="lazy"><img src="/img/b" loading="lazy">');
    root.querySelectorAll("img").forEach((i) => failedAlready(i));
    const stop = watchImages(root, undefined, false);
    await settle();
    stop();
    watchImages(root, "https://example.com/a/1", false); // the online flip attaches again
    await settle();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(root.querySelectorAll("img")).toHaveLength(2);
    root.remove();
  });

  it("a check still in flight when the article closes or switches adds no cue, and its request is stopped", async () => {
    let answer: (r: Response) => void = () => {};
    const signals: AbortSignal[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((_u: string, init: RequestInit) => {
        signals.push(init.signal as AbortSignal);
        return new Promise<Response>((r) => (answer = r));
      }),
    );
    // Closed: the cleanup runs, then the proxy answers 415.
    const closed = body('<img src="/img/svg">');
    const stop = watchImages(closed, undefined, false);
    fail(closed.querySelector("img"));
    stop();
    answer(new Response(null, { status: 415 }));
    await settle();
    expect(closed.querySelector(".kp-img-off")).toBeNull();
    expect(signals[0]?.aborted).toBe(true);
    // Switched: the same body shows another article (new HTML, a new watch) before the old answer comes back.
    const pane = body('<img src="/img/svg" alt="old">');
    const stopOld = watchImages(pane, "https://example.com/a/1", false);
    fail(pane.querySelector("img"));
    const late = answer;
    stopOld();
    pane.innerHTML = '<p>The next article.</p><img src="/img/photo" alt="new">';
    watchImages(pane, "https://example.com/a/2", false);
    late(new Response(null, { status: 415 }));
    await settle();
    expect(pane.querySelector(".kp-img-off")).toBeNull();
    expect(pane.querySelector("img")?.alt).toBe("new");
    expect(signals[1]?.aborted).toBe(true);
    closed.remove();
    pane.remove();
  });

  it("an image whose check was stopped by a new watch of the same body is checked again", async () => {
    const fetchMock = vi.fn((_u: string, init: RequestInit) =>
      init.signal?.aborted ? Promise.reject(new DOMException("aborted", "AbortError")) : Promise.resolve(new Response(null, { status: 415 })),
    );
    vi.stubGlobal("fetch", fetchMock);
    const root = body('<img src="/img/svg" alt="chart">');
    const stop = watchImages(root, undefined, false);
    fail(root.querySelector("img"));
    stop(); // the browser went online mid-check: the link changes and the watch starts again
    watchImages(root, "https://example.com/a/1", false);
    await settle();
    await settle();
    expect(root.querySelector(".kp-img-off")?.textContent).toBe("Image not shown: chart Open original");
    expect(fetchMock).toHaveBeenCalledTimes(2);
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
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 415 })));
    vi.stubGlobal("fetch", fetchMock);
    const root = body('<img src="/img/svg">');
    watchImages(root, undefined, false)();
    fail(root.querySelector("img"));
    await settle();
    expect(root.querySelector(".kp-img-off")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    root.remove();
  });

  it("Listen does not read the cue", () => {
    const root = body('<p>Before <img src="" alt="A chart"> after.</p>');
    imageNotShown(root.querySelector("img") as HTMLImageElement, "https://example.com/a/1", false);
    expect(chunksOf(root).map((c) => c.text.replace(/\s+/g, " ").trim())).toEqual(["Before after."]);
    root.remove();
  });
});
