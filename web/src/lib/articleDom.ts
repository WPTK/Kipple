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

/** Images that fired an error event, kept for the next watch of the same body. */
const erred = new WeakSet<HTMLImageElement>();

/** An image the server sent with no address: it refused it before any request (an SVG named as one). */
const noAddress = (img: HTMLImageElement): boolean =>
  !(img.getAttribute("src") ?? "").trim() && !(img.getAttribute("srcset") ?? "").trim();

/**
 * Whether an image that fired an error was refused for good, which is the only failure that gets the cue: it has no
 * address, or the image proxy answers 415 (a type Kipple does not display, seen by its bytes; one more request, same
 * origin, only after a real error event). Any other answer, a 200 included, means the failure was not a refusal (the
 * network or the source failed, and the image may load next time), so the image is left as it is. The body is never
 * read, and `signal` stops the check when the article closes.
 */
async function refused(img: HTMLImageElement, signal: AbortSignal): Promise<boolean> {
  if (noAddress(img)) return true;
  const doc = img.ownerDocument;
  let u: URL;
  try {
    u = new URL(img.currentSrc || img.getAttribute("src")!.trim(), doc.baseURI);
  } catch {
    return false;
  }
  if (u.origin !== doc.location?.origin || !u.pathname.startsWith("/img/")) return false;
  try {
    const res = await fetch(u.href, { credentials: "same-origin", signal });
    void res.body?.cancel().catch(() => {});
    return res.status === 415;
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
  if (img.dataset.kp) cue.dataset.kp = img.dataset.kp; // it stands where the picture stood, centred the same
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
 * Watches an article body for images refused for good and gives each the cue. An image the server sent with no
 * address gets it at once, with no request. Any other image is checked only after its error event (which does not
 * bubble, so it is caught on the way down): not by its state, because a lazy image below the fold that has not loaded
 * yet looks failed in some browsers, and checking it would fetch it. The HTML goes in during the same commit as this
 * runs, and load errors come in later tasks, so no error is missed. Cues already in the body get the link for
 * `original` (pass undefined while the browser is offline: the original cannot open). Returns the cleanup, which also
 * stops checks still in flight.
 */
export function watchImages(root: HTMLElement, original: string | undefined, sameTab: boolean): () => void {
  const stop = new AbortController();
  const asked = new WeakSet<HTMLImageElement>();
  const check = (img: HTMLImageElement) => {
    if (asked.has(img) || isBeacon(img) || img.closest("figure.kp-embed")) return;
    asked.add(img);
    void refused(img, stop.signal).then((yes) => {
      if (yes && !stop.signal.aborted) imageNotShown(img, original, sameTab);
    });
  };
  for (const cue of root.querySelectorAll<HTMLElement>(`.${NOT_SHOWN_CLASS}`)) setCueLink(cue, original, sameTab);
  // No address, or an error seen by an earlier watch of this body whose check was stopped (the link changed mid-check).
  for (const img of root.querySelectorAll("img")) if (noAddress(img) || erred.has(img)) check(img);
  const onError = (e: Event) => {
    const img = e.target;
    if (!(img instanceof HTMLImageElement) || !root.contains(img)) return;
    erred.add(img);
    check(img);
  };
  root.addEventListener("error", onError, true);
  return () => {
    stop.abort();
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

/** A cell of a table: short text stays on one line, a number also sits on the end edge in tabular figures. */
const NUMBER = /^[(+\-\u2212$\u20ac\u00a3\u00a5\s]*\d[\d,.\s\u00a0]*(%|[kKmMbB])?\)?$/;
const SHORT_CELL = 12;

/** Mark table cells that hold a number or only a few characters (CSS reads data-kp). Cells with markup other than inline text are left alone. */
export function enhanceTables(root: ParentNode): void {
  for (const cell of root.querySelectorAll<HTMLElement>("td, th")) {
    if (cell.dataset.kp || cell.querySelector("table, p, ul, ol, div, img, pre")) continue;
    const text = (cell.textContent ?? "").trim();
    if (text === "") continue;
    if (NUMBER.test(text)) cell.dataset.kp = "num";
    else if (text.length <= SHORT_CELL) cell.dataset.kp = "short";
  }
}

/**
 * Code blocks and tables wider than the column scroll sideways, so they need to be reachable without a mouse: while one
 * overflows it takes focus (tabindex 0), and says what it is (a code block is a labelled region; a table keeps its
 * role and gets a name). They are checked again when the column or the text size changes. Returns the cleanup.
 */
export function watchScrollRegions(root: HTMLElement): () => void {
  const regions = [...root.querySelectorAll<HTMLElement>("pre, table")];
  const apply = (el: HTMLElement) => {
    const over = el.scrollWidth > el.clientWidth + 1;
    if (over) {
      el.tabIndex = 0;
      if (el.tagName === "PRE") el.setAttribute("role", "region");
      // A table with a caption or its own name already has one; a code block has none, so it gets "Code".
      const named = el.hasAttribute("aria-label") || el.hasAttribute("aria-labelledby") || (el.tagName === "TABLE" && el.querySelector(":scope > caption") !== null);
      if (!named) {
        el.setAttribute("aria-label", el.tagName === "PRE" ? "Code" : "Table");
        el.dataset.kpName = "1";
      }
    } else {
      el.removeAttribute("tabindex");
      if (el.tagName === "PRE") el.removeAttribute("role");
      if (el.dataset.kpName) {
        el.removeAttribute("aria-label");
        delete el.dataset.kpName;
      }
    }
  };
  if (typeof ResizeObserver === "undefined") {
    regions.forEach(apply);
    return () => undefined;
  }
  const ro = new ResizeObserver((entries) => entries.forEach((e) => apply(e.target as HTMLElement)));
  regions.forEach((el) => ro.observe(el));
  return () => ro.disconnect();
}

const LINE_BREAKERS = new Set(["ADDRESS", "ARTICLE", "ASIDE", "BLOCKQUOTE", "BR", "DETAILS", "DIV", "DL", "FIGCAPTION", "FIGURE", "FOOTER", "H1", "H2", "H3", "H4", "H5", "H6", "HEADER", "HR", "LI", "MAIN", "NAV", "OL", "P", "PRE", "SECTION", "TABLE", "UL"]);

const NOT_ALONE_MEDIA = new Set(["IMG", "VIDEO", "SVG", "PICTURE", "IFRAME"]);

/** True when no text and no other inline content sits beside this node, up to a line break or the edge of its block. */
function noneBeside(node: Node): boolean {
  for (const dir of ["previousSibling", "nextSibling"] as const) {
    for (let n = node[dir]; n; n = n[dir]) {
      if (n.nodeType === Node.TEXT_NODE) {
        if ((n.textContent ?? "").trim() !== "") return false;
      } else if (n.nodeType === Node.ELEMENT_NODE) {
        const el = n as Element;
        if (LINE_BREAKERS.has(el.tagName)) break;
        if (NOT_ALONE_MEDIA.has(el.tagName) || (el.textContent ?? "").trim() !== "" || el.querySelector("img, video, svg")) return false;
      }
    }
  }
  return true;
}

/**
 * True when nothing else sits on the picture's line. Looks through every inline wrapper (a link, a span, a picture
 * element, strong...): at each level nothing but the wrapper's own markup may sit beside it, until a block ends the line.
 */
function aloneOnItsLine(img: Element): boolean {
  for (let node: Element | null = img; node; node = node.parentElement) {
    if (!noneBeside(node)) return false;
    if (node.parentElement && LINE_BREAKERS.has(node.parentElement.tagName)) return true;
  }
  return true;
}

/**
 * Mark a picture that stands alone on its line (data-kp="block": CSS centres it, so a picture capped in height sits in
 * the middle of the column), and a picture or cue the same. A picture in a line of text, beside others in a row of
 * icons, or in a table cell (where the author's cell alignment rules) is left alone. A block picture with both sizes
 * declared also gets them as --kp-w and --kp-h, so CSS can narrow its box with its height (see index.css).
 */
export function enhanceImages(root: ParentNode): void {
  for (const img of root.querySelectorAll<HTMLImageElement>(`img, .${NOT_SHOWN_CLASS}`)) {
    if (img.dataset.kp || img.closest("figure.kp-embed, td, th")) continue;
    if (!aloneOnItsLine(img)) continue;
    img.dataset.kp = "block";
    const w = declared(img, "width");
    const h = declared(img, "height");
    if (w && h && img.tagName === "IMG") {
      img.dataset.sized = "";
      img.style.setProperty("--kp-w", String(w));
      img.style.setProperty("--kp-h", String(h));
    }
  }
}
