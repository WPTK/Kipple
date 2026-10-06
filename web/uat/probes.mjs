// The in-page half of UAT Suite 1 (uat/run.mjs): functions handed to page.evaluate, so they run in the page and
// only see the page (linted with browser globals only). Each takes `feed` (FEED in run.mjs) as its argument.

/**
 * Installed in every page next to axe (run.mjs serialises it into the init script), so the probes share one
 * definition: is `el` the feed's article HTML, rather than something Kipple put inside it (`own`)?
 */
export function installPageHelpers() {
  window.__uatInBody = (el, body, own) => {
    const b = el && el.closest(body);
    if (!b) return false;
    const k = el.closest(own);
    return !(k && b.contains(k));
  };
}

export function hasAxe() {
  return typeof window.axe !== "undefined";
}

/** The scheme the page shows (theme.ts sets it on the root element). */
export function activeScheme() {
  return document.documentElement.dataset.theme;
}

/** No busy skeleton and no boot splash ("Loading Kipple", App.tsx), without reading the whole page's text. */
export function screenReady() {
  if (document.querySelector('[aria-busy="true"]')) return false;
  for (const s of document.querySelectorAll('[role="status"]')) if (s.textContent.trim() === "Loading Kipple") return false;
  return true;
}

/**
 * A new history entry for `path` shaped as React Router writes one (a fresh key, the next index), announced with
 * popstate, which the router follows. For screens with no link on screen to click.
 */
