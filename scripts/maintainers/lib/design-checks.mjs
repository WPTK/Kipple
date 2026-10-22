// The measurements design-probes.mjs takes inside the page. `installChecks` is handed to context.addInitScript, so it
// runs in the browser before the app on every page and must stay self-contained: it puts the checks on window.__kp, and
// the probe calls them with page.evaluate. Each check returns plain data (a description of every element it names, with
// its box in viewport pixels, so the probe can crop a picture of it); judging thresholds live in the probe.
/* global window, document, location, getComputedStyle, innerWidth, innerHeight, NodeFilter, HTMLElement */
export function installChecks() {
  const K = (window.__kp = {});

  // ---- Colour -------------------------------------------------------------------------------------------------------
  // Any CSS colour (rgb, oklch, color-mix...) to sRGB through a 1x1 canvas, so the maths never parses colour syntax.
  let cx;
  const cache = new Map();
  K.rgba = (c) => {
    if (!c || c === "none") return null;
    if (cache.has(c)) return cache.get(c);
    if (!cx) {
      const cv = document.createElement("canvas");
      cv.width = cv.height = 1;
      cx = cv.getContext("2d", { willReadFrequently: true });
    }
    cx.clearRect(0, 0, 1, 1);
    cx.fillStyle = "#000";
    cx.fillStyle = c;
    cx.fillRect(0, 0, 1, 1);
    const d = cx.getImageData(0, 0, 1, 1).data;
    const v = { r: d[0], g: d[1], b: d[2], a: d[3] / 255 };
    cache.set(c, v);
    return v;
  };
  const over = (top, bottom) => ({ r: top.r * top.a + bottom.r * (1 - top.a), g: top.g * top.a + bottom.g * (1 - top.a), b: top.b * top.a + bottom.b * (1 - top.a), a: 1 });
  const lum = (c) => {
    const f = (v) => ((v /= 255) <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b);
  };
  K.ratio = (a, b) => {
    const [x, y] = [lum(a), lum(b)].sort((m, n) => n - m);
    return Math.round(((x + 0.05) / (y + 0.05)) * 100) / 100;
  };
  K.hex = (c) => {
    const v = K.rgba(c);
    if (!v || v.a === 0) return "none";
    const h = [v.r, v.g, v.b].map((n) => n.toString(16).padStart(2, "0")).join("");
    return v.a < 1 ? `#${h}/${Math.round(v.a * 100)}` : `#${h}`;
  };
  /** The colour behind `el`: its own and its ancestors' backgrounds composited down to the page. `image` says a picture may sit under it. */
  K.bg = (el) => {
    const layers = [];
    let image = false;
    for (let e = el; e && e.nodeType === 1; e = e.parentElement) {
      const cs = getComputedStyle(e);
      if (cs.backgroundImage !== "none") image = true;
      const c = K.rgba(cs.backgroundColor);
      if (c && c.a > 0) {
        layers.push(c);
        if (c.a >= 0.99) break;
      }
    }
    let base = { r: 255, g: 255, b: 255, a: 1 };
    for (const l of layers.reverse()) base = over(l, base);
    return { color: base, image };
  };
  K.opacity = (el) => {
    let o = 1;
    for (let e = el; e && e.nodeType === 1; e = e.parentElement) o *= Number(getComputedStyle(e).opacity);
    return o;
  };
  /** `color` (a CSS colour) as painted on `el`'s background, its alpha and the element's opacity included. */
  K.painted = (color, el, bg = K.bg(el).color) => {
    const c = K.rgba(color);
    if (!c) return null;
    return over({ ...c, a: c.a * K.opacity(el) }, bg);
  };

  // ---- Describing elements ------------------------------------------------------------------------------------------
  K.name = (el) =>
    (
      el.getAttribute("aria-label") ||
      (el.getAttribute("aria-labelledby") || "")
        .split(" ")
        .map((id) => document.getElementById(id)?.textContent || "")
        .join(" ") ||
      el.getAttribute("title") ||
      el.getAttribute("alt") ||
      el.getAttribute("placeholder") ||
      el.innerText ||
      el.textContent ||
      ""
    )
      .replace(/\s+/g, " ")
      .trim()
      .slice(0, 80);
  const REGIONS = '[role=menu],[role=dialog],[role=toolbar],[role=tablist],nav,header,aside,main,footer,[role=navigation],[role=banner],[role=status],[role=alert]';
  K.region = (el) => {
    const r = el.closest(REGIONS);
    if (!r) return "body";
    const label = r.getAttribute("aria-label");
    const role = r.getAttribute("role") || r.tagName.toLowerCase();
    return label ? `${role}[${label.slice(0, 30)}]` : role;
  };
  const cls = (el) => String(el.className?.baseVal ?? el.className ?? "").split(/\s+/).filter((c) => c && !/^(hover|focus|data-|aria-|group|peer)/.test(c)).slice(0, 4).join(".");
  /** What a finding says about an element. `sig` names the kind of element, not this one: rows give their component, not their title, so one finding covers every row. */
  K.desc = (el) => {
    const tag = el.tagName.toLowerCase();
    const role = el.getAttribute("role") || "";
    const name = K.name(el);
    const r = el.getBoundingClientRect();
    const region = K.region(el);
    const row = el.closest("[data-item-id]");
    const what = row && row !== el ? `row ${tag}${role ? `[${role}]` : ""}.${cls(el)}` : `${tag}${role ? `[${role}]` : ""} "${name.replace(/\d[\d,.]*/g, "#").slice(0, 40)}"`;
    return { tag, role, name, region, rect: { x: r.x, y: r.y, w: r.width, h: r.height }, sig: `${region} > ${what}` };
  };
  K.srOnly = (el) => {
    if (el.closest(".sr-only,.sr-only-live")) return true;
    const cs = getComputedStyle(el);
    return (cs.clip && cs.clip !== "auto") || cs.clipPath === "inset(50%)";
  };
  /** Laid out, painted and at least partly inside the window. */
  K.shown = (el) => {
    const r = el.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) return false;
    if (r.bottom <= 0 || r.right <= 0 || r.top >= innerHeight || r.left >= innerWidth) return false;
    const cs = getComputedStyle(el);
    if (cs.visibility === "hidden" || cs.display === "none") return false;
    if (el.closest("[inert]")) return false;
    return K.opacity(el) > 0.05 && !K.srOnly(el);
  };

  // ---- Targets (WCAG 2.5.8, HIG 44 pt) ------------------------------------------------------------------------------
  const TARGETS =
    'a[href],button,input:not([type=hidden]),select,textarea,summary,[role=button],[role=link],[role=menuitem],[role=menuitemradio],[role=menuitemcheckbox],[role=tab],[role=checkbox],[role=switch],[role=radio],[role=option],[role=treeitem],[role=slider],[role=separator][tabindex],[tabindex]:not([tabindex="-1"])';
  K.targetEls = () => {
    let els = [...document.querySelectorAll(TARGETS)].map((e) => {
      // A native checkbox drawn by its label: the label is what a finger hits.
      if ((e.type === "checkbox" || e.type === "radio") && e.getBoundingClientRect().width <= 2) return e.closest("label");
      return e;
    });
    els = [...new Set(els)].filter((e) => e && K.shown(e) && !e.disabled && e.getAttribute("aria-disabled") !== "true");
    // A control that exactly fills the control around it is one target.
    return els.filter((e) => {
      const p = e.parentElement?.closest(TARGETS);
      if (!p || !els.includes(p)) return true;
      const a = e.getBoundingClientRect();
      const b = p.getBoundingClientRect();
      return !(Math.abs(a.width - b.width) < 2 && Math.abs(a.height - b.height) < 2);
    });
  };
  /** Targets smaller than `min` px, and pairs of targets closer than `gap` px where one is smaller than `min`. Inline links in running text are exempt (2.5.8). */
  K.targets = (min, gap = 8) => {
    const els = K.targetEls().filter((e) => !(e.tagName === "A" && getComputedStyle(e).display === "inline" && e.closest("p,li,blockquote,td,figcaption,.article-body")));
    // A stretched link (a ::before laid over its row) is hit anywhere on the row, not just on its words.
    const hitBox = (e) => {
      const b = getComputedStyle(e, "::before");
      if (b.content === "none" || b.position !== "absolute") return e.getBoundingClientRect();
      for (let a = e.parentElement; a; a = a.parentElement) if (getComputedStyle(a).position !== "static") return a.getBoundingClientRect();
      return e.getBoundingClientRect();
    };
    const rs = els.map(hitBox);
    const isSmall = rs.map((r) => r.width < min - 0.5 || r.height < min - 0.5);
    const small = [];
    els.forEach((e, i) => {
      if (!isSmall[i]) return;
      const r = rs[i];
      const cx = r.x + r.width / 2;
      const cy = r.y + r.height / 2;
      // The 2.5.8 spacing exception: a 24 px circle on the target's centre touches no other target and no other small target's circle.
      const spaced = rs.every((o, j) => {
        if (j === i) return true;
        const dx = Math.max(o.x - cx, 0, cx - o.right);
        const dy = Math.max(o.y - cy, 0, cy - o.bottom);
        if (Math.hypot(dx, dy) < 12) return false;
        return !isSmall[j] || Math.hypot(o.x + o.width / 2 - cx, o.y + o.height / 2 - cy) >= 24;
      });
      small.push({ ...K.desc(e), w: Math.round(r.width), h: Math.round(r.height), spaced });
    });
    const close = [];
    for (let i = 0; i < els.length; i++)
      for (let j = i + 1; j < els.length; j++) {
        if (!isSmall[i] && !isSmall[j]) continue;
        if (els[i].contains(els[j]) || els[j].contains(els[i])) continue;
        const a = rs[i];
        const b = rs[j];
        const gx = Math.max(b.x - a.right, a.x - b.right);
        const gy = Math.max(b.y - a.bottom, a.y - b.bottom);
        const d = gx < 0 && gy >= 0 ? gy : gy < 0 && gx >= 0 ? gx : null;
        if (d !== null && d < gap) {
          const box = { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.max(a.right, b.right) - Math.min(a.x, b.x), h: Math.max(a.bottom, b.bottom) - Math.min(a.y, b.y) };
          const da = K.desc(els[i]);
          const db = K.desc(els[j]);
          // Named after the small one, so a small control next to many neighbours is one finding.
          const [s1, s2] = isSmall[i] ? [da, db] : [db, da];
          close.push({ ...s1, rect: box, gap: Math.round(d), other: s2.name });
        }
      }
    return { small, close, count: els.length };
  };

  // ---- Non-text contrast (WCAG 1.4.11) ------------------------------------------------------------------------------
  const SELECTED = '[aria-current=page],[aria-current=true],[aria-selected=true],[aria-checked=true],[aria-pressed=true],[data-state=checked],[data-state=on],[data-state=active]';
  const isSelected = (e) => e.matches(SELECTED);
  /** Icons that carry a control's meaning, unread markers, control boundaries and selected states, each with its contrast. */
  K.nontext = () => {
    const out = [];
    for (const svg of document.querySelectorAll("svg")) {
      if (!K.shown(svg)) continue;
      const c = svg.closest("button,a,[role=button],[role=menuitem],[role=tab],[role=link],summary");
      if (!(c && !(c.innerText || "").trim()) && svg.getAttribute("role") !== "img") continue;
      const cs = getComputedStyle(svg);
      const paint = cs.stroke && cs.stroke !== "none" ? cs.stroke : cs.fill && cs.fill !== "none" ? cs.fill : cs.color;
      const bg = K.bg(svg.parentElement);
      const fg = K.painted(paint, svg, bg.color);
      if (fg) out.push({ kind: "icon", ...K.desc(c ?? svg), value: K.ratio(fg, bg.color), image: bg.image });
    }
    for (const dot of document.querySelectorAll('.kp-unread-dot[data-unread="true"],[data-testid=unread-dot]')) {
      if (!K.shown(dot)) continue;
      const bg = K.bg(dot.parentElement);
      const fg = K.painted(getComputedStyle(dot).backgroundColor, dot, bg.color);
      out.push({ kind: "unread-marker", ...K.desc(dot), value: K.ratio(fg, bg.color), image: bg.image });
    }
    for (const el of document.querySelectorAll('input:not([type=checkbox]):not([type=radio]):not([type=hidden]),select,textarea,[role=switch],[role=checkbox],input[type=checkbox],input[type=radio]')) {
      if (!K.shown(el)) continue;
      const cs = getComputedStyle(el);
      const outer = K.bg(el.parentElement).color;
      const own = K.painted(cs.backgroundColor, el, outer) ?? outer;
      const border = parseFloat(cs.borderTopWidth) > 0 ? K.painted(cs.borderTopColor, el, outer) : null;
      // A native checkbox or radio paints itself with accent-color; its box is the accent.
      const accent = (el.type === "checkbox" || el.type === "radio") && cs.accentColor !== "auto" ? K.painted(cs.accentColor, el, outer) : null;
      const value = Math.max(K.ratio(own, outer), border ? K.ratio(border, outer) : 0, accent ? K.ratio(accent, outer) : 0);
      out.push({ kind: "control-boundary", ...K.desc(el), value });
    }
    for (const el of document.querySelectorAll(SELECTED)) {
      if (!K.shown(el) || el.tagName === "INPUT") continue;
      const role = el.getAttribute("role");
      const group = el.parentElement?.parentElement ?? document.body;
      const peer = [...group.querySelectorAll(role ? `${el.tagName}[role="${role}"]` : el.tagName)].find((x) => x !== el && !isSelected(x) && K.shown(x));
      if (!peer) continue;
      const a = getComputedStyle(el);
      const b = getComputedStyle(peer);
      const outer = K.bg(group).color;
      const bgA = K.painted(a.backgroundColor, el, outer) ?? outer;
      const bgB = K.painted(b.backgroundColor, peer, outer) ?? outer;
      const fgA = K.painted(a.color, el, bgA);
      const fgB = K.painted(b.color, peer, bgB);
      const shape =
        a.fontWeight !== b.fontWeight ||
        a.textDecorationLine !== b.textDecorationLine ||
        a.borderTopWidth !== b.borderTopWidth ||
        a.borderBottomWidth !== b.borderBottomWidth ||
        a.borderLeftWidth !== b.borderLeftWidth ||
        a.outlineStyle !== b.outlineStyle ||
        el.querySelectorAll("svg").length !== peer.querySelectorAll("svg").length ||
        getComputedStyle(el, "::before").content !== getComputedStyle(peer, "::before").content ||
        getComputedStyle(el, "::after").content !== getComputedStyle(peer, "::after").content;
      const borderDiff = a.borderTopColor !== b.borderTopColor || a.borderBottomColor !== b.borderBottomColor ? K.ratio(K.painted(a.borderBottomColor, el, outer) ?? outer, K.painted(b.borderBottomColor, peer, outer) ?? outer) : 1;
      const value = Math.max(K.ratio(bgA, bgB), borderDiff, fgA && fgB ? K.ratio(fgA, fgB) : 1);
      out.push({ kind: "selected-state", ...K.desc(el), value, shape, peer: K.name(peer) });
    }
    return out;
  };

  // ---- Unread told apart from read (WCAG 1.4.1, and read text at 4.5:1) ----------------------------------------------
  K.unreadCues = () => {
    const rows = [...document.querySelectorAll("main article[data-item-id]")].filter(K.shown);
    const unread = (r) => {
      const d = r.querySelector(".kp-unread-dot");
      if (d) return d.dataset.unread === "true";
      if (r.hasAttribute("data-read")) return false;
      return r.querySelector("[data-row-unread]") ? true : null;
    };
    const title = (r) => r.querySelector('h2 a[href^="/i/"],h3 a[href^="/i/"],h2,h3') ?? r.querySelector('a[href^="/i/"]');
    const u = rows.find((r) => unread(r) === true);
    const rd = rows.find((r) => unread(r) === false);
    if (!u || !rd) return null;
    const tu = title(u);
    const tr = title(rd);
    const a = getComputedStyle(tu);
    const b = getComputedStyle(tr);
    const bgU = K.bg(tu).color;
    const bgR = K.bg(tr).color;
    const dotU = u.querySelector('.kp-unread-dot[data-unread="true"]');
    const cues = {
      marker: !!(dotU && K.shown(dotU)),
      weight: a.fontWeight !== b.fontWeight,
      style: a.fontStyle !== b.fontStyle || a.textDecorationLine !== b.textDecorationLine,
      text: !!u.querySelector("[data-row-unread]") && getComputedStyle(u.querySelector("[data-row-unread]"), "::before").content !== "none",
      colorRatio: K.ratio(K.painted(a.color, tu, bgU), K.painted(b.color, tr, bgR)),
    };
    return { cues, readText: K.ratio(K.painted(b.color, tr, bgR), bgR), unreadDesc: K.desc(tu), readDesc: K.desc(tr) };
  };

  // ---- Clipping, overlap and sideways scrolling (WCAG 1.4.4, 1.4.10, 1.4.12) -----------------------------------------
  /** Text boxes that clip their own text (overflow hidden, an ellipsis or a line clamp) and are cutting some off now. */
  K.clipped = () => {
    const out = [];
    for (const el of document.querySelectorAll("body *")) {
      if (!(el instanceof HTMLElement) || !K.shown(el)) continue;
      const text = (el.innerText || "").trim();
      if (!text || text.length > 600 || el.querySelectorAll("*").length > 20) continue;
      const cs = getComputedStyle(el);
      const ov = cs.overflowX + cs.overflowY;
      const clamp = cs.webkitLineClamp && cs.webkitLineClamp !== "none";
      if (/auto|scroll/.test(ov)) continue;
      if (!/hidden|clip/.test(ov) && cs.textOverflow !== "ellipsis" && !clamp) continue;
      const dx = el.scrollWidth - el.clientWidth;
      const dy = el.scrollHeight - el.clientHeight;
      if (dx > 2 || dy > 2)
        out.push({ ...K.desc(el), dx, dy, how: clamp ? "line-clamp" : cs.textOverflow === "ellipsis" ? "ellipsis" : "overflow", shown: Math.round((el.clientWidth / el.scrollWidth) * (el.clientHeight / el.scrollHeight) * 100) / 100, text: text.slice(0, 60) });
    }
    return out;
  };
  /** Visible text that lies on other visible text. */
  K.overlaps = () => {
    const boxes = [];
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n = w.nextNode(); n && boxes.length < 900; n = w.nextNode()) {
      const p = n.parentElement;
      if (!n.textContent.trim() || !p || !K.shown(p)) continue;
      const rg = document.createRange();
      rg.selectNodeContents(n);
      for (const r of rg.getClientRects()) if (r.width > 2 && r.height > 2 && r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth) boxes.push({ p, r });
    }
    const hidden = (under, top) => {
      // Something opaque between the two (a sticky bar over scrolled text) hides the lower one.
      for (let e = top; e && !e.contains(under); e = e.parentElement) {
        const c = K.rgba(getComputedStyle(e).backgroundColor);
        if (c && c.a > 0.95) return true;
      }
      return false;
    };
    const out = [];
    const seen = new Set();
    for (let i = 0; i < boxes.length; i++)
      for (let j = i + 1; j < boxes.length; j++) {
        const A = boxes[i];
        const B = boxes[j];
        if (A.p === B.p) continue;
        const ix = Math.min(A.r.right, B.r.right) - Math.max(A.r.left, B.r.left);
        const iy = Math.min(A.r.bottom, B.r.bottom) - Math.max(A.r.top, B.r.top);
        if (ix <= 2 || iy <= 2 || ix * iy < 0.25 * Math.min(A.r.width * A.r.height, B.r.width * B.r.height)) continue;
        const top = document.elementFromPoint(Math.max(A.r.left, B.r.left) + ix / 2, Math.max(A.r.top, B.r.top) + iy / 2);
        if (!top) continue;
        if (A.p.contains(top) || top.contains(A.p) ? hidden(B.p, top) : B.p.contains(top) || top.contains(B.p) ? hidden(A.p, top) : true) continue;
        const key = `${K.desc(A.p).sig}|${K.desc(B.p).sig}`;
        if (seen.has(key)) continue;
        seen.add(key);
        const box = { x: Math.min(A.r.left, B.r.left), y: Math.min(A.r.top, B.r.top), w: Math.max(A.r.right, B.r.right) - Math.min(A.r.left, B.r.left), h: Math.max(A.r.bottom, B.r.bottom) - Math.min(A.r.top, B.r.top) };
        out.push({ ...K.desc(A.p), rect: box, other: K.name(B.p).slice(0, 40), sig: key, text: A.p.textContent.trim().slice(0, 40) });
      }
    return out;
  };
  /** The page's sideways scroll, content cut off at the window's edge, and regions that scroll sideways on their own. */
  K.overflow = () => {
    const W = document.documentElement.clientWidth;
    const page = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
    const out = { width: W, page, cut: [], scrollers: [] };
    const reported = new Set();
    for (const el of document.querySelectorAll("body *")) {
      const r = el.getBoundingClientRect();
      if (r.width < 2 || r.height < 2 || r.top >= innerHeight || r.bottom <= 0) continue;
      const cs = getComputedStyle(el);
      if (/auto|scroll/.test(cs.overflowX) && el.scrollWidth > el.clientWidth + 1 && K.shown(el)) out.scrollers.push({ ...K.desc(el), over: el.scrollWidth - el.clientWidth });
      if (r.right <= W + 1 && r.left >= -1) continue;
      if (cs.visibility === "hidden" || el.closest("[inert],[aria-hidden=true],[data-state=closed]") || K.srOnly(el)) continue;
      if (!(el.innerText || "").trim() && !el.matches("img,svg,button,a,input,select,textarea,video,canvas")) continue;
      let clip = null;
      for (let a = el.parentElement; a && a !== document.documentElement; a = a.parentElement) {
        if (getComputedStyle(a).overflowX !== "visible") {
          clip = a;
          break;
        }
      }
      if (clip && /auto|scroll/.test(getComputedStyle(clip).overflowX)) continue;
      if (clip && clip !== document.body) {
        const c = clip.getBoundingClientRect();
        // Cut by its own box, not by the window: the clipping parent itself fits.
        if (c.right <= W + 1 && c.left >= -1 && r.right > c.right + 1 && clip.scrollWidth <= clip.clientWidth + 1) continue;
      }
      if ([...reported].some((p) => p.contains(el))) continue;
      reported.add(el);
      out.cut.push({ ...K.desc(el), past: Math.round(Math.max(r.right - W, -r.left)) });
    }
    return out;
  };

  // ---- Focus (WCAG 2.4.7, 2.4.11, 2.4.13) ---------------------------------------------------------------------------
  K.focusInfo = () => {
    const el = document.activeElement;
    if (!el || el === document.body || el === document.documentElement) return null;
    const cs = getComputedStyle(el);
    const before = getComputedStyle(el, "::before");
    const ringOf = (s) => (s.outlineStyle !== "none" && parseFloat(s.outlineWidth) > 0 ? { width: parseFloat(s.outlineWidth), color: s.outlineColor } : null);
    const ring = ringOf(cs) ?? ringOf(before);
    const bg = K.bg(el.parentElement ?? el).color;
    const r = el.getBoundingClientRect();
    const pts = [
      [r.x + r.width / 2, r.y + r.height / 2],
      [r.x + 3, r.y + 3],
      [r.right - 3, r.y + 3],
      [r.x + 3, r.bottom - 3],
      [r.right - 3, r.bottom - 3],
    ];
    let covered = 0;
    let by = null;
    for (const [x, y] of pts) {
      if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) {
        covered++;
        continue;
      }
      const top = document.elementFromPoint(x, y);
      if (top && !el.contains(top) && !top.contains(el)) {
        covered++;
        by ??= K.desc(top).sig;
      }
    }
    const fine = (s) => s.boxShadow !== "none" && s.boxShadow;
    return {
      ...K.desc(el),
      ring: ring ? { ...ring, ratio: K.ratio(K.painted(ring.color, el, bg) ?? bg, bg) } : null,
      shadow: fine(cs) || fine(before) || null,
      bgRatio: K.ratio(K.painted(cs.backgroundColor, el, bg) ?? bg, bg),
      obscured: covered === pts.length ? "full" : covered ? "partial" : "none",
      by,
      tabindex: el.getAttribute("tabindex"),
    };
  };
  /** Focusable elements in the window, in DOM order: the tab order a numbered overlay shows. */
  K.tabMap = () =>
    K.targetEls()
      .filter((e) => e.tabIndex >= 0)
      .map((e) => K.desc(e).rect);

  // ---- Layout shift and skeletons (CLS, NN/g skeleton screens) -------------------------------------------------------
  K.perf = { shifts: [], skeletons: [] };
  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        if (e.hadRecentInput) continue;
        const src = (e.sources || []).map((s) => (s.node?.nodeType === 1 ? s.node : s.node?.parentElement)).filter(Boolean);
        K.perf.shifts.push({ t: Math.round(e.startTime), value: e.value, sources: src.slice(0, 3).map((n) => ({ ...K.desc(n), rect: (({ x, y, width, height }) => ({ x, y, w: width, h: height }))(e.sources[0].currentRect) })) });
      }
    }).observe({ type: "layout-shift", buffered: true });
  } catch {
    // An engine without layout-shift entries: the probe reports no shifts.
  }
  const SKELETON = "[data-testid=list-skeleton],[data-skeleton],[aria-busy=true][role=status]";
  let open = null;
  K.skeletonNow = () => open;
  const tick = () => {
    const el = document.querySelector(SKELETON);
    if (el && !open) {
      const rows = [...el.querySelectorAll(":scope > div, :scope > div > div")].filter((c) => c.getBoundingClientRect().height > 4).slice(0, 3);
      open = { start: Math.round(performance.now()), kind: el.dataset.testid || (el.hasAttribute("data-skeleton") ? "kit" : "article"), path: location.pathname, rows: rows.map((c) => Math.round(c.getBoundingClientRect().height)) };
    } else if (!el && open) {
      open.end = Math.round(performance.now());
      open.after = [...document.querySelectorAll("main article[data-item-id]")].slice(0, 3).map((a) => Math.round(a.getBoundingClientRect().height));
      K.perf.skeletons.push(open);
      open = null;
    }
  };
  window.setInterval(tick, 25);

  // ---- Interface inventory (Brad Frost) -----------------------------------------------------------------------------
  K.inventory = () => {
    const px = (v) => Math.round(parseFloat(v) || 0);
    const items = [];
    const style = (el) => {
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      return {
        h: Math.round(r.height),
        w: Math.round(r.width),
        pad: [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].map(px).join(" "),
        font: `${px(cs.fontSize)}/${px(cs.lineHeight) || "n"} ${cs.fontWeight}`,
        radius: px(cs.borderTopLeftRadius),
        bg: K.hex(cs.backgroundColor),
        fg: K.hex(cs.color),
        border: parseFloat(cs.borderTopWidth) ? `${px(cs.borderTopWidth)}px ${K.hex(cs.borderTopColor)}` : "none",
        shadow: cs.boxShadow === "none" ? "none" : "shadow",
      };
    };
    const seen = new Set();
    const add = (kind, el, extra = "") => {
      if (seen.has(el) || !K.shown(el) || items.length > 500) return;
      seen.add(el);
      const s = style(el);
      const d = K.desc(el);
      const width = kind === "icon-button" || kind === "icon" ? ` w${s.w}` : "";
      items.push({ kind, ...s, name: d.name.slice(0, 40), region: d.region, rect: d.rect, sig: `h${s.h}${width} r${s.radius} ${s.font} pad ${s.pad} bg ${s.bg} fg ${s.fg} border ${s.border} ${s.shadow}${extra}` });
    };
    for (const el of document.querySelectorAll('[role^=menuitem]')) add("menu-item", el);
    for (const el of document.querySelectorAll('input:not([type=hidden]),select,textarea,[role=switch],[role=combobox],[role=slider]')) {
      const t = el.type === "checkbox" || el.type === "radio" ? (el.closest("label") ?? el) : el;
      add("input", t, ` ${el.type || el.getAttribute("role")}`);
    }
    for (const el of document.querySelectorAll('button,[role=button],a[href]')) {
      if (el.closest('[role=menu]')) continue;
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      const text = (el.innerText || "").trim();
      const painted = K.hex(cs.backgroundColor) !== "none" || parseFloat(cs.borderTopWidth) > 0;
      if (el.tagName === "A" && !painted && cs.display === "inline") continue;
      if (el.closest("[data-item-id]") && el.matches('a[href^="/i/"]')) continue;
      const pill = parseFloat(cs.borderTopLeftRadius) >= r.height / 2 - 1 && text && r.height <= 44;
      add(pill && painted ? "chip" : !text && el.querySelector("svg") ? "icon-button" : "button", el);
    }
    for (const el of document.querySelectorAll("span,div,small,strong,b")) {
      const text = (el.innerText || "").trim();
      if (!text || text.length > 14 || el.children.length > 2) continue;
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      if (r.height > 30 || r.height < 8) continue;
      if (K.hex(cs.backgroundColor) === "none" && !parseFloat(cs.borderTopWidth)) continue;
      if (parseFloat(cs.borderTopLeftRadius) < 3) continue;
      add(/^[\d,.\s]+\+?$/.test(text) ? "badge" : "chip", el);
    }
    for (const svg of document.querySelectorAll("svg")) {
      if (!K.shown(svg)) continue;
      const where = svg.closest('[role^=menuitem]') ? "menu" : svg.closest("nav") ? "nav" : svg.closest("button,a,[role=button]") ? "control" : "inline";
      const sw = svg.getAttribute("stroke-width") || getComputedStyle(svg).strokeWidth;
      const r = svg.getBoundingClientRect();
      if (items.length < 900) items.push({ kind: "icon", h: Math.round(r.height), w: Math.round(r.width), name: svg.getAttribute("class")?.match(/lucide-[\w-]+/)?.[0] ?? "", region: K.region(svg), rect: K.desc(svg).rect, sig: `${Math.round(r.width)}x${Math.round(r.height)} stroke ${sw} in ${where}` });
    }
    // Values: colours, type, spacing, radii and shadows actually painted.
    const tally = { color: {}, background: {}, border: {}, type: {}, spacing: {}, radius: {}, shadow: {} };
    const inc = (t, k) => (t[k] = (t[k] ?? 0) + 1);
    for (const el of document.querySelectorAll("body *")) {
      if (!K.shown(el)) continue;
      const cs = getComputedStyle(el);
      const ownText = [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
      if (ownText) {
        inc(tally.color, K.hex(cs.color));
        inc(tally.type, `${px(cs.fontSize)}px/${px(cs.lineHeight) || "normal"} ${cs.fontWeight} ${cs.fontFamily.split(",")[0].replace(/"/g, "")}`);
      }
      if (K.hex(cs.backgroundColor) !== "none") inc(tally.background, K.hex(cs.backgroundColor));
      if (parseFloat(cs.borderTopWidth)) inc(tally.border, `${px(cs.borderTopWidth)}px ${K.hex(cs.borderTopColor)}`);
      for (const v of [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft, cs.marginTop, cs.marginBottom, cs.rowGap, cs.columnGap]) {
        const n = px(v);
        if (n > 0) inc(tally.spacing, `${n}px`);
      }
      if (px(cs.borderTopLeftRadius)) inc(tally.radius, `${px(cs.borderTopLeftRadius)}px`);
      if (cs.boxShadow !== "none") inc(tally.shadow, cs.boxShadow.replace(/\s+/g, " ").slice(0, 90));
    }
    return { items, tally };
  };
}
