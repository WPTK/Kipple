// Measures every surface of the app for the design review: the objective checks of phase 1 in
// docs/maintainers/design-review.md (touch targets, contrast in every theme, reflow, text spacing and size, forced colors,
// color-vision deficiencies, layout shift and skeletons on a slow device, keyboard focus, the accessibility tree, right-to-
// left and long strings, and an inventory of the rendered interface). Writes findings.json, report.md with a cropped
// capture of each hit, and inventory sheets to --out. It runs on demand against a seeded instance, never in CI.
//
//   KIPPLE_SEED_SET=design npm run seed            (in one terminal)
//   node ../scripts/maintainers/design-probes.mjs --out ../.design/probes [--checks targets,contrast,...] [--only <regexp>]
//        [--devices phone,tablet,desktop] [--themes paper,midnight] [--contrast-themes all] [--repeat 20]
//        [--url http://127.0.0.1:1919]
// Run from web/. The surfaces, devices and sign-in are the ones design-shots.mjs uses (lib/design-surfaces.mjs). A check
// that cannot reach a surface lists it under `missed`; the exit code is 1 when any was missed. Findings never fail the
// run: they are what the review reads.
/* global document, window, getComputedStyle, innerWidth, innerHeight, Image, NodeFilter, PerformanceObserver, MutationObserver, performance, CSS, location */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { COMMON_OPTIONS, DEVICES, chromium, resolveCommon, schemes, session, settle, webRequire } from "./lib/design-surfaces.mjs";

const CHECKS = ["targets", "contrast", "reflow", "spacing", "forced", "vision", "throttle", "stuck", "keyboard", "aria", "stress", "inventory"];
const { values: opt } = parseArgs({
  options: {
    ...COMMON_OPTIONS,
    checks: { type: "string", default: CHECKS.join(",") },
    "contrast-themes": { type: "string", default: "all" },
    repeat: { type: "string", default: "20" },
  },
});
const { origin, themes, devices, surfaces } = resolveCommon(
  opt,
  "usage: node ../scripts/maintainers/design-probes.mjs --out <dir> [--checks a,b] [--only <regexp>] [--devices ...] [--themes ...] [--contrast-themes all|a,b] [--repeat n]",
);
const checks = opt.checks.split(",").map((s) => s.trim());
for (const c of checks) if (!CHECKS.includes(c)) throw new Error(`unknown check ${c} (have ${CHECKS.join(", ")})`);
const contrastThemes = opt["contrast-themes"] === "all" ? schemes.map((s) => s.id) : opt["contrast-themes"].split(",").map((s) => s.trim());
const AXE = readFileSync(webRequire.resolve("axe-core/axe.min.js"), "utf8");

const out = resolve(opt.out);
for (const d of ["crops", "aria", "frames", "inventory"]) mkdirSync(join(out, d), { recursive: true });
const findings = [];
const missed = [];
const notes = [];
const inventory = [];
const browser = await chromium.launch();
const { context, setProfile, library, removeDevices } = session(browser, { origin, user: opt.user, password: opt.password });
let lib;
let cropN = 0;