export function pushRoute(path) {
  const s = history.state && typeof history.state === "object" ? history.state : {};
  const idx = typeof s.idx === "number" ? s.idx + 1 : 0;
  history.pushState({ usr: null, key: Math.random().toString(36).slice(2, 10), idx }, "", path);
  window.dispatchEvent(new PopStateEvent("popstate", { state: history.state }));
}
// S4. The page must not scroll sideways, and no visible element may run past the right edge of the viewport. Content
// wider than a scroll container that fits on screen is caught through that container: every screen scrolls in an
// `overflow-y-auto` box, whose overflow-x computes to auto as well, so a container that actually scrolls sideways is
// a finding unless it asks for it (a Tailwind overflow-x-auto/-scroll or overflow-auto/-scroll class) or is inside
// the feed's own article HTML (wide code blocks and tables scroll there on purpose). A container pushed wide only by
// the article HTML (the reader pane around a too-wide embed) is marked `feed`: a note, not a failure.
export function overflowProbe(feed) {
  const vw = document.documentElement.clientWidth;
  const out = { scrollWidth: document.documentElement.scrollWidth, vw, offenders: [], scrollers: [], clipped: [] };
  const describe = (el) => {
    const id = el.id ? `#${el.id}` : "";
    const cls = typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
    const name = el.getAttribute("aria-label") || (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 60);
    return `${el.tagName.toLowerCase()}${id}${cls}${name ? ` "${name}"` : ""}`;
  };
  const visible = (el) => el.checkVisibility({ opacityProperty: true, visibilityProperty: true });
  const inBody = (el) => window.__uatInBody(el, feed.body, feed.own);
  const scrolls = (el) => {
    const ox = getComputedStyle(el).overflowX;
    return ox === "auto" || ox === "scroll";
  };
  const deliberate = (el) =>
    /(^|\s)([\w-]+:)*overflow(-x)?-(auto|scroll)(\s|$)/.test(typeof el.className === "string" ? el.className : "") ||
    inBody(el);
  // Clipped by an ancestor that itself ends inside the viewport (overflow hidden, or a scroller: those are checked
  // on their own below), or hidden the screen-reader-only way. Only ancestors from the containing block up clip: a
  // fixed element escapes every one, an absolute one those between it and its offset parent.
  const srOnly = (el) => {
    for (let a = el; a && a !== document.body; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.clip === "rect(0px, 0px, 0px, 0px)" || cs.clipPath === "inset(50%)") return true;
    }
    return false;
  };
  const contained = (el) => {
    if (srOnly(el)) return true;
    const pos = getComputedStyle(el).position;
    if (pos === "fixed") return false;
    let a = pos === "absolute" ? el.offsetParent : el.parentElement;
    for (; a && a !== document.body; a = a.parentElement) {
      if (getComputedStyle(a).overflowX !== "visible" && a.getBoundingClientRect().right <= vw + 1) return true;
    }
    return false;
  };
  // True when everything sticking out past the scroller's right edge is inside the article HTML.
  const onlyFeedWide = (sc) => {
    const edge = sc.getBoundingClientRect().left + sc.clientWidth + 1;
    let any = false;
    for (const d of sc.querySelectorAll("*")) {
      if (d.getBoundingClientRect().right <= edge || !visible(d)) continue;
      if (!inBody(d)) return false;
      any = true;
    }
    return any;
  };
  // Cut off: partly hidden past the right edge of the nearest ancestor (from the containing block up) that hides
  // overflow without scrolling. Entirely outside it is hidden on purpose (swipe actions, off-canvas parts); an
  // ancestor that truncates with an ellipsis does it on purpose too.
  const clippedBy = (el, r) => {
    const pos = getComputedStyle(el).position;
    if (pos === "fixed") return null;
    for (let a = pos === "absolute" ? el.offsetParent : el.parentElement; a && a !== document.body; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowX === "visible") continue;
      // The nearest box that does anything with overflow decides: a scroller shows the rest on scroll (and is checked
      // as a scroller above), an ellipsis truncates on purpose.
      if (cs.overflowX === "auto" || cs.overflowX === "scroll" || cs.textOverflow === "ellipsis") return null;
      const ar = a.getBoundingClientRect();
      return r.right > ar.right + 1 && r.left < ar.right - 1 ? { a, ar } : null;
    }
    return null;
  };
  for (const el of document.body.querySelectorAll("*")) {
    if (scrolls(el) && el.scrollWidth > el.clientWidth + 1 && !deliberate(el) && visible(el)) {
      if (out.scrollers.length < 10)
        out.scrollers.push({ desc: describe(el), scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, feed: onlyFeedWide(el) });
    }
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0 && out.clipped.length < 10 && !out.clipped.some((c) => c.el.contains(el))) {
      const c = clippedBy(el, r);
      if (c && c.ar.right <= vw + 1 && visible(el) && !inBody(el) && !srOnly(el) &&
        ((el.textContent || "").trim() !== "" || el.matches("button, a[href], input, select, textarea, [role=button], img, svg")))
        out.clipped.push({ el, desc: describe(el), right: Math.round(r.right), edge: Math.round(c.ar.right) });
    }
    if (r.width === 0 || r.height === 0 || r.right <= vw + 1) continue;
    if (r.left >= vw) continue; // entirely off screen: a closed drawer or an off-canvas panel
    if (!visible(el) || contained(el)) continue;
    // Report the outermost offender only.
    if (out.offenders.some((o) => o.el.contains(el))) continue;
    if (out.offenders.length < 10) out.offenders.push({ el, desc: describe(el), right: Math.round(r.right) });
  }
  out.offenders = out.offenders.map(({ desc, right }) => ({ desc, right }));
  out.clipped = out.clipped.map(({ desc, right, edge }) => ({ desc, right, edge }));
  return out;
}

