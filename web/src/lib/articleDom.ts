// DOM behaviour for server-transformed article HTML (docs/design.md 7.7 and 7.8):
// click-to-load embeds, footnote links that scroll inside the article, and nothing else.
// No inline script, no eval; iframes exist only for the two allowed hosts and only after a tap.

// Same patterns as internal/sanitize/serve.go, which already filtered what reaches the placeholder.
const YOUTUBE_ID = /^[A-Za-z0-9_-]{1,64}$/;
const YOUTUBE_LIST = /^[A-Za-z0-9_-]{1,64}$/;
const START = /^\d{1,6}$/;
const VIMEO_ID = /^\d{1,20}$/;
const VIMEO_HASH = /^[0-9a-f]{1,32}$/;

export const SANDBOX = "allow-scripts allow-same-origin allow-presentation allow-popups";
export const ALLOW = "autoplay; fullscreen; picture-in-picture";

/** The player parameters a placeholder may carry (data-list, data-start, data-h). */
export interface EmbedParams {
  list?: string;
  start?: string;
  h?: string;
}

/**
 * The iframe URL for an embed placeholder, or null when the provider or id is not allowed. A parameter
 * that fails its pattern is left out; a YouTube playlist player (id "videoseries") needs a valid list.
 */
export function embedSrc(provider: string | undefined, id: string | undefined, params: EmbedParams = {}): string | null {
  if (!provider || !id) return null;
  if (provider === "youtube" && YOUTUBE_ID.test(id)) {
    const list = params.list && YOUTUBE_LIST.test(params.list) ? params.list : "";
    if (id === "videoseries" && !list) return null;
    let q = "autoplay=1";
    if (list) q += `&list=${list}`;
    if (params.start && START.test(params.start)) q += `&start=${params.start}`;
    return `https://www.youtube-nocookie.com/embed/${id}?${q}`;
  }
  if (provider === "vimeo" && VIMEO_ID.test(id)) {
    const h = params.h && VIMEO_HASH.test(params.h) ? `h=${params.h}&` : "";
    return `https://player.vimeo.com/video/${id}?${h}dnt=1&autoplay=1`;
  }
  return null;
}

const figureSrc = (fig: HTMLElement): string | null =>
  embedSrc(fig.dataset.provider, fig.dataset.id, { list: fig.dataset.list, start: fig.dataset.start, h: fig.dataset.h });

const providerName = (p: string | undefined): string => (p === "vimeo" ? "Vimeo" : "YouTube");

/** Build the sandboxed iframe for a placeholder and put it in place of the thumbnail. */
export function loadEmbed(figure: HTMLElement): HTMLIFrameElement | null {
  if (figure.dataset.loaded === "1") return null;
  const src = figureSrc(figure);
  if (!src) return null;
  const doc = figure.ownerDocument;
  const frame = doc.createElement("iframe");
  frame.src = src;
  frame.setAttribute("sandbox", SANDBOX);
  frame.setAttribute("allow", ALLOW);
  frame.setAttribute("referrerpolicy", "strict-origin-when-cross-origin");
  frame.setAttribute("allowfullscreen", "");
  frame.title = `${providerName(figure.dataset.provider)} video player`;
  frame.className = "kp-embed-frame";
  figure.querySelector("img")?.remove();
  figure.querySelector("button.kp-embed-play")?.remove();
  figure.prepend(frame);
  figure.dataset.loaded = "1";
  return frame;
}

/** Give each placeholder a real Play button (the placeholder itself is not focusable). */
export function enhanceEmbeds(root: ParentNode): void {
  for (const fig of root.querySelectorAll<HTMLElement>("figure.kp-embed[data-provider][data-id]")) {
    if (fig.querySelector("button.kp-embed-play") || fig.dataset.loaded === "1") continue;
    if (!figureSrc(fig)) continue;
    const b = fig.ownerDocument.createElement("button");
    b.type = "button";
    b.className = "kp-embed-play";
    b.setAttribute("aria-label", `Play ${providerName(fig.dataset.provider)} video`);
    b.textContent = "Play";
    fig.prepend(b);
  }
}

/** The class of the "Image not shown" cue. Read-aloud and keyword highlights skip its text. */
export const NOT_SHOWN_CLASS = "kp-img-off";

/** A whole number of pixels from a width or height attribute ("300" or "300px"), else null. */
const declared = (img: HTMLImageElement, k: "width" | "height"): number | null => {
  const m = /^(\d+)(?:px)?$/.exec((img.getAttribute(k) ?? "").trim());
  return m ? Number(m[1]) : null;
};

/**
 * A declared size this small is a tracking pixel, not artwork. This is only the size half of the server's rule
 * (tinyDim in internal/sanitize/leadimage.go); its list of known beacon addresses (isTracker) is not repeated here.
 */
const isBeacon = (img: HTMLImageElement): boolean => ["width", "height"].some((k) => (declared(img, k as "width") ?? 3) <= 2);

/**
 * Whether a failed image was refused for good, which is the only failure that gets the cue: the server sent it with no
 * address (an SVG named as one, or a source it cannot proxy), or the image proxy answers 415 (an SVG seen by its
 * bytes; one more request, same origin, only after the image failed). Anything else (offline, a timeout, a source
 * that is down) may work next time, so the image is left as it is.
 */
async function refused(img: HTMLImageElement): Promise<boolean> {
  const addr = img.currentSrc || img.getAttribute("src")?.trim() || "";
  if (!addr) return true;
  const doc = img.ownerDocument;
  let u: URL;
  try {
    u = new URL(addr, doc.baseURI);
  } catch {
    return false;
  }
  if (u.origin !== doc.location?.origin || !u.pathname.startsWith("/img/")) return false;
  try {
    return (await fetch(u.href, { credentials: "same-origin" })).status === 415;
  } catch {
    return false;
  }
}