// ---- In-page helpers --------------------------------------------------------------------------------------------------
// Installed once per document as window.__dp. Colors go through a 1x1 canvas, so any CSS color (oklch, color-mix) comes
// back as sRGB; contrast is the WCAG 2.x ratio; a background is the stack of ancestor backgrounds blended down.
function installLib() {
  if (window.__dp) return;
  const cv = document.createElement("canvas");
  cv.width = cv.height = 1;
  const cx = cv.getContext("2d", { willReadFrequently: true });
  const cache = new Map();
  const rgba = (c) => {
    if (cache.has(c)) return cache.get(c);
    cx.clearRect(0, 0, 1, 1);
    cx.fillStyle = "rgba(0,0,0,0)";
    cx.fillStyle = c;
    cx.fillRect(0, 0, 1, 1);
    const d = cx.getImageData(0, 0, 1, 1).data;
    const v = [d[0], d[1], d[2], d[3] / 255];
    cache.set(c, v);
    return v;
  };
  const blend = (t, b) => [t[0] * t[3] + b[0] * (1 - t[3]), t[1] * t[3] + b[1] * (1 - t[3]), t[2] * t[3] + b[2] * (1 - t[3]), 1];
  const lum = ([r, g, b]) => {
    const f = (v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  };
  const ratio = (a, b) => {
    const x = lum(a);
    const y = lum(b);
    return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
  };
  const opacity = (el) => {
    let o = 1;
    for (let e = el; e && e.nodeType === 1; e = e.parentElement) o *= +getComputedStyle(e).opacity;
    return o;
  };
  const bgOf = (el) => {
    const layers = [];
    for (let e = el; e && e.nodeType === 1; e = e.parentElement) {
      const c = rgba(getComputedStyle(e).backgroundColor);
      if (c[3] > 0) {
        layers.push(c);
        if (c[3] >= 1) break;
      }
    }
    let base = [255, 255, 255, 1];
    for (let i = layers.length - 1; i >= 0; i--) base = blend(layers[i], base);
    return base;
  };
  const shown = (el) => {
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    const cs = getComputedStyle(el);
    if (cs.visibility !== "visible" || cs.display === "none") return false;
    return opacity(el) > 0.05;
  };
  const inView = (r) => r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
  // The box an element is drawn in: the window cut by every ancestor that clips (a scrolled pane hides what is outside it).
  const clipBox = (el, self) => {
    let l = 0;
    let t = 0;
    let r = innerWidth;
    let b = innerHeight;
    for (let e = self ? el : el.parentElement; e && e !== document.body && e !== document.documentElement; e = e.parentElement) {
      const cs = getComputedStyle(e);
      if (cs.overflowX !== "visible" || cs.overflowY !== "visible") {
        const q = e.getBoundingClientRect();
        l = Math.max(l, q.left);
        t = Math.max(t, q.top);
        r = Math.min(r, q.right);
        b = Math.min(b, q.bottom);
      }
      if (cs.position === "fixed") break;
    }
    return { left: l, top: t, right: r, bottom: b };
  };
  const cut = (q, c) => {
    const left = Math.max(q.left, c.left);
    const top = Math.max(q.top, c.top);
    const right = Math.min(q.right, c.right);
    const bottom = Math.min(q.bottom, c.bottom);
    return { left, top, right, bottom, width: right - left, height: bottom - top };
  };
  const seenAt = (el, self) => {
    const q = cut(el.getBoundingClientRect(), clipBox(el, self));
    return q.width > 1 && q.height > 1;
  };
  // A visually hidden input stands for its label, which is what is drawn and tapped.
  const drawn = (el) => {
    const r = el.getBoundingClientRect();
    return r.width < 4 || r.height < 4 ? el.labels?.[0] || el.closest("label") || el.parentElement || el : el;
  };
  let n = 0;
  const id = (el) => el.dataset.dpId || (el.dataset.dpId = String(++n));
  const text = (s) => (s || "").replace(/\s+/g, " ").trim();
  const name = (el) => {
    const by = el.getAttribute("aria-labelledby");
    const lab = by ? by.split(/\s+/).map((x) => document.getElementById(x)?.textContent || "").join(" ") : "";
    return text(el.getAttribute("aria-label") || lab || el.labels?.[0]?.innerText || el.innerText || el.getAttribute("title") || el.getAttribute("alt") || el.getAttribute("placeholder") || "").slice(0, 90);
  };
  const LANDMARK = '[role=dialog],[role=menu],[role=toolbar],[role=tablist],nav,main,header,footer,aside,form,[role=navigation],[role=main],[role=banner],[role=region],[role=tree]';
  const landmark = (el) => {
    const l = el.parentElement?.closest(LANDMARK);
    return l ? `${l.getAttribute("role") || l.tagName.toLowerCase()}${l.getAttribute("aria-label") ? ` "${text(l.getAttribute("aria-label")).slice(0, 30)}"` : ""}` : "page";
  };
  const role = (el) => el.getAttribute("role") || { A: "link", BUTTON: "button", INPUT: "input", SELECT: "select", TEXTAREA: "textbox", SUMMARY: "summary" }[el.tagName] || el.tagName.toLowerCase();
  const INTERACTIVE =
    'a[href],button,input:not([type=hidden]),select,textarea,summary,[role=button],[role=link],[role=menuitem],[role=menuitemradio],[role=menuitemcheckbox],[role=tab],[role=checkbox],[role=switch],[role=radio],[role=option],[role=slider],[role=treeitem],[tabindex]:not([tabindex="-1"])';
  const rect = (el) => {
    const r = drawn(el).getBoundingClientRect();
    return { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) };
  };
  const info = (el) => ({ id: id(el), name: name(el), role: role(el), landmark: landmark(el), rect: rect(el) });
  const disabled = (el) => !!el.closest("[disabled],[aria-disabled=true]");
  // A link inside running text: WCAG 2.5.8 exempts it.
  const inline = (el) => {
    if (el.tagName !== "A") return false;
    if (getComputedStyle(el).display !== "inline") return false;
    const p = el.parentElement;
    return p && text(p.textContent).length > text(el.textContent).length + 20;
  };
  const fixedLayer = (el) => {
    for (let e = el; e && e.nodeType === 1; e = e.parentElement) {
      const p = getComputedStyle(e).position;
      if (p === "fixed" || p === "sticky") return e;
    }
    return null;
  };

  /** Interactive targets in the window, with the nearest other target and whether the WCAG 2.5.8 spacing exception holds. */
  function targets() {
    const els = [...document.querySelectorAll(INTERACTIVE)].filter((e) => drawn(e) === e && shown(e) && seenAt(e) && !disabled(e) && !inline(e));
    const rs = els.map((e) => e.getBoundingClientRect());
    return els.map((e, i) => {
      const r = rs[i];
      const cx0 = r.left + r.width / 2;
      const cy0 = r.top + r.height / 2;
      let gap = Infinity;
      let near = null;
      let spacingOk = true;
      for (let j = 0; j < els.length; j++) {
        if (j === i) continue;
        const o = els[j];
        if (e.contains(o) || o.contains(e)) continue;
        const q = rs[j];
        const inside = (a, b) => a.left >= b.left - 1 && a.right <= b.right + 1 && a.top >= b.top - 1 && a.bottom <= b.bottom + 1;
        if (inside(r, q) || inside(q, r)) continue; // a control laid over a whole-row link
        const dx = Math.max(0, q.left - r.right, r.left - q.right);
        const dy = Math.max(0, q.top - r.bottom, r.top - q.bottom);
        const d = Math.hypot(dx, dy);
        if (d < gap) (gap = d), (near = name(o));
        const ddx = Math.max(0, q.left - cx0, cx0 - q.right);
        const ddy = Math.max(0, q.top - cy0, cy0 - q.bottom);
        if (Math.hypot(ddx, ddy) < 12) spacingOk = false;
        const small = q.width < 24 || q.height < 24;
        if (small && Math.hypot(q.left + q.width / 2 - cx0, q.top + q.height / 2 - cy0) < 24) spacingOk = false;
      }
      return { ...info(e), gap: gap === Infinity ? null : Math.round(gap * 10) / 10, near, spacingOk };
    });
  }

  /** Text that does not fit its box: cut by its own overflow (ellipsis, a line clamp or a hard clip) or by an ancestor. */
  function clipping() {
    const hits = [];
    for (const el of document.querySelectorAll("body *")) {
      if (![...el.childNodes].some((c) => c.nodeType === 3 && c.textContent.trim())) continue;
      if (!shown(el)) continue;
      const r = el.getBoundingClientRect();
      if (!seenAt(el)) continue;
      if (el.closest(".sr-only,.sr-only-live")) continue;
      const cs = getComputedStyle(el);
      const overX = el.scrollWidth > el.clientWidth + 1 && /hidden|clip/.test(cs.overflowX);
      const overY = el.scrollHeight > el.clientHeight + 1 && /hidden|clip/.test(cs.overflowY);
      if (overX || overY) {
        const kind = cs.textOverflow === "ellipsis" ? "ellipsis" : cs.webkitLineClamp && cs.webkitLineClamp !== "none" ? "clamp" : "clip";
        hits.push({ ...info(el), kind, over: Math.max(el.scrollWidth - el.clientWidth, el.scrollHeight - el.clientHeight) });
        continue;
      }
      for (let a = el.parentElement, k = 0; a && k < 4; a = a.parentElement, k++) {
        const acs = getComputedStyle(a);
        if (!/hidden|clip/.test(acs.overflowY + acs.overflowX)) continue;
        if (/auto|scroll/.test(acs.overflowY)) break;
        const ar = a.getBoundingClientRect();
        const over = Math.max(r.bottom - ar.bottom, r.right - ar.right, ar.left - r.left, ar.top - r.top);
        const kind = acs.webkitLineClamp && acs.webkitLineClamp !== "none" ? "clamp" : acs.textOverflow === "ellipsis" ? "ellipsis" : "clipped-by-ancestor";
        if (over > 2 && r.height < 400) hits.push({ ...info(el), kind, over: Math.round(over) });
        break;
      }
    }
    return hits;
  }

  /** Lines of text from different elements drawn on top of each other in the same layer. */
  function overlaps() {
    const boxes = [];
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let t = w.nextNode(); t && boxes.length < 1500; t = w.nextNode()) {
      if (!t.textContent.trim()) continue;
      const p = t.parentElement;
      if (!p || !shown(p) || p.closest(".sr-only,.sr-only-live,[aria-hidden=true] svg")) continue;
      const range = document.createRange();
      range.selectNodeContents(t);
      const c = clipBox(p, true);
      for (const q of range.getClientRects()) {
        const r = cut(q, c);
        if (r.width > 2 && r.height > 2) boxes.push({ p, r, layer: fixedLayer(p) });
      }
    }
    const hits = [];
    const seen = new Set();
    for (let i = 0; i < boxes.length; i++)
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i];
        const b = boxes[j];
        if (a.p === b.p || a.p.contains(b.p) || b.p.contains(a.p) || a.layer !== b.layer) continue;
        const iw = Math.min(a.r.right, b.r.right) - Math.max(a.r.left, b.r.left);
        const ih = Math.min(a.r.bottom, b.r.bottom) - Math.max(a.r.top, b.r.top);
        if (iw > 2 && ih > Math.min(a.r.height, b.r.height) * 0.3) {
          const k = `${id(a.p)}-${id(b.p)}`;
          if (seen.has(k)) continue;
          seen.add(k);
          hits.push({ ...info(a.p), other: name(b.p), otherId: id(b.p), overlap: `${Math.round(iw)}x${Math.round(ih)}` });
        }
      }
    return hits.slice(0, 60);
  }

  /** Sideways overflow: how far the page scrolls sideways, and the outermost elements past either edge. */
  function hOverflow() {
    const W = document.documentElement.clientWidth;
    const page = Math.max(document.scrollingElement.scrollWidth, document.body.scrollWidth) - W;
    const off = [];
    for (const el of document.querySelectorAll("body *")) {
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) continue;
      if (!(r.right > W + 1 || r.left < -1)) continue;
      if (el.closest(".sr-only,.sr-only-live")) continue;
      const cs = getComputedStyle(el);
      if (cs.visibility !== "visible" || cs.position === "fixed" && opacity(el) < 0.05) continue;
      let scroller = null;
      let clipped = null;
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        const ox = getComputedStyle(a).overflowX;
        if (/auto|scroll/.test(ox)) {
          scroller = a;
          break;
        }
        if (/hidden|clip/.test(ox) && !clipped) clipped = a;
      }
      if (scroller) continue; // inside its own sideways scroller (a table, code)
      const parent = el.parentElement;
      const pr = parent?.getBoundingClientRect();
      if (pr && (pr.right > W + 1 || pr.left < -1) && parent !== document.body) continue; // only the outermost
      if (opacity(el) < 0.05) continue;
      off.push({ ...info(el), past: Math.round(Math.max(r.right - W, -r.left)), clipped: !!clipped });
    }
    return { page, offenders: off.sort((a, b) => b.past - a.past).slice(0, 15) };
  }

  /**
   * Non-text contrast (WCAG 1.4.11): icons (graphics in icon-only controls are required; next to a text label they are
   * not), unread markers, the boundary of inputs and switches. Selected states are in stateCues.
   */
  function nonText() {
    const hits = [];
    for (const svg of document.querySelectorAll("svg")) {
      if (!shown(svg) || !seenAt(svg)) continue;
      const ctl = svg.closest(INTERACTIVE);
      if (!ctl || disabled(ctl)) continue;
      const iconOnly = !text(ctl.innerText);
      const cs = getComputedStyle(svg);
      const paint = cs.stroke && cs.stroke !== "none" ? cs.stroke : cs.fill;
      let c = rgba(paint);
      if (c[3] === 0) continue;
      const bg = bgOf(svg);
      c = blend([c[0], c[1], c[2], c[3] * opacity(svg)], bg);
      const r = ratio(c, bg);
      if (r < 3) hits.push({ ...info(ctl), what: iconOnly ? "icon in an icon-only control" : "icon beside a text label", ratio: +r.toFixed(2), required: iconOnly });
    }
    for (const dot of document.querySelectorAll('.kp-unread-dot[data-unread="true"]')) {
      if (!shown(dot) || !seenAt(dot)) continue;
      const c = blend(rgba(getComputedStyle(dot).backgroundColor), bgOf(dot.parentElement));
      const r = ratio(c, bgOf(dot.parentElement));
      if (r < 3) hits.push({ ...info(dot), name: "unread dot", what: "unread marker", ratio: +r.toFixed(2), required: true });
    }
    const fields = 'input:not([type=hidden]):not([type=checkbox]):not([type=radio]):not([type=range]),select,textarea,[role=switch],[role=checkbox]:not(input),[role=radio]:not(input),input[type=checkbox],input[type=radio]';
    for (const f of document.querySelectorAll(fields)) {
      if (!shown(f) || !seenAt(f) || disabled(f)) continue;
      const cs = getComputedStyle(f);
      if ((f.type === "checkbox" || f.type === "radio") && cs.appearance !== "none") continue; // drawn by the browser
      const outer = bgOf(f.parentElement);
      const own = ratio(bgOf(f), outer);
      const bw = parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth);
      const border = bw > 0 ? ratio(blend(rgba(cs.borderTopColor), outer), outer) : 1;
      const best = Math.max(own, border);
      if (best < 3) hits.push({ ...info(f), what: "control boundary", ratio: +best.toFixed(2), required: true, border: +border.toFixed(2), fill: +own.toFixed(2) });
    }
    return hits;
  }

  /** Each selected item next to an unselected peer: which cues tell them apart, and how far apart their colors are. */
  function stateCues() {
    const SEL = '[aria-current]:not([aria-current=false]),[aria-selected=true],[aria-pressed=true],[aria-checked=true],[data-state=on],[data-state=checked],[data-state=active],label:has(input:checked)';
    const res = [];
    for (const s of document.querySelectorAll(SEL)) {
      if (!shown(s) || !seenAt(s)) continue;
      const group = s.parentElement?.parentElement || s.parentElement;
      const peer = [...(group?.querySelectorAll(s.tagName) || [])].find((p) => p !== s && !p.matches(SEL) && !p.contains(s) && !s.contains(p) && (p.getAttribute("role") || "") === (s.getAttribute("role") || "") && shown(p));
      if (!peer) continue;
      const a = getComputedStyle(s);
      const b = getComputedStyle(peer);
      const cues = [];
      if (a.fontWeight !== b.fontWeight) cues.push("weight");
      if (a.textDecorationLine !== b.textDecorationLine) cues.push("underline");
      for (const side of ["Top", "Right", "Bottom", "Left"]) if (a[`border${side}Width`] !== b[`border${side}Width`]) cues.push(`border-${side.toLowerCase()}`);
      if (a.outlineStyle !== b.outlineStyle || a.outlineWidth !== b.outlineWidth) cues.push("outline");
      if (s.querySelectorAll("svg").length !== peer.querySelectorAll("svg").length) cues.push("icon");
      for (const pe of ["::before", "::after"]) {
        const x = getComputedStyle(s, pe);
        const y = getComputedStyle(peer, pe);
        if (x.content !== y.content || x.width !== y.width || x.backgroundColor !== y.backgroundColor) cues.push(`marker${pe}`);
      }
      const bgS = bgOf(s);
      const bgP = bgOf(peer);
      const outer = bgOf(group);
      res.push({
        ...info(s), peer: name(peer), cues: [...new Set(cues.map((c) => c.replace(/-(top|right|bottom|left)$/, "")))],
        text: [rgba(a.color), rgba(b.color)].map((c) => c.slice(0, 3).map(Math.round)),
        bg: [bgS, bgP].map((c) => c.slice(0, 3).map(Math.round)),
        textRatio: +ratio(rgba(a.color), rgba(b.color)).toFixed(2), bgRatio: +ratio(bgS, bgP).toFixed(2), bgVsOuter: +ratio(bgS, outer).toFixed(2),
      });
    }
    return res;
  }

  /** Unread against read rows: what tells them apart. */
  function readCues() {
    const rows = [...document.querySelectorAll("main article[data-item-id]")].filter((r) => shown(r) && inView(r.getBoundingClientRect()));
    const unread = rows.find((r) => r.querySelector('.kp-unread-dot[data-unread="true"]'));
    const read = rows.find((r) => !r.querySelector('.kp-unread-dot[data-unread="true"]'));
    if (!unread || !read) return null;
    const title = (r) => r.querySelector("h2,h3,[data-title]") || r.querySelector('a[href^="/i/"]');
    const a = getComputedStyle(title(unread));
    const b = getComputedStyle(title(read));
    const cues = ["dot"];
    if (a.fontWeight !== b.fontWeight) cues.push("weight");
    if (a.fontStyle !== b.fontStyle) cues.push("style");
    return { unread: info(unread), read: info(read), cues, textRatio: +ratio(rgba(a.color), rgba(b.color)).toFixed(2), readTextVsBg: +ratio(rgba(b.color), bgOf(title(read))).toFixed(2), opacityRead: +opacity(title(read)).toFixed(2) };
  }

  /** Text drawn in the danger color with no icon or word that says something failed. */
  function dangerOnly(danger) {
    const d = rgba(danger);
    const hits = [];
    for (const el of document.querySelectorAll("body *")) {
      if (![...el.childNodes].some((c) => c.nodeType === 3 && c.textContent.trim())) continue;
      if (!shown(el) || !inView(el.getBoundingClientRect())) continue;
      const c = rgba(getComputedStyle(el).color);
      if (Math.abs(c[0] - d[0]) + Math.abs(c[1] - d[1]) + Math.abs(c[2] - d[2]) > 12) continue;
      const box = el.closest("[role=alert],[role=status],li,p,div") || el;
      const hasIcon = !!box.querySelector("svg") || !!box.parentElement?.querySelector(":scope > svg");
      const worded = /error|fail|couldn|can.?t|cannot|not |invalid|unable|problem|wrong|delete|remove|stopp|broken|denied|refus/i.test(box.textContent);
      if (!hasIcon && !worded) hits.push({ ...info(el), text: text(el.textContent).slice(0, 80) });
    }
    return hits;
  }

  /** The focused element: where it is, whether it is drawn, and whether a fixed or sticky layer covers it (WCAG 2.4.11). */
  function focusInfo() {
    const a = document.activeElement;
    if (!a || a === document.body || a === document.documentElement) return null;
    const r = drawn(a).getBoundingClientRect();
    const pts = [[0.5, 0.5], [0.15, 0.2], [0.85, 0.2], [0.15, 0.8], [0.85, 0.8]].map(([fx, fy]) => [r.left + r.width * fx, r.top + r.height * fy]);
    let covered = 0;
    let by = null;
    let onScreen = 0;
    for (const [x, y] of pts) {
      if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) continue;
      onScreen++;
      const hit = document.elementFromPoint(x, y);
      const d = drawn(a);
      if (!hit || hit === a || a.contains(hit) || hit.contains(a) || d.contains(hit)) continue;
      const layer = fixedLayer(hit);
      if (layer && !layer.contains(a)) {
        covered++;
        by = name(layer).slice(0, 40) || layer.tagName.toLowerCase();
      }
    }
    const ring = a.closest("article[data-item-id],[role=treeitem],[role=row],li") || drawn(a);
    const q = ring.getBoundingClientRect();
    const box = q.width <= 900 && q.height <= 500 ? { x: Math.round(q.left), y: Math.round(q.top), w: Math.round(q.width), h: Math.round(q.height) } : rect(a);
    return { ...info(a), box, opacity: +opacity(drawn(a)).toFixed(2), onScreen, covered, by, landmarkEl: landmark(a) };
  }

  /** The heading outline and the landmarks, in document order with where they are drawn. */
  function outline() {
    return [...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")]
      .filter(shown)
      .map((h) => ({ ...info(h), level: +(h.getAttribute("aria-level") || h.tagName.slice(1)) || 0 }));
  }

  /** Rendered components and style values, for the interface inventory. */
  function inventoryScan() {
    const S = (el) => {
      const cs = getComputedStyle(el);
      return {
        h: Math.round(el.getBoundingClientRect().height), font: `${cs.fontSize} ${cs.fontWeight}`, family: cs.fontFamily.split(",")[0].replace(/"/g, ""),
        pad: `${cs.paddingTop} ${cs.paddingRight} ${cs.paddingBottom} ${cs.paddingLeft}`, radius: cs.borderTopLeftRadius,
        bg: cs.backgroundColor, color: cs.color, border: cs.borderTopWidth === "0px" ? "none" : `${cs.borderTopWidth} ${cs.borderTopColor}`, shadow: cs.boxShadow === "none" ? "none" : cs.boxShadow,
      };
    };
    const kinds = {
      button: "button,[role=button],a[class*=rounded][class*=px-]",
      input: "input:not([type=hidden]),select,textarea,[role=switch],[role=checkbox]:not(input),[role=radio]:not(input)",
      menuitem: "[role=menuitem],[role=menuitemradio],[role=menuitemcheckbox]",
      badge: "span,div",
      icon: "svg",
    };
    const items = [];
    for (const [kind, sel] of Object.entries(kinds))
      for (const el of document.querySelectorAll(sel)) {
        if (!shown(el)) continue;
        const r = el.getBoundingClientRect();
        if (!inView(r)) continue;
        if (kind === "badge") {
          const t = text(el.textContent);
          const cs = getComputedStyle(el);
          if (!t || t.length > 14 || el.children.length > 1 || r.height > 30 || r.width > 160) continue;
          const filled = rgba(cs.backgroundColor)[3] > 0 || cs.borderTopWidth !== "0px";
          if (!filled || parseFloat(cs.borderTopLeftRadius) < 4) continue;
        }
        if (kind === "icon") {
          const cs = getComputedStyle(el);
          items.push({ kind, id: id(el), rect: rect(el), style: { size: `${Math.round(r.width)}x${Math.round(r.height)}`, stroke: el.getAttribute("stroke-width") || cs.strokeWidth, color: cs.stroke !== "none" ? cs.stroke : cs.color }, name: name(el.closest(INTERACTIVE) || el) });
          continue;
        }
        items.push({ kind, id: id(el), rect: rect(el), style: S(el), name: name(el).slice(0, 40) });
      }
    const values = { fontSize: {}, spacing: {}, radius: {}, color: {}, bg: {}, border: {}, shadow: {}, gap: {} };
    const inc = (m, k) => k && (m[k] = (m[k] || 0) + 1);
    for (const el of document.querySelectorAll("body *")) {
      if (!shown(el) || !inView(el.getBoundingClientRect())) continue;
      const cs = getComputedStyle(el);
      if ([...el.childNodes].some((c) => c.nodeType === 3 && c.textContent.trim())) (inc(values.fontSize, cs.fontSize), inc(values.color, cs.color));
      for (const p of ["paddingTop", "paddingRight", "paddingBottom", "paddingLeft", "marginTop", "marginBottom"]) if (cs[p] !== "0px" && !cs[p].startsWith("-")) inc(values.spacing, cs[p]);
      if (cs.display.includes("flex") || cs.display.includes("grid")) if (cs.rowGap !== "normal" && cs.rowGap !== "0px") inc(values.gap, cs.rowGap);
      if (cs.borderTopLeftRadius !== "0px") inc(values.radius, cs.borderTopLeftRadius);
      if (rgba(cs.backgroundColor)[3] > 0) inc(values.bg, cs.backgroundColor);
      if (cs.borderTopWidth !== "0px" && cs.borderTopStyle !== "none") inc(values.border, `${cs.borderTopWidth} ${cs.borderTopColor}`);
      if (cs.boxShadow !== "none") inc(values.shadow, cs.boxShadow);
    }
    return { items, values };
  }

  window.__dp = { targets, clipping, overlaps, hOverflow, nonText, stateCues, readCues, dangerOnly, focusInfo, outline, inventoryScan, rgba };
}

/** Evaluates `fn` (a name in window.__dp) in the page, installing the helpers first. */
async function dp(page, fn, arg) {
  await page.evaluate(installLib);
  return page.evaluate(([f, a]) => window.__dp[f](a), [fn, arg]);
}

// ---- Pixels -----------------------------------------------------------------------------------------------------------
// A blank page that decodes PNGs on a canvas: focus rings are found by comparing a control focused and not focused.
const lab = await browser.newPage();
async function pixelDiff(a, b) {
  return lab.evaluate(
    async ([x, y]) => {
      const load = async (s) => {
        const img = new Image();
        img.src = `data:image/png;base64,${s}`;
        await img.decode();
        const c = document.createElement("canvas");
        c.width = img.width;
        c.height = img.height;
        const g = c.getContext("2d");
        g.drawImage(img, 0, 0);
        return g.getImageData(0, 0, c.width, c.height);
      };
      const [p, q] = await Promise.all([load(x), load(y)]);
      if (p.width !== q.width || p.height !== q.height) return { changed: -1 };
      const lum = (r, g, b) => [r, g, b].map((v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
      let changed = 0;
      const ratios = [];
      for (let i = 0; i < p.data.length; i += 4) {
        const d = Math.abs(p.data[i] - q.data[i]) + Math.abs(p.data[i + 1] - q.data[i + 1]) + Math.abs(p.data[i + 2] - q.data[i + 2]);
        if (d < 40) continue;
        changed++;
        const l1 = lum(p.data[i], p.data[i + 1], p.data[i + 2]);
        const l2 = lum(q.data[i], q.data[i + 1], q.data[i + 2]);
        ratios.push((Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05));
      }
      ratios.sort((m, n) => m - n);
      return { changed, total: p.width * p.height, median: ratios.length ? +ratios[ratios.length >> 1].toFixed(2) : 0, max: ratios.length ? +ratios.at(-1).toFixed(2) : 0 };
    },
    [a.toString("base64"), b.toString("base64")],
  );
}

/** A clip around `r` (a rect from the page), padded and kept inside the window. */
function clipOf(page, r, pad = 4) {
  const vp = page.viewportSize();
  const x = Math.max(0, r.x - pad);
  const y = Math.max(0, r.y - pad);
  const w = Math.min(vp.width - x, r.w + pad * 2);
  const h = Math.min(vp.height - y, r.h + pad * 2);
  return w > 2 && h > 2 ? { x, y, width: w, height: h } : null;
}

/** A capture of the element marked `id` with a magenta outline around it and some context; returns its file name. */
async function crop(page, id, label) {
  try {
    const r = await page.evaluate((i) => {
      const el0 = document.querySelector(`[data-dp-id="${i}"]`);
      if (!el0) return null;
      const r0 = el0.getBoundingClientRect();
      const el = r0.width < 4 || r0.height < 4 ? el0.labels?.[0] || el0.closest("label") || el0.parentElement || el0 : el0;
      let b = el.getBoundingClientRect();
      if (b.bottom < 0 || b.top > innerHeight) {
        el.scrollIntoView({ block: "center" });
        b = el.getBoundingClientRect();
      }
      const o = document.createElement("div");
      o.id = "__dp-mark";
      Object.assign(o.style, { position: "fixed", left: `${b.left - 2}px`, top: `${b.top - 2}px`, width: `${b.width + 4}px`, height: `${b.height + 4}px`, outline: "2px solid #ff00ff", zIndex: 2147483647, pointerEvents: "none" });
      document.body.append(o);
      return { x: Math.round(b.left), y: Math.round(b.top), w: Math.round(b.width), h: Math.round(b.height) };
    }, id);
    if (!r) return null;
    const clip = clipOf(page, r, 40);
    const file = `crops/${String(++cropN).padStart(4, "0")}-${label.replace(/[^a-z0-9]+/gi, "-").slice(0, 50)}.png`;
    if (clip) await page.screenshot({ path: join(out, file), clip });
    await page.evaluate(() => document.getElementById("__dp-mark")?.remove());
    return clip ? file : null;
  } catch {
    return null;
  }
}

// ---- Recording --------------------------------------------------------------------------------------------------------
// One finding per instance; report.md groups them by `key`, the root cause (same check, rule and control name).
const CROPS_PER_KEY = 3;
const cropCount = new Map();
const stripName = (n) => (n || "").replace(/[:,] .*$/, "").replace(/\d+/g, "#").slice(0, 50) || "(no name)";
async function hit(page, f) {
  const key = f.key ?? `${f.check}|${f.rule}|${f.role ?? ""}|${stripName(f.name)}`;
  const n = cropCount.get(key) ?? 0;
  let file = null;
  if (page && f.id && n < CROPS_PER_KEY) {
    cropCount.set(key, n + 1);
    file = await crop(page, f.id, `${f.check}-${f.surface}-${f.device}`);
  }
  const { id: _id, ...rest } = f;
  findings.push({ ...rest, key, crop: file });
}

/** Opens `s` in a new page of `ctx`; returns the page, or null after recording a miss. */
async function open(ctx, s, where, { keepFocus = s.area === "menus" } = {}) {
  const page = await ctx.newPage();
  try {
    await s.go(page, lib);
    await settle(page, keepFocus);
    return page;
  } catch (e) {
    missed.push({ ...where, surface: s.id, why: String(e.message).split("\n")[0] });
    console.error(`MISS ${where.check} ${s.id} ${where.device}: ${String(e.message).split("\n")[0]}`);
    await page.close();
    return null;
  }
}

/** Runs `fn(page, s)` over the surfaces in a signed-in context for `device`, with the device profile set first. */
async function overSurfaces(check, device, { theme = themes[0], size = 1, contextOptions = {}, filter = () => true, signedOut = true }, fn) {
  const list = surfaces.filter((s) => filter(s) && (signedOut || !s.signedOut));
  const ctx = await context(device, contextOptions);
  try {
    await setProfile(ctx, { "ui.theme": theme, "client.text_size": size, "client.peek_seen": true });
    lib ??= await library(ctx);
    for (const s of list.filter((x) => !x.signedOut)) {
      await setProfile(ctx, { "client.layout": s.layout ?? "magazine" });
      const page = await open(ctx, s, { check, device });
      if (!page) continue;
      try {
        await fn(page, s);
      } catch (e) {
        missed.push({ check, device, surface: s.id, why: `probe: ${String(e.message).split("\n")[0]}` });
      } finally {
        await page.close();
      }
    }
  } finally {
    await ctx.close();
  }
  for (const s of list.filter((x) => x.signedOut)) {
    const ctx2 = await context(device, { ...contextOptions, signedOutScheme: schemes.find((x) => x.id === theme).kind });
    const page = await open(ctx2, s, { check, device });
    if (page) await fn(page, s).catch((e) => missed.push({ check, device, surface: s.id, why: `probe: ${e.message}` }));
    await ctx2.close();
  }
}

const touch = (device) => DEVICES[device].mobile;
const log = (s) => console.log(`${new Date().toISOString().slice(11, 19)} ${s}`);

// ---- Checks -----------------------------------------------------------------------------------------------------------
const RUN = {
  /** WCAG 2.5.8 (24 px or spacing), the 44 pt (HIG) and 48 dp (Material) touch sizes on phone and tablet, and neighbours under 8 px apart. */
  async targets() {
    for (const device of devices)
      await overSurfaces("targets", device, {}, async (page, s) => {
        const min = touch(device) ? 44 : 24;
        for (const t of await dp(page, "targets")) {
          const base = { check: "targets", surface: s.id, device, theme: themes[0], ...t };
          const small = Math.min(t.rect.w, t.rect.h);
          if (small < 24 && !t.spacingOk) await hit(page, { ...base, rule: "WCAG 2.5.8 target under 24 px without spacing", value: `${t.rect.w}x${t.rect.h}`, threshold: 24 });
          else if (small < min) await hit(page, { ...base, rule: `touch target under ${min} px (HIG 44 pt, Material 48 dp)`, value: `${t.rect.w}x${t.rect.h}`, threshold: min });
          if (t.gap !== null && t.gap < 8 && small < 44) await hit(page, { ...base, rule: "neighbouring targets under 8 px apart", value: `${t.gap} px to "${t.near}"`, threshold: 8, key: `targets|gap|${t.role}|${stripName(t.name)}` });
        }
      });
  },

  /** Text contrast (axe color-contrast, link-in-text-block) and non-text contrast, in every theme, switched live in one page. */
  async contrast() {
    for (const device of devices.filter((d) => d !== "tablet"))
      await overSurfaces("contrast", device, {}, async (page, s) => {
        await page.addStyleTag({ content: "*,*::before,*::after{transition:none!important;animation:none!important}" });
        for (const theme of contrastThemes) {
          await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
          await page.waitForTimeout(120);
          if (!(await page.evaluate(() => typeof window.axe === "object"))) await page.evaluate(AXE);
          const v = await page.evaluate(() =>
            window.axe.run(document, { runOnly: { type: "rule", values: ["color-contrast", "link-in-text-block"] } }).then((r) =>
              r.violations.flatMap((x) => x.nodes.map((n) => ({ rule: x.id, target: n.target.join(" "), html: n.html.slice(0, 120), msg: (n.any[0] || n.all[0] || n.none[0])?.message ?? "", data: (n.any[0] || {}).data ?? {} }))),
            ),
          );
          for (const x of v) {
            const id = await page.evaluate((sel) => {
              const el = document.querySelector(sel);
              if (!el) return null;
              el.dataset.dpId ||= `ax${Math.random().toString(36).slice(2, 8)}`;
              return el.dataset.dpId;
            }, x.target).catch(() => null);
            const nm = await page.evaluate((i) => (document.querySelector(`[data-dp-id="${i}"]`)?.textContent || "").replace(/\s+/g, " ").trim().slice(0, 60), id).catch(() => "");
            await hit(page, {
              check: "contrast", rule: x.rule === "color-contrast" ? "WCAG 1.4.3 text contrast" : "WCAG 1.4.1 link told from text by color alone", surface: s.id, device, theme, id, name: nm,
              value: x.data.contrastRatio ? `${x.data.contrastRatio}:1 (${x.data.fgColor} on ${x.data.bgColor}, ${x.data.fontSize} ${x.data.fontWeight})` : x.msg.slice(0, 120),
              threshold: x.data.expectedContrastRatio ?? null, selector: x.target, key: `contrast|${x.rule}|${x.target.replace(/\[data-dp-id[^\]]*\]/g, "").replace(/:nth-child\(\d+\)/g, "").slice(-60)}`,
            });
          }
          for (const t of await dp(page, "nonText"))
            await hit(page, { check: "contrast", rule: t.required ? `WCAG 1.4.11 non-text contrast: ${t.what}` : `non-text contrast (advisory): ${t.what}`, surface: s.id, device, theme, ...t, value: `${t.ratio}:1`, threshold: 3 });
          for (const c of await dp(page, "stateCues"))
            if (c.cues.length === 0 && c.bgVsOuter < 3 && c.bgRatio < 3 && c.textRatio < 3)
              await hit(page, { check: "contrast", rule: "WCAG 1.4.11 selected state under 3:1 with no other cue", surface: s.id, device, theme, ...c, value: `background ${c.bgRatio}:1, text ${c.textRatio}:1 against "${c.peer}"`, threshold: 3 });
        }
      });
  },

  /** WCAG 1.4.10: no sideways scrolling at 320 CSS px (which is also 1280 px at 400% zoom), at 280 px, and on a phone held sideways. */
  async reflow() {
    const sizes = [
      { label: "320x256 (400% zoom)", viewport: { width: 320, height: 256 } },
      { label: "280x653", viewport: { width: 280, height: 653 } },
      { label: "landscape 844x390", viewport: { width: 844, height: 390 } },
      { label: "landscape 667x375", viewport: { width: 667, height: 375 } },
    ];
    for (const sz of sizes)
      await overSurfaces("reflow", "phone", { contextOptions: { viewport: sz.viewport } }, async (page, s) => {
        const r = await dp(page, "hOverflow");
        if (r.page > 1) notes.push({ check: "reflow", surface: s.id, condition: sz.label, note: `page scrolls sideways by ${r.page} px` });
        for (const o of r.offenders)
          await hit(page, { check: "reflow", rule: o.clipped ? "WCAG 1.4.10 content cut off at the side" : "WCAG 1.4.10 content past the edge (sideways scroll)", surface: s.id, device: "phone", condition: sz.label, ...o, value: `${o.past} px past the edge${r.page > 1 ? `, page scrolls ${r.page} px` : ""}`, threshold: 0 });
        for (const c of (await dp(page, "clipping")).filter((x) => x.kind === "clip" || x.kind === "clipped-by-ancestor"))
          await hit(page, { check: "reflow", rule: "text clipped at a small window", surface: s.id, device: "phone", condition: sz.label, ...c, value: `${c.kind}, ${c.over} px hidden` });
      });
  },

  /** WCAG 1.4.12 text spacing, the largest in-app text size and 200% browser text (1.4.4): text that newly clips or overlaps. */
  async spacing() {
    const SPACING = "*{line-height:1.5!important;letter-spacing:0.12em!important;word-spacing:0.16em!important}p{margin-bottom:2em!important}";
    const conditions = [
      { label: "WCAG 1.4.12 text-spacing override", rule: "WCAG 1.4.12", css: SPACING, size: 1 },
      { label: "largest text size (1.5)", rule: "WCAG 1.4.4 / largest text size", size: 1.5 },
      { label: "browser text 200%", rule: "WCAG 1.4.4", css: "html{font-size:200%!important}", size: 1 },
    ];
    for (const device of devices)
      for (const c of conditions)
        await overSurfaces("spacing", device, { size: c.size }, async (page, s) => {
          const before = c.css ? new Set([...(await dp(page, "clipping")), ...(await dp(page, "overlaps"))].map((x) => `${x.name}|${x.kind ?? "overlap"}`)) : new Set();
          if (c.css) {
            await page.addStyleTag({ content: c.css });
            await page.waitForTimeout(250);
          }
          let shortened = 0;
          for (const x of await dp(page, "clipping")) {
            if (before.has(`${x.name}|${x.kind}`)) continue;
            if (x.kind === "clamp" || x.kind === "ellipsis") shortened++;
            else await hit(page, { check: "spacing", rule: `${c.rule}: text clipped`, surface: s.id, device, condition: c.label, ...x, value: `${x.kind}, ${x.over} px hidden` });
          }
          for (const x of await dp(page, "overlaps")) {
            if (before.has(`${x.name}|overlap`)) continue;
            await hit(page, { check: "spacing", rule: `${c.rule}: text overlaps`, surface: s.id, device, condition: c.label, ...x, value: `over "${x.other.slice(0, 40)}" by ${x.overlap}` });
          }
          if (shortened) notes.push({ check: "spacing", surface: s.id, device, condition: c.label, note: `${shortened} more texts shortened by an ellipsis or line clamp` });
          const h = await dp(page, "hOverflow");
          for (const o of h.offenders.filter((x) => !x.clipped)) await hit(page, { check: "spacing", rule: `${c.rule}: sideways scroll`, surface: s.id, device, condition: c.label, ...o, value: `${o.past} px past the edge` });
        });
  },

  /** Forced colors (Windows high contrast) and prefers-contrast: more: icons, unread markers, control boundaries and selected states still drawn. */
  async forced() {
    for (const [label, contextOptions] of [["forced colors", { forcedColors: "active" }], ["prefers-contrast: more", { contrast: "more" }]])
      for (const device of devices.filter((d) => d !== "tablet"))
        await overSurfaces("forced", device, { contextOptions }, async (page, s) => {
          for (const t of (await dp(page, "nonText")).filter((x) => x.required))
            await hit(page, { check: "forced", rule: `${label}: ${t.what} under 3:1`, surface: s.id, device, condition: label, ...t, value: `${t.ratio}:1`, threshold: 3 });
          for (const c of await dp(page, "stateCues"))
            if (c.cues.length === 0 && c.bgRatio < 1.5 && c.textRatio < 1.5)
              await hit(page, { check: "forced", rule: `${label}: selected state not drawn`, surface: s.id, device, condition: label, ...c, value: `background ${c.bgRatio}:1, text ${c.textRatio}:1 against "${c.peer}"` });
        });
  },

  /** WCAG 1.4.1: unread, selected, errors and links told by color alone, judged under every simulated deficiency, with strips as evidence. */
  async vision() {
    const DEFICIENCIES = ["protanopia", "deuteranopia", "tritanopia", "achromatopsia", "blurredVision", "reducedContrast"];
    const pick = ["list-magazine", "list-compact", "list-headlines", "list-gazette", "list-cards", "list-inbox", "state-feed-error", "settings-appearance", "nav-feeds", "menu-list-options", "health", "stats"];
    for (const device of devices.filter((d) => d !== "tablet"))
      await overSurfaces("vision", device, { filter: (s) => pick.includes(s.id) }, async (page, s) => {
        // Rows on All, so read and unread sit together.
        if (s.id.startsWith("list-")) {
          await page.goto("/l/all");
          await page.locator("main article[data-item-id]").first().waitFor({ timeout: 15000 });
          await settle(page, false);
        }
        const danger = schemes.find((x) => x.id === themes[0]).tokens.danger;
        const rc = await dp(page, "readCues");
        if (rc) {
          findings.push({
            check: "vision", rule: "unread against read (cues)", surface: s.id, device, theme: themes[0], key: `vision|read|${s.id}`, name: s.id,
            value: `cues: ${rc.cues.join(", ")}; title color ratio unread/read ${rc.textRatio}:1; read text ${rc.readTextVsBg}:1 on its background${rc.opacityRead < 1 ? `, opacity ${rc.opacityRead}` : ""}`, info: true,
          });
        }
        for (const c of await dp(page, "stateCues")) {
          if (c.cues.length) continue;
          const worst = worstUnderDeficiency(c.text, c.bg);
          await hit(page, { check: "vision", rule: "WCAG 1.4.1 selected state told by color alone", surface: s.id, device, theme: themes[0], ...c, value: `no shape, weight or underline cue against "${c.peer}"; text ${c.textRatio}:1, background ${c.bgRatio}:1; smallest ΔE ${worst.dE} under ${worst.type}` });
        }
        for (const d of await dp(page, "dangerOnly", danger)) await hit(page, { check: "vision", rule: "WCAG 1.4.1 error told by color alone", surface: s.id, device, theme: themes[0], ...d, value: `"${d.text}" in the danger color, no icon or wording` });
        // Evidence: the window under each deficiency and in grayscale, side by side.
        const cdp = await page.context().newCDPSession(page);
        const frames = [];
        for (const type of DEFICIENCIES) {
          await cdp.send("Emulation.setEmulatedVisionDeficiency", { type });
          frames.push({ label: type, buf: await page.screenshot() });
        }
        await cdp.send("Emulation.setEmulatedVisionDeficiency", { type: "none" });
        await page.addStyleTag({ content: "html{filter:grayscale(1)!important}" });
        frames.push({ label: "grayscale", buf: await page.screenshot() });
        const file = `frames/vision-${s.id}-${device}.png`;
        await strip(frames, file, device === "phone" ? 260 : 420);
        notes.push({ check: "vision", surface: s.id, device, note: "strip", file });
      });
  },

  /** A slow phone (4x CPU, a slow 4G-like network): layout shift (CLS sources) and skeletons that flash or do not match what replaces them. */
  async throttle() {
    const pick = ["list-magazine", "list-cards", "list-gazette", "list-headlines", "article-picture", "article-long", "nav-feeds", "stats", "settings-appearance", "health"];
    for (const device of devices.filter((d) => d !== "tablet")) {
      const ctx = await context(device);
      try {
        await setProfile(ctx, { "ui.theme": themes[0], "client.text_size": 1, "client.peek_seen": true });
        lib ??= await library(ctx);
        await ctx.addInitScript(() => {
          window.__dpShifts = [];
          window.__dpSkel = [];
          const sig = (n) => (n && n.nodeType === 1 ? `${n.tagName.toLowerCase()}${n.getAttribute("aria-label") ? `[${n.getAttribute("aria-label").slice(0, 30)}]` : ""}${n.dataset?.testid ? `#${n.dataset.testid}` : ""} "${(n.textContent || "").trim().slice(0, 40)}"` : "?");
          new PerformanceObserver((l) => {
            for (const e of l.getEntries()) if (!e.hadRecentInput) window.__dpShifts.push({ t: Math.round(e.startTime), v: e.value, src: (e.sources || []).map((x) => ({ n: sig(x.node), from: x.previousRect && [x.previousRect.y, x.previousRect.height].map(Math.round), to: x.currentRect && [x.currentRect.y, x.currentRect.height].map(Math.round) })) });
          }).observe({ type: "layout-shift", buffered: true });
          let on = null;
          let last = null;
          const look = () => {
            const sk = [...document.querySelectorAll('[data-skeleton],[data-testid="list-skeleton"],[aria-busy="true"]')].filter((e) => e.getBoundingClientRect().height > 0);
            if (sk.length) {
              if (on === null) on = performance.now();
              last = sk.map((e) => ({ sig: sig(e), rect: [...e.children].slice(0, 8).map((c) => Math.round(c.getBoundingClientRect().height)), top: Math.round(e.getBoundingClientRect().top) }));
            } else if (on !== null) {
              window.__dpSkel.push({ from: Math.round(on), ms: Math.round(performance.now() - on), shapes: last });
              on = null;
            }
          };
          new MutationObserver(look).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ["aria-busy"] });
        });
        for (const s of surfaces.filter((x) => pick.includes(x.id))) {
          await setProfile(ctx, { "client.layout": s.layout ?? "magazine" });
          const page = await ctx.newPage();
          try {
            const cdp = await ctx.newCDPSession(page);
            const slow = async (on) => {
              await cdp.send("Network.enable");
              await cdp.send("Network.emulateNetworkConditions", on ? { offline: false, latency: 150, downloadThroughput: (1.6 * 1024 * 1024) / 8, uploadThroughput: (750 * 1024) / 8 } : { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
              await cdp.send("Emulation.setCPUThrottlingRate", { rate: on ? 4 : 1 });
            };
            const isArticle = s.area === "article";
            let action;
            if (isArticle) {
              const feed = s.id === "article-long" ? "Long Essays" : "The Morning Ledger";
              await page.goto(`/l/all?feed=${lib.feed(feed)}`);
              await page.locator('main article[data-item-id] a[href^="/i/"]').first().waitFor({ timeout: 20000 });
              await page.evaluate(() => ((window.__dpShifts = []), (window.__dpSkel = [])));
              await slow(true);
              action = page.locator('main article[data-item-id] a[href^="/i/"]').first().click();
            } else {
              await slow(true);
              action = s.go(page, lib);
            }
            const frames = [];
            const t0 = Date.now();
            let done = false;
            action.then(() => (done = true), () => (done = true));
            while (Date.now() - t0 < 8000 && (!done || Date.now() - t0 < 2500) && frames.length < 14) {
              frames.push({ label: `${Date.now() - t0} ms`, buf: await page.screenshot().catch(() => null) });
              await page.waitForTimeout(150);
            }
            await action.catch(() => {});
            await page.waitForTimeout(2500);
            await slow(false);
            const file = `frames/throttle-${s.id}-${device}.png`;
            await strip(frames.filter((f) => f.buf), file, device === "phone" ? 200 : 360);
            const r = await page.evaluate(() => ({ shifts: window.__dpShifts, skel: window.__dpSkel, stillBusy: !!document.querySelector('[data-skeleton],[data-testid="list-skeleton"],[aria-busy="true"]'), rows: [...document.querySelectorAll("main article[data-item-id]")].slice(0, 8).map((e) => Math.round(e.getBoundingClientRect().height)) }));
            const cls = r.shifts.reduce((a, x) => a + x.v, 0);
            notes.push({ check: "throttle", surface: s.id, device, note: `CLS ${cls.toFixed(3)}, ${r.skel.length} skeleton spells`, file });
            if (cls >= 0.1)
              findings.push({ check: "throttle", rule: "layout shift (CLS) at or over 0.1 on a slow device", surface: s.id, device, value: `CLS ${cls.toFixed(3)}; biggest: ${r.shifts.sort((a, b) => b.v - a.v).slice(0, 3).map((x) => `${x.v.toFixed(3)} at ${x.t} ms (${x.src.map((y) => y.n).join("; ").slice(0, 160)})`).join(" | ")}`, threshold: 0.1, crop: file, key: `throttle|cls|${s.id}` });
            for (const k of r.skel) {
              if (k.ms < 300) findings.push({ check: "throttle", rule: "skeleton flashes (shown under 300 ms)", surface: s.id, device, value: `${k.ms} ms, ${k.shapes?.[0]?.sig ?? ""}`, crop: file, key: `throttle|flash|${k.shapes?.[0]?.sig?.slice(0, 40)}` });
              const sk = k.shapes?.find((x) => x.sig.includes("list-skeleton"));
              if (sk && r.rows.length) {
                const skH = sk.rect.reduce((a, b) => a + b, 0) / sk.rect.length;
                const rowH = r.rows.reduce((a, b) => a + b, 0) / r.rows.length;
                if (Math.abs(skH - rowH) / rowH > 0.2)
                  findings.push({ check: "throttle", rule: "skeleton rows do not match the rows that replace them", surface: s.id, device, value: `skeleton row ${Math.round(skH)} px, real row ${Math.round(rowH)} px (${s.layout ?? "magazine"} layout)`, crop: file, key: `throttle|shape|${s.layout ?? "magazine"}` });
              }
            }
            if (r.stillBusy) findings.push({ check: "throttle", rule: "still loading 2.5 s after the page settled", surface: s.id, device, value: "a skeleton or busy region stayed", crop: file, key: `throttle|stuck|${s.id}` });
          } catch (e) {
            missed.push({ check: "throttle", device, surface: s.id, why: String(e.message).split("\n")[0] });
          } finally {
            await page.close();
          }
        }
      } finally {
        await ctx.close();
      }
    }
  },

  /** The article that stays on its loading skeleton on a phone: opens articles from lists again and again and records any that never load. */
  async stuck() {
    const n = Number(opt.repeat);
    const ctx = await context("phone");
    try {
      await setProfile(ctx, { "ui.theme": themes[0], "client.text_size": 1, "client.peek_seen": true, "client.layout": "magazine" });
      lib ??= await library(ctx);
      const feeds = ["The Morning Ledger", "Long Essays", "Ünïcödé", null];
      let stuckCount = 0;
      for (let i = 0; i < n; i++) {
        const page = await ctx.newPage();
        const pending = new Map();
        const errors = [];
        page.on("request", (r) => pending.set(r, Date.now()));
        page.on("requestfinished", (r) => pending.delete(r));
        page.on("requestfailed", (r) => pending.delete(r));
        page.on("pageerror", (e) => errors.push(String(e).split("\n")[0]));
        page.on("console", (m) => m.type() === "error" && errors.push(`console: ${m.text().slice(0, 160)}`));
        try {
          const f = feeds[i % feeds.length];
          await page.goto(f ? `/l/all?feed=${lib.feed(f)}` : "/l/unread");
          const rows = page.locator('main article[data-item-id] a[href^="/i/"]');
          await rows.first().waitFor({ timeout: 20000 });
          const k = Math.min(i % 3, (await rows.count()) - 1);
          await rows.nth(k).click();
          const t0 = Date.now();
          const ok = await page
            .waitForFunction(() => document.querySelector('[aria-label="Article actions"]') && !document.querySelector('main [aria-busy="true"], [aria-busy="true"][role=status]'), null, { timeout: 12000 })
            .then(() => true, () => false);
          if (!ok) {
            stuckCount++;
            const waiting = [...pending].filter(([r]) => !r.url().endsWith("/api/events")).map(([r, t]) => `${r.method()} ${new URL(r.url()).pathname} (${Math.round((Date.now() - t) / 1000)} s)`);
            const state = await page.evaluate(() => ({ url: location.pathname + location.search, busy: [...document.querySelectorAll('[aria-busy="true"]')].map((e) => e.outerHTML.slice(0, 160)), toolbar: !!document.querySelector('[aria-label="Article actions"]') }));
            const file = `crops/stuck-${i}.png`;
            await page.screenshot({ path: join(out, file) });
            const later = await page.waitForFunction(() => !document.querySelector('[aria-busy="true"]'), null, { timeout: 20000 }).then(() => Date.now() - t0, () => null);
            findings.push({ check: "stuck", rule: "article stays on its loading skeleton", surface: `article from ${f ?? "All unread"} row ${k + 1}`, device: "phone", value: `not loaded after 12 s${later ? `, loaded at ${Math.round(later / 1000)} s` : ", still loading at 32 s"}; waiting: ${waiting.join(", ") || "nothing"}; errors: ${errors.join(" | ") || "none"}`, detail: state, crop: file, key: "stuck|article" });
          }
        } catch (e) {
          missed.push({ check: "stuck", device: "phone", surface: `try ${i}`, why: String(e.message).split("\n")[0] });
        } finally {
          await page.close();
        }
      }
      notes.push({ check: "stuck", note: `${stuckCount} of ${n} article opens stayed on the skeleton past 12 s` });
    } finally {
      await ctx.close();
    }
  },

  /** Tabs through each screen: focus drawn (pixels change), not hidden under a sticky bar (2.4.11), order follows the layout (2.4.3). */
  async keyboard() {
    for (const [label, contextOptions] of [["", {}], ["forced colors", { forcedColors: "active" }]])
      for (const device of devices.filter((d) => (label ? d === "desktop" : d !== "phone")))
        await overSurfaces("keyboard", device, { contextOptions, filter: (s) => s.area !== "menus" && (!label || ["list-magazine", "article-picture", "settings-appearance", "nav-feeds"].includes(s.id)) }, async (page, s) => {
          await page.evaluate(() => {
            document.activeElement?.blur?.();
            window.scrollTo(0, 0);
          });
          const seen = [];
          let first = null;
          for (let i = 0; i < 45; i++) {
            await page.keyboard.press("Tab");
            await page.waitForTimeout(40);
            const f = await dp(page, "focusInfo");
            if (!f) break;
            if (first === f.id) break;
            first ??= f.id;
            if (seen.some((x) => x.id === f.id)) break;
            const clip = clipOf(page, f.box, 4);
            let vis = null;
            if (clip && f.onScreen) {
              const a = await page.screenshot({ clip });
              await page.keyboard.press("Shift+Tab");
              await page.waitForTimeout(40);
              const b = await page.screenshot({ clip });
              await page.keyboard.press("Tab");
              await page.waitForTimeout(40);
              vis = await pixelDiff(a, b);
            }
            seen.push({ ...f, vis });
            const base = { check: "keyboard", surface: s.id, device, condition: label || undefined, ...f };
            if (i === 0 && !/skip/i.test(f.name)) notes.push({ check: "keyboard", surface: s.id, device, note: `first Tab stop is "${f.name}" (${f.role}), not a skip link` });
            if (f.opacity < 0.1) await hit(page, { ...base, rule: "WCAG 2.4.7 focus lands on an invisible control", value: `opacity ${f.opacity}` });
            else if (!f.onScreen) await hit(page, { ...base, rule: "focused control off screen (not scrolled into view)", value: `at ${f.rect.x},${f.rect.y}` });
            else if (f.covered >= f.onScreen && f.covered > 0) await hit(page, { ...base, rule: "WCAG 2.4.11 focus hidden under a fixed or sticky layer", value: `covered by ${f.by}` });
            else if (vis && vis.changed >= 0 && vis.changed < 12) await hit(page, { ...base, rule: "WCAG 2.4.7 no visible focus indicator", value: `${vis.changed} px change` });
            else if (vis && vis.changed > 0 && vis.max < 3) await hit(page, { ...base, rule: "focus indicator under 3:1 against its unfocused state (WCAG 1.4.11; 2.4.13 AAA)", value: `best ${vis.max}:1 over ${vis.changed} px`, threshold: 3 });
            const prev = seen.at(-2);
            if (prev && prev.landmarkEl === f.landmarkEl && f.onScreen && prev.onScreen) {
              const up = f.rect.y < prev.rect.y - 24 && f.rect.y + f.rect.h < prev.rect.y;
              const back = Math.abs(f.rect.y - prev.rect.y) < 8 && f.rect.x + f.rect.w <= prev.rect.x;
              if (up || back) await hit(page, { ...base, rule: "WCAG 2.4.3 focus order goes against the layout", value: `"${prev.name}" then "${f.name}" (${up ? "jumps up" : "jumps left"}) in ${f.landmarkEl}`, key: `keyboard|order|${s.id}|${stripName(prev.name)}>${stripName(f.name)}` });
            }
          }
          writeFileSync(join(out, "aria", `tab-order__${s.id}__${device}${label ? "__forced" : ""}.txt`), seen.map((x, i) => `${i + 1}. ${x.role} "${x.name}" in ${x.landmarkEl} at ${x.rect.x},${x.rect.y} ${x.rect.w}x${x.rect.h}${x.vis ? ` focus ${x.vis.changed} px max ${x.vis.max}:1` : ""}`).join("\n"));
        });
  },

  /** The accessibility tree as copy: unnamed controls, repeated names, heading levels and reading order; plus axe's other rules. */
  async aria() {
    const ROLES = /^(button|link|menuitem|menuitemradio|menuitemcheckbox|tab|checkbox|switch|radio|textbox|combobox|searchbox|slider|option|treeitem)$/;
    for (const device of devices.filter((d) => d !== "tablet"))
      await overSurfaces("aria", device, {}, async (page, s) => {
        const yml = await page.locator("body").ariaSnapshot();
        writeFileSync(join(out, "aria", `${s.id}__${device}.yml`), yml);
        const names = new Map();
        for (const line of yml.split("\n")) {
          const m = /^\s*- (\w+)(?: "((?:[^"\\]|\\.)*)")?/.exec(line);
          if (!m || !ROLES.test(m[1])) continue;
          const nm = (m[2] ?? "").trim();
          if (!nm) findings.push({ check: "aria", rule: "WCAG 4.1.2 control with no name", surface: s.id, device, value: line.trim().slice(0, 120), key: `aria|unnamed|${m[1]}|${line.trim().slice(0, 50)}` });
          else names.set(`${m[1]} "${nm}"`, (names.get(`${m[1]} "${nm}"`) ?? 0) + 1);
        }
        for (const [k, c] of names) if (c > 1) findings.push({ check: "aria", rule: "repeated control name (WCAG 2.4.6 / 2.4.4: which one?)", surface: s.id, device, value: `${k} x${c}`, key: `aria|dup|${k.slice(0, 60)}` });
        const hs = await dp(page, "outline");
        const levels = hs.map((h) => h.level);
        if (!levels.includes(1)) findings.push({ check: "aria", rule: "no level-1 heading", surface: s.id, device, value: hs.map((h) => `h${h.level} "${h.name.slice(0, 30)}"`).slice(0, 5).join(", ") || "no headings", key: `aria|noh1|${s.area}` });
        if (levels.filter((l) => l === 1).length > 1) findings.push({ check: "aria", rule: "more than one level-1 heading", surface: s.id, device, value: hs.filter((h) => h.level === 1).map((h) => `"${h.name.slice(0, 30)}"`).join(", "), key: `aria|h1s|${s.area}` });
        for (let i = 1; i < hs.length; i++)
          if (hs[i].level > hs[i - 1].level + 1) await hit(page, { check: "aria", rule: "WCAG 1.3.1 heading level skipped", surface: s.id, device, ...hs[i], value: `h${hs[i - 1].level} "${hs[i - 1].name.slice(0, 30)}" then h${hs[i].level}`, key: `aria|skip|${s.area}|h${hs[i - 1].level}>h${hs[i].level}` });
        for (let i = 1; i < hs.length; i++) {
          const a = hs[i - 1].rect;
          const b = hs[i].rect;
          if (b.y + b.h < a.y - 8 && Math.abs(a.x - b.x) < 200 && b.w && a.w) await hit(page, { check: "aria", rule: "WCAG 1.3.2 reading order differs from the layout", surface: s.id, device, ...hs[i], value: `"${hs[i].name.slice(0, 30)}" is read after "${hs[i - 1].name.slice(0, 30)}" but drawn above it` });
        }
        if (!(await page.evaluate(() => typeof window.axe === "object"))) await page.evaluate(AXE);
        const v = await page.evaluate(() =>
          window.axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"] }, rules: { "color-contrast": { enabled: false } } }).then((r) => r.violations.map((x) => ({ id: x.id, help: x.help, n: x.nodes.length, t: x.nodes.slice(0, 3).map((n) => n.target.join(" ")) }))),
        );
        for (const x of v) findings.push({ check: "aria", rule: `axe ${x.id}: ${x.help}`, surface: s.id, device, value: `${x.n} nodes: ${x.t.join(" | ").slice(0, 200)}`, key: `aria|axe|${x.id}` });
      });
  },

  /** Right-to-left and long strings (100-character names, a long compound, CJK) put into names and titles: new overflow, clipping and overlap. */
  async stress() {
    const LONG = "The Quarterly Review of Municipal Infrastructure, Transportation Planning and Public Works Engineering";
    const variants = {
      rtl: null,
      "100 characters": LONG,
      "long compound": "Donaudampfschifffahrtselektrizitätenhauptbetriebswerkbauunterbeamtengesellschaft",
      CJK: "東京都市圏における公共交通機関の利用状況と今後の都市計画に関する総合的な調査報告書第三版改訂",
    };
    for (const device of devices.filter((d) => d !== "tablet"))
      await overSurfaces("stress", device, { signedOut: false }, async (page, s) => {
        const names = [...lib.boot.feeds.map((f) => f.title), ...lib.boot.folders.map((f) => f.name)];
        const base = await page.evaluate(() => location.href);
        const before = new Set([...(await dp(page, "clipping")), ...(await dp(page, "overlaps"))].map((x) => `${x.kind ?? "overlap"}|${x.role}|${x.landmark}`));
        const hBefore = (await dp(page, "hOverflow")).page;
        for (const [label, str] of Object.entries(variants)) {
          if (page.url() !== base || label !== "rtl") {
            await page.goto(base);
            await s.go(page, lib).catch(() => {});
            await settle(page, s.area === "menus");
          }
          const changed = await page.evaluate(
            ([str, names]) => {
              if (!str) {
                document.documentElement.dir = "rtl";
                return -1;
              }
              let n = 0;
              const set = new Set(names);
              const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
              const todo = [];
              for (let t = w.nextNode(); t; t = w.nextNode()) {
                const v = t.textContent.trim();
                if (!v) continue;
                const title = t.parentElement?.closest('a[href^="/i/"], h1, h2, h3');
                if (set.has(v) || (title && v.length > 12 && !title.closest("[class*=prose]"))) todo.push(t);
              }
              for (const t of todo) ((t.textContent = str), n++);
              return n;
            },
            [str, names],
          );
          await page.waitForTimeout(200);
          const h = await dp(page, "hOverflow");
          if (h.page > Math.max(1, hBefore)) for (const o of h.offenders.filter((x) => !x.clipped).slice(0, 5)) await hit(page, { check: "stress", rule: `${label}: sideways scroll`, surface: s.id, device, condition: label, ...o, value: `${o.past} px past the edge (${changed} strings replaced)` });
          for (const x of await dp(page, "clipping")) {
            if (x.kind !== "clip" && x.kind !== "clipped-by-ancestor") continue;
            if (before.has(`${x.kind}|${x.role}|${x.landmark}`)) continue;
            await hit(page, { check: "stress", rule: `${label}: text clipped`, surface: s.id, device, condition: label, ...x, value: `${x.kind}, ${x.over} px hidden` });
          }
          for (const x of await dp(page, "overlaps")) {
            if (before.has(`overlap|${x.role}|${x.landmark}`)) continue;
            await hit(page, { check: "stress", rule: `${label}: text overlaps`, surface: s.id, device, condition: label, ...x, value: `over "${x.other.slice(0, 40)}" by ${x.overlap}` });
          }
          if (label === "rtl") {
            const file = `frames/rtl-${s.id}-${device}.png`;
            await page.screenshot({ path: join(out, file) });
          }
        }
      });
  },

  /** The interface inventory (Brad Frost): every rendered button, input, menu item, badge and icon, and every style value, grouped so near-duplicates show. */
  async inventory() {
    for (const device of devices.filter((d) => d !== "tablet"))
      await overSurfaces("inventory", device, {}, async (page, s) => {
        const { items, values } = await dp(page, "inventoryScan");
        for (const it of items) {
          const k = `${it.kind}|${JSON.stringify(it.style)}`;
          const prior = inventory.find((x) => x.k === k);
          if (prior) {
            prior.count++;
            prior.surfaces.add(s.id);
            continue;
          }
          const clip = clipOf(page, it.rect, 6);
          let file = null;
          if (clip && inventory.filter((x) => x.kind === it.kind).length < 120) {
            file = `inventory/${it.kind}-${String(inventory.length).padStart(4, "0")}.png`;
            await page.screenshot({ path: join(out, file), clip }).catch(() => (file = null));
          }
          inventory.push({ k, kind: it.kind, style: it.style, name: it.name, count: 1, surfaces: new Set([s.id]), device, file });
        }
        for (const [group, m] of Object.entries(values)) {
          const v = (inventory.values ??= {});
          v[group] ??= {};
          for (const [val, c] of Object.entries(m)) v[group][val] = (v[group][val] ?? 0) + c;
        }
      });
  },
};

// ---- Deficiency arithmetic (Machado, Oliveira and Fernandes 2009, severity 1) ------------------------------------------
const MACHADO = {
  protanopia: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]],
  deuteranopia: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.01182, 0.04294, 0.968881]],
  tritanopia: [[1.255528, -0.076749, -0.178779], [-0.078411, 0.930809, 0.147602], [0.004733, 0.691367, 0.3039]],
};
const lin = (v) => ((v /= 255) <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
const unlin = (v) => 255 * (v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055);
function simulate(rgb, type) {
  const l = rgb.map(lin);
  if (type === "achromatopsia") {
    const y = 0.2126 * l[0] + 0.7152 * l[1] + 0.0722 * l[2];
    return [y, y, y].map((v) => Math.max(0, Math.min(255, unlin(v))));
  }
  const m = MACHADO[type];
  return m.map((row) => Math.max(0, Math.min(255, unlin(Math.max(0, Math.min(1, row[0] * l[0] + row[1] * l[1] + row[2] * l[2]))))));
}
function toLab([r, g, b]) {
  const [R, G, B] = [r, g, b].map(lin);
  const f = (t) => (t > 0.008856 ? Math.cbrt(t) : 7.787 * t + 16 / 116);
  const x = f((R * 0.4124 + G * 0.3576 + B * 0.1805) / 0.95047);
  const y = f(R * 0.2126 + G * 0.7152 + B * 0.0722);
  const z = f((R * 0.0193 + G * 0.1192 + B * 0.9505) / 1.08883);
  return [116 * y - 16, 500 * (x - y), 200 * (y - z)];
}
const deltaE = (a, b) => Math.hypot(...toLab(a).map((v, i) => v - toLab(b)[i]));
/** The smallest separation (CIE76 ΔE) between two states' colors under any deficiency, taking the larger of text and background. */
function worstUnderDeficiency(text, bg) {
  let worst = { dE: Infinity, type: "" };
  for (const type of ["protanopia", "deuteranopia", "tritanopia", "achromatopsia"]) {
    const d = Math.max(deltaE(simulate(text[0], type), simulate(text[1], type)), deltaE(simulate(bg[0], type), simulate(bg[1], type)));
    if (d < worst.dE) worst = { dE: Math.round(d * 10) / 10, type };
  }
  return worst;
}

/** Lays `frames` ({label, buf}) side by side, each `width` px wide, into one PNG at `file`. */
async function strip(frames, file, width) {
  if (!frames.length) return;
  const html = `<!doctype html><meta charset="utf-8"><style>body{margin:0;background:#ddd;font:12px system-ui;display:flex;gap:6px;padding:6px;align-items:flex-start;width:max-content}figure{margin:0}img{display:block;width:${width}px;border:1px solid #999}figcaption{padding:2px 0}</style>${frames.map((f) => `<figure><img src="data:image/png;base64,${f.buf.toString("base64")}"><figcaption>${f.label}</figcaption></figure>`).join("")}`;
  const p = await browser.newPage({ viewport: { width: 400, height: 300 } });
  try {
    await p.setContent(html);
    await p.evaluate(() => Promise.all([...document.images].map((i) => i.decode().catch(() => {}))));
    await p.screenshot({ path: join(out, file), fullPage: true });
  } finally {
    await p.close();
  }
}

// ---- Run --------------------------------------------------------------------------------------------------------------
const started = Date.now();
try {
  for (const c of checks) {
    log(`check ${c}`);
    const before = findings.length;
    await RUN[c]();
    await removeDevices(devices[0]);
    log(`check ${c}: ${findings.length - before} findings`);
    writeFileSync(join(out, "findings.json"), JSON.stringify({ origin, taken: new Date().toISOString(), checks, devices, themes, findings, notes, missed }, null, 2));
  }
  if (inventory.length) await inventorySheets();
  writeFileSync(join(out, "findings.json"), JSON.stringify({ origin, taken: new Date().toISOString(), checks, devices, themes, findings, notes, missed }, null, 2));
  writeFileSync(join(out, "report.md"), report());
} finally {
  await browser.close();
}
console.log(`\n${findings.length} findings in ${new Set(findings.map((f) => f.key)).size} groups, ${missed.length} missed, ${Math.round((Date.now() - started) / 60000)} min. ${join(out, "report.md")}`);
process.exit(missed.length ? 1 : 0);

/** report.md: per check, one section per root-cause group (key) with its instances, worst values and up to three crops. */
function report() {
  const esc = (s) => String(s ?? "").replace(/\|/g, "\\|").replace(/\n/g, " ");
  const lines = [`# Design probes`, "", `${new Date().toISOString().slice(0, 16).replace("T", " ")} UTC · ${origin} · checks ${checks.join(", ")} · devices ${devices.join(", ")} · ${findings.length} findings in ${new Set(findings.map((f) => f.key)).size} groups · ${missed.length} missed`, ""];
  for (const c of checks) {
    const fs = findings.filter((f) => f.check === c);
    lines.push(`## ${c} (${fs.length})`, "");
    const groups = new Map();
    for (const f of fs) groups.set(f.key, [...(groups.get(f.key) ?? []), f]);
    for (const [, g] of [...groups].sort((a, b) => b[1].length - a[1].length)) {
      const f = g[0];
      const uniq = (k) => [...new Set(g.map((x) => x[k]).filter(Boolean))];
      lines.push(`### ${esc(f.rule)}: ${esc(f.name || f.surface)}`, "");
      lines.push(`${g.length} instance(s) · surfaces ${uniq("surface").slice(0, 8).join(", ")}${uniq("surface").length > 8 ? " …" : ""} · devices ${uniq("device").join(", ")}${uniq("theme").length ? ` · themes ${uniq("theme").length > 6 ? `${uniq("theme").length} themes` : uniq("theme").join(", ")}` : ""}${uniq("condition").length ? ` · ${uniq("condition").join(", ")}` : ""}`, "");
      lines.push(`Measured: ${esc(f.value)}${f.threshold ? ` (needs ${f.threshold})` : ""}${f.role ? ` · ${f.role} in ${esc(f.landmark)}` : ""}`, "");
      for (const x of g.filter((y) => y.crop).slice(0, 3)) lines.push(`![${esc(x.surface)} ${x.device}](${x.crop})`);
      lines.push("");
    }
    for (const n of notes.filter((x) => x.check === c)) lines.push(`- note: ${esc(n.surface ?? "")} ${esc(n.device ?? "")} ${esc(n.condition ?? "")} ${esc(n.note)}${n.file ? ` ([strip](${n.file}))` : ""}`);
    lines.push("");
  }
  if (missed.length) lines.push("## Missed", "", ...missed.map((m) => `- ${m.check} ${m.surface} ${m.device ?? ""}: ${esc(m.why)}`), "");
  if (inventory.length) lines.push("## Inventory", "", ...["button", "input", "menuitem", "badge", "icon", "values"].map((k) => `- [${k}](inventory-${k}.png)`), "");
  return lines.join("\n");
}

/** inventory-<kind>.png: one sheet per component kind, distinct styles sorted so near-duplicates sit together; and a sheet of style values. */
async function inventorySheets() {
  const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;");
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  try {
    for (const kind of ["button", "input", "menuitem", "badge", "icon"]) {
      const list = inventory.filter((x) => x.kind === kind).sort((a, b) => JSON.stringify(a.style).localeCompare(JSON.stringify(b.style)));
      const cells = list
        .map((x) => `<figure>${x.file ? `<img src="${pathToFileURL(join(out, x.file)).href}">` : "<div class=none>(off screen)</div>"}<figcaption><b>${esc(x.name || "")}</b> ×${x.count} · ${x.device}<br>${Object.entries(x.style).map(([k, v]) => `${k}: ${esc(v)}`).join("<br>")}</figcaption></figure>`)
        .join("");
      writeFileSync(join(out, `inventory-${kind}.html`), `<!doctype html><meta charset="utf-8"><style>body{font:11px system-ui;margin:12px;background:#eee}h1{font-size:16px}.g{display:flex;flex-wrap:wrap;gap:10px}figure{margin:0;background:#fff;padding:6px;border:1px solid #ccc;width:230px}img{max-width:218px;display:block;margin-bottom:4px;background:repeating-conic-gradient(#ddd 0 25%,#fff 0 50%) 0 0/10px 10px}</style><h1>${kind}: ${list.length} distinct styles</h1><div class="g">${cells}</div>`);
      await page.goto(pathToFileURL(join(out, `inventory-${kind}.html`)).href);
      await page.evaluate(() => Promise.all([...document.images].map((i) => i.decode().catch(() => {}))));
      await page.screenshot({ path: join(out, `inventory-${kind}.png`), fullPage: true });
    }
    const v = inventory.values ?? {};
    const sections = Object.entries(v)
      .map(([g, m]) => {
        const rows = Object.entries(m).sort((a, b) => (parseFloat(a[0]) || 0) - (parseFloat(b[0]) || 0) || b[1] - a[1]);
        const swatch = (val) => (["color", "bg"].includes(g) ? `<span class=s style="background:${val}"></span>` : "");
        return `<section><h2>${g} (${rows.length} distinct)</h2><table>${rows.map(([val, c]) => `<tr><td>${swatch(val)}${esc(val).slice(0, 90)}</td><td>${c}</td></tr>`).join("")}</table></section>`;
      })
      .join("");
    writeFileSync(join(out, "inventory-values.html"), `<!doctype html><meta charset="utf-8"><style>body{font:11px system-ui;margin:12px;background:#fff;display:flex;flex-wrap:wrap;gap:24px;align-items:flex-start}td{padding:1px 6px;border-bottom:1px solid #eee}.s{display:inline-block;width:12px;height:12px;border:1px solid #999;margin-right:4px;vertical-align:middle}h2{font-size:13px}</style>${sections}`);
    await page.goto(pathToFileURL(join(out, "inventory-values.html")).href);
    await page.screenshot({ path: join(out, "inventory-values.png"), fullPage: true });
    writeFileSync(join(out, "inventory.json"), JSON.stringify({ items: inventory.map(({ surfaces: sx, ...x }) => ({ ...x, surfaces: [...sx] })), values: v }, null, 2));
  } finally {
    await page.close();
  }
}