// S5. Visible text nodes, form field values (including a select's chosen option) and accessible-name attributes:
// "undefined", "NaN" (also with a unit stuck to it, as in "NaNm" or "NaNkB"), "[object Object]" and "Invalid Date".
// Feeds can legitimately say "undefined behaviour" or "NaN-boxing", so a hit is the feed's (a note) when it is in the
// article HTML, or when it goes away once the feed-supplied strings in `feed.names` are taken out of the text, or of
// a nearby ancestor's text (only for a text node: a highlighted search term splits a title into several). A field
// that rendered as undefined next to them is still Kipple's.
export function literalProbe(feed) {
  const bad = new RegExp(feed.pattern);
  const norm = (t) => t.replace(/\s+/g, " ");
  // Longest first, so a short name inside a longer string ("NaN Tech" in "NaN Tech Weekly: undefined results") does
  // not break the longer one up before it is taken out.
  const names = [...feed.names].sort((a, b) => b.length - a.length);
  const strip = (t) => names.reduce((s, n) => s.split(n).join(" "), norm(t));
  const inBody = (el) => window.__uatInBody(el, feed.body, feed.ownText);
  const fromFeed = (el, text, isTextNode) => {
    if (inBody(el)) return true;
    if (!feed.names.length) return false;
    if (!bad.test(strip(text))) return true;
    if (!isTextNode) return false;
    for (let a = el, i = 0; a && a !== document.body && i < 4; a = a.parentElement, i++) {
      const t = norm(a.textContent || "");
      if (feed.names.some((n) => t.includes(n)) && !bad.test(strip(t))) return true;
    }
    return false;
  };
  const hits = [];
  const add = (el, where, text, isTextNode = false) =>
    hits.push({ where, text: norm(text).trim().slice(0, 160), feed: fromFeed(el, text, isTextNode) });
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (!el || !n.nodeValue || !bad.test(n.nodeValue) || el.closest("script, style, noscript, template, select")) continue;
    // An SVG <title> (a chart's tooltip) never has a box of its own: it is shown when its graphic is.
    const box = el instanceof SVGTitleElement ? el.parentElement : el;
    if (!box || !box.checkVisibility({ visibilityProperty: true })) continue;
    add(el, el instanceof SVGTitleElement ? "svg title" : el.tagName.toLowerCase(), n.nodeValue, true);
  }
  for (const el of document.body.querySelectorAll("[aria-label],[title],[alt],[placeholder],[aria-valuetext]")) {
    if (!el.checkVisibility({ visibilityProperty: true })) continue;
    for (const a of ["aria-label", "title", "alt", "placeholder", "aria-valuetext"]) {
      const v = el.getAttribute(a);
      if (v && bad.test(v)) add(el, `${el.tagName.toLowerCase()}[${a}]`, v);
    }
  }
  for (const el of document.body.querySelectorAll("input, textarea")) {
    if (el.type === "hidden" || el.type === "password" || !el.checkVisibility({ visibilityProperty: true })) continue;
    if (el.value && bad.test(el.value)) add(el, `${el.tagName.toLowerCase()}.value`, el.value);
  }
  for (const el of document.body.querySelectorAll("select")) {
    if (!el.checkVisibility({ visibilityProperty: true })) continue;
    const label = el.selectedOptions[0]?.textContent ?? "";
    if (bad.test(label)) add(el, "select", label);
  }
  if (bad.test(document.title)) hits.push({ where: "title", text: document.title, feed: false });
  return hits.slice(0, 30);
}

// Whether focus is inside an element matching sel (an open menu), and whether el has focus.
export const focusInMenu = (sel) => !!document.activeElement?.closest(sel);
export const isFocused = (el) => el === document.activeElement;

// S3. axe-core's WCAG 2.0/2.1/2.2 A and AA rules over the document (or, with include, over the elements that selector
// matches), each failing element marked as the feed's own markup or Kipple's.
export async function axeProbe({ feed, include }) {
  // A target is a list of selectors, one per frame or shadow root on the way (a shadow step is itself a list):
  // the first one names the element in this document, which says whose content it is.
  const top = (t) => (Array.isArray(t[0]) ? t[0][0] : t[0]);
  const flat = (t) => t.map((s) => (Array.isArray(s) ? s.join(" >>> ") : s)).join(" | ");
  const r = await window.axe.run(include ? { include: [[include]] } : document, {
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22a", "wcag22aa"] },
    resultTypes: ["violations"],
  });
  return r.violations.map((v) => ({
    id: v.id,
    impact: v.impact,
    help: v.help,
    helpUrl: v.helpUrl,
    nodes: v.nodes.map((n) => ({
      target: flat(n.target),
      html: n.html.slice(0, 300),
      summary: n.failureSummary?.split("\n").slice(0, 3).join(" ").slice(0, 300),
      // Nodes inside the article body are the feed's own markup, reported apart from Kipple's, except what Kipple
      // added there itself. Inside a frame in the article (a started embed: Kipple's frame, the provider's player)
      // the markup is the provider's.
      feedContent: ((el) =>
        n.target.length > 1 && !Array.isArray(n.target[0])
          ? !!el?.closest(feed.body)
          : window.__uatInBody(el, feed.body, feed.own))(document.querySelector(top(n.target))),
    })),
  }));
}