/** Puts the "Open original" link in a cue, or takes it out: none inside another link, none without an address. */
function setCueLink(cue: HTMLElement, original: string | undefined, sameTab: boolean): void {
  cue.querySelector(":scope > a")?.previousSibling?.remove(); // the space before it
  cue.querySelector(":scope > a")?.remove();
  if (!original || cue.parentElement?.closest("a")) return;
  const a = cue.ownerDocument.createElement("a");
  a.href = original;
  a.rel = "noopener noreferrer";
  if (!sameTab) a.target = "_blank";
  a.textContent = "Open original";
  cue.append(" ", a); // the space keeps the text (read aloud, copied) two phrases
}

/**
 * Puts the "Image not shown" cue in place of an article image, and returns it (null when the image gets none). The cue
 * keeps the image's alt text and, when both sizes are declared, its box (so a late failure moves no text), and offers
 * the original article, where the image can be seen. Inside a link it is plain text (the link still works). Tracking
 * pixels and embed thumbnails (the embed has its own link) get no cue.
 */
export function imageNotShown(img: HTMLImageElement, original: string | undefined, sameTab: boolean): HTMLElement | null {
  if (!img.isConnected || isBeacon(img) || img.closest(`figure.kp-embed, .${NOT_SHOWN_CLASS}`)) return null;
  const doc = img.ownerDocument;
  const cue = doc.createElement("span");
  cue.className = NOT_SHOWN_CLASS;
  const alt = (img.getAttribute("alt") ?? "").trim();
  const label = doc.createElement("span");
  label.textContent = alt ? `Image not shown: ${alt}` : "Image not shown";
  cue.append(label);
  const w = declared(img, "width");
  const h = declared(img, "height");
  if (w && h) {
    cue.dataset.sized = "";
    cue.style.width = `${w}px`;
    cue.style.aspectRatio = `${w} / ${h}`;
  }
  // A <picture> is one image: the cue takes the place of the whole of it.
  const box = img.parentElement?.tagName === "PICTURE" ? img.parentElement : img;
  box.replaceWith(cue);
  setCueLink(cue, original, sameTab);
  return cue;
}

/**
 * Watches an article body for images refused for good and gives each the cue. An image that already failed before
 * this ran (the HTML went in during an earlier task, or an article opened again from the cache) is found by its state;
 * a later failure by its error event, which does not bubble, so it is caught on the way down. Cues already in the body
 * get the link for `original` (pass undefined while the browser is offline: the original cannot open). Returns the
 * cleanup.
 */
export function watchImages(root: HTMLElement, original: string | undefined, sameTab: boolean): () => void {
  let live = true;
  const asked = new WeakSet<HTMLImageElement>();
  const check = (img: HTMLImageElement) => {
    if (asked.has(img) || isBeacon(img) || img.closest("figure.kp-embed")) return;
    asked.add(img);
    void refused(img).then((yes) => {
      if (yes && live) imageNotShown(img, original, sameTab);
    });
  };
  for (const cue of root.querySelectorAll<HTMLElement>(`.${NOT_SHOWN_CLASS}`)) setCueLink(cue, original, sameTab);
  for (const img of root.querySelectorAll("img")) if (img.complete && img.naturalWidth === 0) check(img);
  const onError = (e: Event) => {
    if (e.target instanceof HTMLImageElement && root.contains(e.target)) check(e.target);
  };
  root.addEventListener("error", onError, true);
  return () => {
    live = false;
    root.removeEventListener("error", onError, true);
  };
}

/** Element a `#kp-...` footnote link points at, inside `root`. */
export function footnoteTarget(root: ParentNode, href: string): HTMLElement | null {
  if (!href.startsWith("#kp-") || href.length < 5) return null;
  let id = href.slice(1);
  try {
    id = decodeURIComponent(id);
  } catch {
    // A malformed escape (a stray %): match the raw id instead of throwing before preventDefault runs.
  }
  for (const el of root.querySelectorAll<HTMLElement>("[id]")) if (el.id === id) return el;
  return null;
}

/** Scroll `target` to the top of the article container, inside it (never the page). */
export function scrollWithin(scroller: HTMLElement, target: HTMLElement, smooth: boolean): void {
  const top = target.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop - 12;
  scroller.scrollTo({ top: Math.max(0, top), behavior: smooth ? "smooth" : "auto" });
  if (!target.hasAttribute("tabindex")) target.setAttribute("tabindex", "-1");
  target.focus({ preventScroll: true });
}

/** One delegated click handler for the article body. Returns true when it handled the click. */
export function handleArticleClick(e: MouseEvent | React.MouseEvent, body: HTMLElement, scroller: HTMLElement | null, smooth: boolean): boolean {
  const t = e.target;
  if (!(t instanceof Element)) return false;

  const link = t.closest<HTMLElement>("a[href^='#kp-'], area[href^='#kp-']");
  if (link && body.contains(link)) {
    const target = footnoteTarget(body, link.getAttribute("href") ?? "");
    e.preventDefault();
    if (target && scroller) scrollWithin(scroller, target, smooth);
    return true;
  }

  const fig = t.closest<HTMLElement>("figure.kp-embed");
  if (fig && body.contains(fig) && !t.closest("a")) {
    e.preventDefault();
    const frame = loadEmbed(fig);
    frame?.focus();
    return true;
  }
  return false;
}
