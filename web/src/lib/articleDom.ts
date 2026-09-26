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
