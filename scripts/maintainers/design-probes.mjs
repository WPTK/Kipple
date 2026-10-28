// Measured checks for the design review: the same surfaces design-shots.mjs pictures, measured instead of looked at.
// Each probe loads the surfaces under one condition (a device, a theme, a narrow window, forced colors, a slow network...)
// and records every element that misses a published threshold: touch targets, non-text and text contrast in every theme,
// reflow, text spacing and the largest text size, forced colors, colour-vision deficiencies, layout shift and skeletons,
// keyboard focus, the accessibility tree, right-to-left and long names. It writes hits-<probe>.json, a cropped picture of
// the first element behind each finding, report.md (findings grouped by rule, one row per kind of element) and the
// interface inventory sheets. It judges against thresholds only; docs/maintainers/design-review.md says what the review
// does with the findings. On demand, never in CI.
//
//   KIPPLE_SEED_SET=design npm run seed            (in one terminal)
//   node ../scripts/maintainers/design-probes.mjs --out ../.design/probes [--probes base,themes,...] [--only <regexp>]
//        [--themes all|a,b] [--url http://127.0.0.1:1919]
//   node ../scripts/maintainers/design-probes.mjs --out ../.design/probes --report      (rebuild report.md only)
// Run from web/. Probes that run in separate processes into one --out add up: each writes its own hits file and the
// report reads them all. The `long` probe renames a feed and a folder (and puts them back), so run it on its own.
/* global document, window, axe, getComputedStyle -- used inside page.evaluate, which runs in the browser */
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { installChecks } from "./lib/design-checks.mjs";
import { DEVICES, LAYOUT_IDS, SEED_PASSWORD, SEED_USER, chromium, designSession, loopbackOrigin, pickSurfaces, pickThemes, settle, webRequire } from "./lib/design-surfaces.mjs";

const PROBES = {
  base: "targets, non-text contrast, clipping, overlap, axe and the accessibility tree, layout shift and the inventory; paper, every device",
  themes: "text and non-text contrast in every theme (desktop and phone)",
  reflow: "320x256 (400% zoom), 280 px, landscape phones and 200% zoom on a desktop: sideways scroll, clipping, overlap",
  spacing: "the WCAG 1.4.12 text-spacing override and the largest text size: newly clipped or overlapping text",
  reading: "article measure, body size and leading at every text size, and where empty states sit",
  forced: "forced colors and prefers-contrast: icons, focus and selected states",
  vision: "every colour-vision deficiency Chromium emulates plus grayscale, and whether unread is told by colour alone",
  throttle: "slow network and a 6x slower CPU: layout shift and skeletons that flash or do not match",
  keyboard: "Tab through each screen: focus visible, not hidden, ring contrast, order",
  rtl: "right-to-left: sideways scroll, clipping and overlap",
  long: "a 100-character feed name, a long compound and CJK names (renames, then restores)",
  stuck: "opens articles again and again on a phone and times how long the loading skeleton stays",
};
const { values: opt } = parseArgs({
  options: {
    url: { type: "string", default: "http://127.0.0.1:1919" },
    out: { type: "string" },
    probes: { type: "string", default: Object.keys(PROBES).filter((p) => p !== "long").join(",") },
    only: { type: "string" },
    themes: { type: "string", default: "all" },
    report: { type: "boolean", default: false },
    user: { type: "string", default: SEED_USER },
    password: { type: "string", default: SEED_PASSWORD },
  },
});
if (!opt.out) {
  console.error(`usage: node ../scripts/maintainers/design-probes.mjs --out <dir> [--probes a,b] [--only <regexp>] [--themes all|a,b] [--report]\nprobes:\n${Object.entries(PROBES).map(([k, v]) => `  ${k.padEnd(9)} ${v}`).join("\n")}`);
  process.exit(2);
}
const out = resolve(opt.out);
for (const d of ["crops", "aria", "forced", "vision", "frames", "rtl", "keyboard", "inventory"]) mkdirSync(join(out, d), { recursive: true });
const probes = opt.probes.split(",").map((s) => s.trim());
for (const p of probes) if (!PROBES[p]) throw new Error(`unknown probe ${p} (have ${Object.keys(PROBES).join(", ")})`);
const origin = loopbackOrigin(opt.url);
const surfaces = pickSurfaces(opt.only);
const themes = pickThemes(opt.themes);
const AXE = readFileSync(webRequire.resolve("axe-core/axe.min.js"), "utf8");

// ---- Recording ---------------------------------------------------------------------------------------------------------
// A hit is one element missing one rule under one condition. The report folds hits with the same rule and `sig` (the kind
// of element) into one row, so a row that fails in 300 places is one line with its count.
const RULES = {
  "target-24": ["Target smaller than 24x24 px with no spacing exception", "WCAG 2.5.8", "https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html"],
  "target-44": ["Touch target smaller than 44x44 on a phone or tablet", "Apple HIG (44x44 pt), WCAG 2.5.5", "https://www.w3.org/WAI/WCAG22/Understanding/target-size-enhanced.html"],
  "target-gap": ["Small target less than 8 px from its neighbour", "WCAG 2.5.8 spacing, NN/g touch targets", "https://www.nngroup.com/articles/touch-target-size/"],
  "icon-contrast": ["Icon of an icon-only control under 3:1", "WCAG 1.4.11", "https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html"],
  "unread-marker": ["Unread marker under 3:1 against its row", "WCAG 1.4.11", "https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html"],
  "control-boundary": ["Input, switch or checkbox boundary under 3:1", "WCAG 1.4.11", "https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html"],
  "selected-state": ["Selected state differs from its neighbours by colour under 3:1 and nothing else", "WCAG 1.4.11, 1.4.1", "https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html"],
  "axe": ["axe-core violation", "WCAG 2.2 A/AA (axe-core rule)", "https://www.w3.org/TR/WCAG22/"],
  "text-contrast": ["Text under its contrast minimum (axe color-contrast)", "WCAG 1.4.3", "https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html"],
  "link-in-text": ["Link in running text told from the text by colour alone", "WCAG 1.4.1", "https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html"],
  "clipped": ["Text cut off by its box", "WCAG 1.4.4, 1.4.10", "https://www.w3.org/WAI/WCAG22/Understanding/reflow.html"],
  "truncated": ["Text cut by an ellipsis or line clamp to under a third of it", "WCAG 1.4.10, NN/g F-pattern (front-loaded titles)", "https://www.nngroup.com/articles/f-shaped-pattern-reading-web-content/"],
  "overlap": ["Visible text lying on other text", "WCAG 1.4.10", "https://www.w3.org/WAI/WCAG22/Understanding/reflow.html"],
  "page-scrolls-sideways": ["The page scrolls sideways", "WCAG 1.4.10", "https://www.w3.org/WAI/WCAG22/Understanding/reflow.html"],
  "cut-off": ["Content pushed past the window's edge", "WCAG 1.4.10", "https://www.w3.org/WAI/WCAG22/Understanding/reflow.html"],
  "region-scrolls-sideways": ["A region scrolls sideways (allowed for tables and code only)", "WCAG 1.4.10", "https://www.w3.org/WAI/WCAG22/Understanding/reflow.html"],
  "text-spacing": ["Clipped or overlapping only under the text-spacing override", "WCAG 1.4.12", "https://www.w3.org/WAI/WCAG22/Understanding/text-spacing.html"],
  "largest-text": ["Clipped, overlapping or sideways only at the largest text size", "WCAG 1.4.4", "https://www.w3.org/WAI/WCAG22/Understanding/resize-text.html"],
  "measure": ["Article line length outside 45-90 characters", "Practical Typography, line length", "https://practicaltypography.com/line-length.html"],
  "body-size": ["Article body text under 16 px", "Practical Typography, point size", "https://practicaltypography.com/point-size.html"],
  "leading": ["Article line spacing outside 120-160%", "Practical Typography, line spacing", "https://practicaltypography.com/line-spacing.html"],
  "empty-state-top": ["Empty or status block small at the top of a blank pane", "NN/g visual hierarchy", "https://www.nngroup.com/articles/visual-hierarchy-ux-definition/"],
  "forced-icon": ["Icon invisible in forced colors", "WCAG 1.4.11 (forced colors)", "https://developer.mozilla.org/en-US/docs/Web/CSS/@media/forced-colors"],
  "forced-selected": ["Selected state lost in forced colors", "WCAG 1.4.11 (forced colors)", "https://developer.mozilla.org/en-US/docs/Web/CSS/@media/forced-colors"],
  "forced-focus": ["No focus ring in forced colors", "WCAG 2.4.7 (forced colors)", "https://www.w3.org/WAI/WCAG22/Understanding/focus-visible.html"],
  "unread-colour-only": ["Unread told from read by colour alone", "WCAG 1.4.1", "https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html"],
  "read-text-contrast": ["Read article title under 4.5:1", "WCAG 1.4.3", "https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html"],
  "layout-shift": ["Layout shift while the screen loads (CLS over 0.1, or one shift over 0.05)", "web.dev CLS", "https://web.dev/articles/cls"],
  "skeleton-flash": ["Loading skeleton shown for under 400 ms (a flash)", "NN/g skeleton screens, Doherty threshold", "https://www.nngroup.com/articles/skeleton-screens/"],
  "skeleton-mismatch": ["Loading skeleton rows a different height from the rows that replace them", "NN/g skeleton screens", "https://www.nngroup.com/articles/skeleton-screens/"],
  "skeleton-stuck": ["Loading skeleton still showing after 10 s", "NN/g ten heuristics (visibility of system status)", "https://www.nngroup.com/articles/ten-usability-heuristics/"],
  "focus-invisible": ["Focused element shows no outline and no background change", "WCAG 2.4.7", "https://www.w3.org/WAI/WCAG22/Understanding/focus-visible.html"],
  "focus-ring-contrast": ["Focus ring under 3:1 against what is around it", "WCAG 2.4.13, 1.4.11", "https://www.w3.org/WAI/WCAG22/Understanding/focus-appearance.html"],
  "focus-obscured": ["Focused element entirely hidden under something else", "WCAG 2.4.11", "https://www.w3.org/WAI/WCAG22/Understanding/focus-not-obscured-minimum.html"],
  "focus-order": ["Tab moves back up within the same region", "WCAG 2.4.3", "https://www.w3.org/WAI/WCAG22/Understanding/focus-order.html"],
  "unnamed": ["Control with no accessible name", "WCAG 4.1.2", "https://www.w3.org/WAI/WCAG22/Understanding/name-role-value.html"],
  "duplicate-name": ["Several controls on one screen with the same name", "WCAG 2.4.6", "https://www.w3.org/WAI/WCAG22/Understanding/headings-and-labels.html"],
  "headings": ["Heading levels skip, or no or several level-1 headings", "WCAG 1.3.1", "https://www.w3.org/WAI/WCAG22/Understanding/info-and-relationships.html"],
};
const hits = [];
const missed = [];
const cropped = new Map();
const hash = (s) => createHash("sha1").update(s).digest("hex").slice(0, 12);

/** A picture of `rect` with a frame round it, once per rule and kind of element (later hits reuse the first picture). */
async function crop(page, rule, sig, rect) {
  const key = `${rule}|${sig}`;
  if (cropped.has(key)) return cropped.get(key);
  cropped.set(key, null);
  if (!rect || rect.w < 1 || rect.h < 1) return null;
  const vp = page.viewportSize();
  const pad = 24;
  const x = Math.max(0, Math.floor(rect.x - pad));
  const y = Math.max(0, Math.floor(rect.y - pad));
  const w = Math.min(vp.width - x, Math.ceil(rect.w + 2 * pad));
  const h = Math.min(vp.height - y, Math.ceil(rect.h + 2 * pad));
  if (w < 4 || h < 4) return null;
  const file = `crops/${hash(key)}.png`;
  await page.evaluate((r) => {
    const d = document.createElement("div");
    d.id = "__kp_frame";
    Object.assign(d.style, { position: "fixed", left: `${r.x - 2}px`, top: `${r.y - 2}px`, width: `${r.w + 4}px`, height: `${r.h + 4}px`, outline: "2px solid #e0118a", zIndex: 2147483647, pointerEvents: "none" });
    document.body.append(d);
  }, rect);
  await page.screenshot({ path: join(out, file), clip: { x, y, width: w, height: h } }).catch(() => {});
  await page.evaluate(() => document.getElementById("__kp_frame")?.remove());
  cropped.set(key, file);
  return file;
}
async function hit(page, where, rule, item, value, limit, extra = {}) {
  const sig = item.sig ?? `${item.region} > ${item.tag} "${item.name}"`;
  const file = page ? await crop(page, `${where.probe}:${rule}`, sig, item.rect) : null;
  hits.push({ rule, ...where, sig, name: item.name, region: item.region, value, limit, crop: file, ...extra });
}

// RULES is set above, so a report-only run starts here.
if (opt.report) {
  writeReport();
  process.exit(0);
}

// ---- Driving ---------------------------------------------------------------------------------------------------------
const browser = await chromium.launch();
const session = designSession(browser, origin, opt.user, opt.password);
let lib;
const contexts = new Map();
/** One signed-in context per device and condition, with the checks installed; the device's profile is reset to paper, default size. */
async function ctxFor(device, signedOut = false, key = "") {
  const k = `${device}|${signedOut}|${key}`;
  if (!contexts.has(k)) {
    const ctx = await session.context(device, signedOut ? "light" : undefined);
    await ctx.addInitScript(installChecks);
    if (!signedOut) {
      await session.setProfile(ctx, { "ui.theme": "paper", "client.text_size": 1, "client.peek_seen": true });
      lib ??= await session.library(ctx);
    }
    contexts.set(k, ctx);
  }
  return contexts.get(k);
}
/** Loads surface `s` on a new page of `device` and hands it to `fn`; a surface that cannot be reached is noted and skipped. */
async function onSurface(device, s, fn, { viewport, before, key, keepFocus } = {}) {
  const ctx = await ctxFor(device, !!s.signedOut, key);
  if (!s.signedOut) await session.setProfile(ctx, { "client.layout": s.layout ?? "magazine" });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e).split("\n")[0]));
  try {
    if (viewport) await page.setViewportSize(viewport);
    if (before) await before(page);
    await s.go(page, lib);
    await settle(page, keepFocus ?? s.area === "menus");
    return await fn(page);
  } catch (e) {
    missed.push({ surface: s.id, device, key, why: String(e.message).split("\n")[0], errors });
    console.error(`MISS ${s.id} ${device} ${key ?? ""}: ${String(e.message).split("\n")[0]}`);
    return null;
  } finally {
    await page.close();
  }
}
const ev = (page, fn, arg) => page.evaluate(fn, arg);
const runAxe = async (page, runOnly) => {
  if (!(await page.evaluate(() => typeof window.axe !== "undefined"))) await page.evaluate(AXE); // through the debugger: the app's CSP refuses inline scripts
  return page.evaluate(async (only) => {
    const r = await axe.run(document, { runOnly: only, resultTypes: ["violations"] });
    return r.violations.map((v) => ({
      id: v.id,
      impact: v.impact,
      help: v.help,
      nodes: v.nodes.slice(0, 30).map((n) => {
        const el = document.querySelector(n.target.flat()[0]);
        const d = el ? window.__kp.desc(el) : { rect: null, name: "", region: "" };
        const data = n.any?.[0]?.data ?? {};
        return { target: n.target.flat().join(" "), html: n.html.slice(0, 160), ...d, ratio: data.contrastRatio, expected: data.expectedContrastRatio, fg: data.fgColor, bg: data.bgColor };
      }),
    }));
  }, runOnly);
};
const AXE_ALL = { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"] };
const AXE_COLOUR = { type: "rule", values: ["color-contrast", "link-in-text-block"] };
async function recordAxe(page, where, violations) {
  for (const v of violations)
    for (const n of v.nodes) {
      const rule = v.id === "color-contrast" ? "text-contrast" : v.id === "link-in-text-block" ? "link-in-text" : "axe";
      const sig = `${v.id}: ${n.sig ?? n.target.replace(/\d+/g, "#")}`;
      await hit(page, where, rule, { ...n, sig }, n.ratio ?? v.impact, n.expected ?? null, { axe: v.id, help: v.help, html: n.html, colours: n.fg ? `${n.fg} on ${n.bg}` : undefined });
    }
}
const NONTEXT_RULE = { icon: "icon-contrast", "unread-marker": "unread-marker", "control-boundary": "control-boundary", "selected-state": "selected-state" };
async function recordNontext(page, where, items, prefix = "") {
  for (const n of items) {
    if (n.kind === "selected-state" ? n.value >= 3 || n.shape : n.value >= 3) continue;
    if (n.image) continue; // Over a picture: the probe cannot know the colour behind it.
    await hit(page, where, prefix ? `${prefix}-${n.kind === "selected-state" ? "selected" : "icon"}` : NONTEXT_RULE[n.kind], n, n.value, 3, n.peer ? { peer: n.peer } : {});
  }
}
const clipKey = (c) => `${c.sig}|${c.text ?? ""}`;
async function recordLayout(page, where, rulePrefix, { clipped, overlaps, overflow }, baseline) {
  const known = baseline ? new Set([...baseline.clipped, ...baseline.overlaps].map(clipKey)) : new Set();
  for (const c of clipped) {
    if (known.has(clipKey(c))) continue;
    // A line clamp or an ellipsis is a deliberate cut; it is a finding when it leaves under a third of the text, or only
    // appears under a condition (the baseline did not have it).
    if (!rulePrefix && c.how !== "overflow") {
      if (c.shown < 0.34) await hit(page, where, "truncated", c, `${Math.round(c.shown * 100)}% shown`, "a third", { how: c.how, text: c.text });
      continue;
    }
    await hit(page, where, rulePrefix ?? "clipped", c, `${c.dx}x${c.dy} px over`, 0, { how: c.how, text: c.text });
  }
  for (const o of overlaps) if (!known.has(clipKey(o))) await hit(page, where, rulePrefix ?? "overlap", o, "overlaps", 0, { other: o.other, text: o.text });
  if (!overflow) return;
  if (overflow.page > overflow.width + 1 && !(baseline?.overflow?.page > baseline?.overflow?.width + 1))
    await hit(page, where, rulePrefix ?? "page-scrolls-sideways", { sig: `page ${where.surface}`, name: where.surface, region: "page", rect: null }, overflow.page - overflow.width, 0);
  for (const c of overflow.cut) await hit(page, where, rulePrefix ?? "cut-off", c, `${c.past} px past the edge`, 0);
  if (!rulePrefix) for (const s of overflow.scrollers) await hit(page, where, "region-scrolls-sideways", s, `${s.over} px`, 0);
}
const layoutNow = (page) => ev(page, () => ({ clipped: window.__kp.clipped(), overlaps: window.__kp.overlaps(), overflow: window.__kp.overflow() }));
const MOBILE = (d) => DEVICES[d]?.mobile;
const signedIn = surfaces.filter((s) => !s.signedOut);
const pick = (ids) => surfaces.filter((s) => ids.includes(s.id));

// ---- Probes ----------------------------------------------------------------------------------------------------------
const inventory = { items: [], tally: {} };
const run = {
  async base() {
    for (const device of ["phone", "tablet", "desktop"])
      for (const s of surfaces)
        await onSurface(device, s, async (page) => {
          const where = { probe: "base", surface: s.id, device, theme: "paper" };
          const t = await ev(page, (min) => window.__kp.targets(min), MOBILE(device) ? 44 : 24);
          for (const x of t.small) {
            if (x.w < 24 || x.h < 24) {
              if (!x.spaced) await hit(page, where, "target-24", x, `${x.w}x${x.h}`, 24);
            } else await hit(page, where, "target-44", x, `${x.w}x${x.h}`, 44);
          }
          for (const x of t.close) await hit(page, where, "target-gap", x, x.gap, 8, { other: x.other });
          await recordNontext(page, where, await ev(page, () => window.__kp.nontext()));
          await recordLayout(page, where, null, await layoutNow(page));
          await recordAxe(page, where, await runAxe(page, AXE_ALL));
          await ariaChecks(page, where);
          const perf = await ev(page, () => window.__kp.perf);
          await recordShifts(page, where, perf);
          const cues = await ev(page, () => window.__kp.unreadCues());
          if (cues) await recordCues(page, where, cues);
          if (device !== "tablet") {
            const inv = await ev(page, () => window.__kp.inventory());
            for (const it of inv.items) {
              const k = `${it.kind}|${it.sig}`;
              const have = inventory.items.filter((x) => `${x.kind}|${x.sig}` === k).length;
              it.crop = have < 2 ? await crop(page, `inv${have}`, k, it.rect) : null;
              inventory.items.push({ ...it, surface: s.id, device });
            }
            for (const [t2, counts] of Object.entries(inv.tally)) for (const [v, n] of Object.entries(counts)) ((inventory.tally[t2] ??= {})[v] = (inventory.tally[t2][v] ?? 0) + n);
          }
          console.log(`base ${device} ${s.id}`);
        });
    writeFileSync(join(out, "inventory", "inventory.json"), JSON.stringify(inventory, null, 1));
    await inventorySheets();
  },
  async themes() {
    const desk = pick(["list-magazine", "list-cards", "list-compact", "list-inbox", "list-headlines", "list-gazette", "article-long", "menu-row", "menu-article-more", "menu-list-options", "nav-feeds", "state-feed-error", "settings-appearance", "stats", "health", "nav-search-results"]);
    const phone = pick(["list-magazine", "article-picture", "menu-row", "nav-feeds", "settings-appearance"]);
    for (const [device, list] of [["desktop", desk], ["phone", phone]]) {
      const ctx = await ctxFor(device);
      for (const theme of themes) {
        await session.setProfile(ctx, { "ui.theme": theme });
        for (const s of list)
          await onSurface(device, s, async (page) => {
            const where = { probe: "themes", surface: s.id, device, theme };
            await recordAxe(page, where, await runAxe(page, AXE_COLOUR));
            await recordNontext(page, where, await ev(page, () => window.__kp.nontext()));
            const cues = await ev(page, () => window.__kp.unreadCues());
            if (cues) await recordCues(page, where, cues);
          });
        console.log(`themes ${device} ${theme}`);
      }
      await session.setProfile(ctx, { "ui.theme": "paper" });
    }
  },
  async reflow() {
    const sizes = [
      ["phone", { width: 320, height: 256 }, "320x256 (400% of 1280x1024)"],
      ["phone", { width: 280, height: 653 }, "280x653"],
      ["phone", { width: 844, height: 390 }, "844x390 landscape"],
      ["phone", { width: 667, height: 375 }, "667x375 landscape"],
      ["desktop", { width: 720, height: 450 }, "720x450 (200% of 1440x900)"],
    ];
    for (const [device, viewport, label] of sizes)
      for (const s of surfaces)
        await onSurface(device, s, async (page) => {
          await recordLayout(page, { probe: "reflow", surface: s.id, device, theme: "paper", viewport: label }, null, await layoutNow(page));
          console.log(`reflow ${label} ${s.id}`);
        }, { viewport });
  },
  async spacing() {
    const CSS = "*{line-height:1.5!important;letter-spacing:0.12em!important;word-spacing:0.16em!important}p{margin-bottom:2em!important}";
    for (const device of ["phone", "desktop"]) {
      const base = new Map();
      for (const s of surfaces)
        await onSurface(device, s, async (page) => {
          const a = await layoutNow(page);
          base.set(s.id, a);
          await page.addStyleTag({ content: CSS });
          await page.waitForTimeout(250);
          await recordLayout(page, { probe: "spacing", surface: s.id, device, theme: "paper", viewport: "text-spacing override" }, "text-spacing", await layoutNow(page), a);
          console.log(`spacing ${device} ${s.id}`);
        });
      const ctx = await ctxFor(device);
      await session.setProfile(ctx, { "client.text_size": 1.5 });
      for (const s of signedIn)
        await onSurface(device, s, async (page) => {
          await recordLayout(page, { probe: "spacing", surface: s.id, device, theme: "paper", viewport: "largest text (1.5)" }, "largest-text", await layoutNow(page), base.get(s.id));
        });
      await session.setProfile(ctx, { "client.text_size": 1 });
    }
  },
  async reading() {
    const articles = pick(["article-picture", "article-long", "article-unicode"]);
    const states = pick(["state-empty-feed", "state-caught-up", "state-feed-error", "state-not-found", "state-muted", "nav-search-empty"]);
    for (const device of ["phone", "tablet", "desktop"]) {
      const ctx = await ctxFor(device);
      for (const size of [0.875, 1, 1.125, 1.25, 1.5]) {
        await session.setProfile(ctx, { "client.text_size": size });
        for (const s of articles)
          await onSurface(device, s, async (page) => {
            await page.waitForSelector(".article-body p", { timeout: 10000 }).catch(() => {});
            const m = await ev(page, () => {
              // The widest paragraph that wraps, so its width is the column's line length.
              const wraps = (x) => x.getBoundingClientRect().height >= 1.8 * parseFloat(getComputedStyle(x).lineHeight);
              const p = [...document.querySelectorAll(".article-body p")].filter(wraps).sort((a, b) => b.getBoundingClientRect().width - a.getBoundingClientRect().width)[0];
              if (!p) return null;
              const cs = getComputedStyle(p);
              const c = document.createElement("canvas").getContext("2d");
              c.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
              const sample = "the quick brown fox jumps over the lazy dog and keeps reading the article text";
              const avg = c.measureText(sample).width / sample.length + (parseFloat(cs.letterSpacing) || 0) || 1;
              const r = p.getBoundingClientRect();
              return { width: r.width, font: parseFloat(cs.fontSize), leading: parseFloat(cs.lineHeight) / parseFloat(cs.fontSize), chars: Math.round(r.width / avg), rect: { x: r.x, y: r.y, w: r.width, h: Math.min(r.height, 120) }, family: cs.fontFamily.split(",")[0] };
            });
            if (!m) return;
            const where = { probe: "reading", surface: s.id, device, theme: "paper", viewport: `text ${size}` };
            const item = { sig: `article body at text ${size}`, name: `${m.family} ${m.font}px, ${m.chars} chars, ${Math.round(m.width)} px column`, region: "main", rect: m.rect };
            hits.push({ rule: "metric-measure", ...where, sig: item.sig, name: item.name, value: m.chars, font: m.font, leading: Math.round(m.leading * 100) / 100, width: Math.round(m.width) });
            if (s.id === "article-unicode") return; // CJK sets its own measure; characters are not Latin letters.
            if (m.chars < 45 || m.chars > 90) await hit(page, where, "measure", { ...item, sig: `article body, ${device}` }, m.chars, "45-90", { font: m.font });
            if (m.font < 16) await hit(page, where, "body-size", { ...item, sig: `article body, ${device}` }, m.font, 16);
            if (m.leading < 1.2 || m.leading > 1.6) await hit(page, where, "leading", { ...item, sig: `article body, ${device}` }, Math.round(m.leading * 100) / 100, "1.2-1.6");
          });
      }
      await session.setProfile(ctx, { "client.text_size": 1 });
      for (const s of states)
        await onSurface(device, s, async (page) => {
          const b = await ev(page, () => {
            const main = document.querySelector("main");
            const block = [...document.querySelectorAll("main [role=status], main [role=alert]")].find((e) => e.querySelector("h1,h2") && window.__kp.shown(e));
            if (!main || !block) return null;
            const m = main.getBoundingClientRect();
            const r = block.getBoundingClientRect();
            const h = block.querySelector("h1,h2");
            return { top: (r.top - m.top) / m.height, share: (r.width * r.height) / (m.width * m.height), title: parseFloat(getComputedStyle(h).fontSize), rect: { x: r.x, y: r.y, w: r.width, h: r.height }, name: h.innerText, main: { w: m.width, h: m.height } };
          });
          if (b && b.top < 0.25 && b.share < 0.12 && b.main.h > 500)
            await hit(page, { probe: "reading", surface: s.id, device, theme: "paper" }, "empty-state-top", { sig: `status block, ${device}`, name: b.name, region: "main", rect: b.rect }, `top at ${Math.round(b.top * 100)}% of the pane, ${Math.round(b.share * 100)}% of its area, title ${b.title}px`, "centred");
        });
    }
  },
  async forced() {
    for (const device of ["phone", "desktop"])
      for (const s of surfaces) {
        await onSurface(device, s, async (page) => {
          const where = { probe: "forced", surface: s.id, device, theme: "paper", viewport: "forced-colors: active" };
          await page.screenshot({ path: join(out, "forced", `${s.id}__${device}.png`) });
          await recordNontext(page, where, await ev(page, () => window.__kp.nontext()), "forced");
          await tabWalk(page, where, 15, "forced-focus");
        }, { before: (p) => p.emulateMedia({ forcedColors: "active" }), key: "forced" });
        if (device === "desktop")
          await onSurface(device, s, async (page) => {
            const where = { probe: "forced", surface: s.id, device, theme: "paper", viewport: "prefers-contrast: more" };
            await recordAxe(page, where, await runAxe(page, AXE_COLOUR));
            await recordNontext(page, where, await ev(page, () => window.__kp.nontext()));
          }, { before: (p) => p.emulateMedia({ contrast: "more" }), key: "more" });
        console.log(`forced ${device} ${s.id}`);
      }
  },
  async vision() {
    const TYPES = ["none", "achromatopsia", "protanopia", "deuteranopia", "tritanopia", "blurredVision", "reducedContrast"];
    const list = pick(["list-magazine", "list-compact", "list-headlines", "list-gazette", "list-pictures", "state-feed-error", "article-long", "nav-feeds", "settings-appearance", "stats", "health", "menu-list-options"]);
    for (const device of ["phone", "desktop"]) {
      const ctx = await ctxFor(device);
      for (const theme of ["paper", "midnight"]) {
        await session.setProfile(ctx, { "ui.theme": theme });
        for (const s of list)
          await onSurface(device, s, async (page) => {
            const cdp = await page.context().newCDPSession(page);
            const files = [];
            for (const type of TYPES) {
              await cdp.send("Emulation.setEmulatedVisionDeficiency", { type });
              await page.waitForTimeout(150);
              const f = `vision/${s.id}__${device}__${theme}__${type}.png`;
              await page.screenshot({ path: join(out, f) });
              files.push([type, f]);
            }
            await cdp.send("Emulation.setEmulatedVisionDeficiency", { type: "none" });
            visionSheets.push({ id: `${s.id}__${device}__${theme}`, files, device });
          });
        // Unread against read in each layout, on a list that has both.
        for (const layout of LAYOUT_IDS) {
          const s = { id: `all-${layout}`, area: "lists", layout, go: async (page) => (await page.goto(`/l/all?feed=${lib.feed("The Morning Ledger")}`), page.locator("main article[data-item-id]").first().waitFor({ timeout: 20000 })) };
          await onSurface(device, s, async (page) => {
            const cues = await ev(page, () => window.__kp.unreadCues());
            if (cues) await recordCues(page, { probe: "vision", surface: `list-${layout} (All)`, device, theme }, cues);
          });
        }
      }
      await session.setProfile(ctx, { "ui.theme": "paper" });
    }
    await sheetOf("vision", visionSheets.map((v) => ({ title: v.id, cells: v.files.map(([t, f]) => ({ src: f, caption: t })), width: v.device === "phone" ? 200 : 360 })), "vision/sheet.png");
  },
  async throttle() {
    const list = pick(["list-magazine", "list-cards", "list-compact", "list-inbox", "list-headlines", "list-gazette", "article-picture", "article-long", "nav-feeds", "stats", "settings-appearance", "health"]);
    for (const device of ["phone", "desktop"])
      for (const s of list) {
        const name = `${s.id}__${device}`;
        let frames = [];
        let stop = false;
        let loop;
        await onSurface(device, s, async (page) => {
          stop = true;
          await loop;
          await page.waitForTimeout(500);
          const perf = await ev(page, () => ({ ...window.__kp.perf, open: window.__kp.skeletonNow() }));
          const where = { probe: "throttle", surface: s.id, device, theme: "paper", viewport: "slow network, 6x CPU" };
          await recordShifts(page, where, perf);
          for (const k of perf.skeletons) {
            const shown = k.end - k.start;
            const item = { sig: `${k.kind} skeleton on ${s.area}`, name: `${k.kind} on ${k.path}`, region: "main", rect: null };
            if (shown < 400) await hit(null, where, "skeleton-flash", item, `${shown} ms`, 400, { frames });
            const sk = k.rows[0];
            const real = k.after?.[0];
            if (sk && real && (real / sk < 0.8 || real / sk > 1.25)) await hit(null, where, "skeleton-mismatch", item, `skeleton row ${sk} px, real row ${real} px`, "within 20%", { frames, rows: k.rows, after: k.after });
          }
          if (perf.open) await hit(null, where, "skeleton-stuck", { sig: `${perf.open.kind} skeleton`, name: perf.open.path, region: "main" }, "still showing", 0, { frames });
          console.log(`throttle ${name}: ${perf.skeletons.length} skeletons, ${perf.shifts.length} shifts`);
        }, {
          key: "throttle",
          before: async (page) => {
            const cdp = await page.context().newCDPSession(page);
            await cdp.send("Network.enable");
            await cdp.send("Network.emulateNetworkConditions", { offline: false, latency: 300, downloadThroughput: 200000, uploadThroughput: 90000 });
            await cdp.send("Emulation.setCPUThrottlingRate", { rate: 6 });
            frames = [];
            loop = (async () => {
              for (let i = 0; i < 24 && !stop; i++) {
                const f = `frames/${name}__${String(i).padStart(2, "0")}.png`;
                if (await page.screenshot({ path: join(out, f), timeout: 4000 }).then(() => true, () => false)) frames.push(f);
                await page.waitForTimeout(150).catch(() => {});
              }
            })();
          },
        });
        stop = true;
        await loop;
      }
  },
  async keyboard() {
    for (const device of ["desktop", "tablet"])
      for (const s of surfaces)
        await onSurface(device, s, async (page) => {
          const where = { probe: "keyboard", surface: s.id, device, theme: "paper" };
          // The tab order as an overlay: every focusable element in the window, numbered in DOM order.
          const map = await ev(page, () => window.__kp.tabMap());
          await page.evaluate((rects) => {
            rects.slice(0, 60).forEach((r, i) => {
              const d = document.createElement("div");
              d.className = "__kp_num";
              d.textContent = String(i + 1);
              Object.assign(d.style, { position: "fixed", left: `${r.x}px`, top: `${r.y}px`, font: "bold 11px system-ui", background: "#e0118a", color: "#fff", padding: "0 3px", borderRadius: "3px", zIndex: 2147483647, pointerEvents: "none" });
              document.body.append(d);
            });
          }, map);
          await page.screenshot({ path: join(out, "keyboard", `${s.id}__${device}.png`) });
          await page.evaluate(() => document.querySelectorAll(".__kp_num").forEach((d) => d.remove()));
          const stops = await tabWalk(page, where, 60);
          writeFileSync(join(out, "keyboard", `${s.id}__${device}.json`), JSON.stringify(stops, null, 1));
          console.log(`keyboard ${device} ${s.id}: ${stops.length} stops`);
        }, { keepFocus: false });
  },
  async rtl() {
    for (const device of ["phone", "desktop"])
      for (const s of surfaces)
        await onSurface(device, s, async (page) => {
          const a = await layoutNow(page);
          await page.evaluate(() => (document.documentElement.dir = "rtl"));
          await page.waitForTimeout(300);
          await page.screenshot({ path: join(out, "rtl", `${s.id}__${device}.png`) });
          await recordLayout(page, { probe: "rtl", surface: s.id, device, theme: "paper", viewport: "dir=rtl" }, null, await layoutNow(page), a);
          console.log(`rtl ${device} ${s.id}`);
        });
  },
  async long() {
    const ctx = await ctxFor("desktop");
    const WRITE = { "X-Kipple-Client": "web", Origin: origin };
    const boot = await (await ctx.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
    const feedA = boot.feeds.find((f) => f.title.includes("Morning Ledger"));
    const feedB = boot.feeds.find((f) => f.title.includes("Long Essays"));
    const folder = boot.folders.find((f) => f.name.includes("News"));
    const names = {
      a: "The Extraordinarily Long Name of a Regional Newspaper That Publishes Morning, Evening and Weekend Editions",
      b: "Donaudampfschifffahrtsgesellschaftskapitänswitwenrentenversicherungsanstaltsbericht",
      f: "日本語のニュースとテクノロジーの最新情報まとめ一覧表示用フォルダー名前",
    };
    const patch = async (path, body) => {
      const r = await ctx.request.patch(path, { headers: WRITE, data: body });
      if (!r.ok()) throw new Error(`PATCH ${path}: ${r.status()} ${await r.text()}`);
    };
    try {
      await patch(`/api/feeds/${feedA.id}`, { custom_title: names.a });
      await patch(`/api/feeds/${feedB.id}`, { custom_title: names.b });
      await patch(`/api/folders/${folder.id}`, { name: names.f });
      const list = pick(["list-magazine", "nav-feeds", "article-picture", "article-long", "health", "list-folder", "stats", "menu-article-more"]).map((s) =>
        s.id === "list-folder" ? { ...s, go: async (page) => (await page.goto(`/l/unread?folder=${folder.id}`), page.locator("main").first().waitFor()) } : s,
      );
      for (const device of ["phone", "desktop"])
        for (const s of list)
          await onSurface(device, s, async (page) => {
            await page.screenshot({ path: join(out, "rtl", `long-${s.id}__${device}.png`) });
            await recordLayout(page, { probe: "long", surface: s.id, device, theme: "paper", viewport: "long names" }, null, await layoutNow(page));
          });
    } finally {
      await patch(`/api/feeds/${feedA.id}`, { custom_title: feedA.custom_title ?? null }).catch((e) => console.error(String(e)));
      await patch(`/api/feeds/${feedB.id}`, { custom_title: feedB.custom_title ?? null }).catch((e) => console.error(String(e)));
      await patch(`/api/folders/${folder.id}`, { name: folder.name }).catch((e) => console.error(String(e)));
    }
  },
  async stuck() {
    const ctx = await ctxFor("phone");
    const feeds = ["The Morning Ledger", "Long Essays", "Ünïcödé", "Example Site"];
    const times = [];
    for (let round = 0; round < 24; round++) {
      const page = await ctx.newPage();
      const pending = new Map();
      page.on("request", (r) => pending.set(r, Date.now()));
      page.on("requestfinished", (r) => pending.delete(r));
      page.on("requestfailed", (r) => pending.delete(r));
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e).split("\n")[0]));
      try {
        // Every other pair of rounds changes the device's layout first, as the captures do between surfaces.
        if (round % 4 >= 2) await session.setProfile(ctx, { "client.layout": round % 8 < 4 ? "cards" : "magazine" });
        await page.goto(`/l/all?feed=${lib.feed(feeds[round % feeds.length])}`);
        const rows = page.locator('main article[data-item-id] a[href^="/i/"]');
        await rows.first().waitFor({ timeout: 20000 });
        // Alternate a cold open with an open straight after going back (the capture's pattern), and the nth row.
        await rows.nth(round % 3).click();
        if (round % 2) {
          await page.goBack();
          await rows.nth((round + 1) % 3).click();
        }
        const t0 = Date.now();
        const ok = await page
          .waitForFunction(() => document.querySelector(".article-body") && !document.querySelector("main [aria-busy=true][role=status]"), null, { timeout: 10000 })
          .then(() => true, () => false);
        const ms = Date.now() - t0;
        times.push({ round, ms, ok, url: page.url() });
        if (!ok) {
          const f = `frames/stuck-${round}.png`;
          await page.screenshot({ path: join(out, f) });
          const waiting = [...pending].filter(([r]) => !r.url().endsWith("/api/events")).map(([r, t]) => `${r.method()} ${new URL(r.url()).pathname} (${Math.round((Date.now() - t) / 1000)} s)`);
          await hit(null, { probe: "stuck", surface: "article from a list", device: "phone", theme: "paper" }, "skeleton-stuck", { sig: "article skeleton after opening a row", name: page.url(), region: "main" }, `${ms} ms`, 10000, { crop: f, waiting, errors, round });
        }
      } finally {
        await page.close();
      }
    }
    writeFileSync(join(out, "stuck.json"), JSON.stringify(times, null, 1));
    console.log(`stuck: ${times.filter((t) => !t.ok).length} of ${times.length} stayed on the skeleton; slowest ${Math.max(...times.map((t) => t.ms))} ms`);
  },
};
const visionSheets = [];

async function recordShifts(page, where, perf) {
  const cls = perf.shifts.reduce((n, s) => n + s.value, 0);
  for (const s of perf.shifts) {
    if (s.value < 0.05 && cls <= 0.1) continue;
    const src = s.sources[0] ?? { sig: "unknown", name: "", region: "", rect: null };
    await hit(page, where, "layout-shift", { ...src, sig: `${src.sig}` }, Math.round(s.value * 1000) / 1000, 0.05, { cls: Math.round(cls * 1000) / 1000, at: s.t });
  }
}
async function recordCues(page, where, { cues, readText, readDesc, unreadDesc }) {
  if (!cues.marker && !cues.weight && !cues.style && !cues.text) await hit(page, where, "unread-colour-only", { ...unreadDesc, sig: `unread title, ${where.surface}` }, `colour ratio ${cues.colorRatio}`, "a second cue");
  if (readText < 4.5) await hit(page, where, "read-text-contrast", { ...readDesc, sig: `read title, ${where.surface.replace(/ \(All\)$/, "")}` }, readText, 4.5);
}
/** Presses Tab up to `max` times, checking each stop; returns the stops. `rule` names the only rule to check (forced colors). */
async function tabWalk(page, where, max, rule) {
  await page.evaluate(() => document.activeElement?.blur?.());
  const stops = [];
  for (let i = 0; i < max; i++) {
    await page.keyboard.press("Tab");
    await page.waitForTimeout(40);
    const f = await ev(page, () => window.__kp.focusInfo());
    if (!f) break;
    if (stops.length && f.sig === stops[0].sig && Math.abs(f.rect.y - stops[0].rect.y) < 2) break;
    stops.push(f);
    const visible = f.ring || f.shadow || f.bgRatio >= 1.3;
    if (rule) {
      if (!f.ring && !f.shadow) await hit(page, where, rule, f, "no outline", "an outline");
      continue;
    }
    if (!visible) await hit(page, where, "focus-invisible", f, `background ${f.bgRatio}:1`, "an outline or 3:1");
    else if (f.ring && f.ring.ratio < 3 && !f.disabled) await hit(page, where, "focus-ring-contrast", f, f.ring.ratio, 3, { width: f.ring.width });
    else if (!f.ring && f.bgRatio < 3) await hit(page, where, "focus-ring-contrast", { ...f, sig: `${f.sig} (background only)` }, f.bgRatio, 3);
    if (f.obscured === "full") await hit(page, where, "focus-obscured", f, `under ${f.by}`, "partly visible");
    const prev = stops.at(-2);
    if (prev && prev.region === f.region && f.docY + f.rect.h < prev.docY - 4 && f.obscured === "none") await hit(page, where, "focus-order", { ...f, sig: `${prev.sig} -> ${f.sig}` }, `up ${Math.round(prev.docY - f.docY)} px after "${prev.name}"`, "down or right");
  }
  return stops;
}
async function ariaChecks(page, where) {
  const yaml = await page.locator("body").ariaSnapshot();
  writeFileSync(join(out, "aria", `${where.surface}__${where.device}.yml`), yaml);
  const INTERACTIVE = new Set(["button", "link", "menuitem", "menuitemradio", "menuitemcheckbox", "checkbox", "switch", "tab", "textbox", "searchbox", "combobox", "slider", "radio", "spinbutton", "option", "treeitem"]);
  const heads = [];
  const names = new Map();
  for (const l of yaml.split("\n")) {
    const m = l.match(/^\s*- (\w+)(?: "((?:[^"\\]|\\.)*)")?(.*)$/);
    if (!m) continue;
    const [, role, name, rest] = m;
    if (INTERACTIVE.has(role) && !name && !/^\s*:\s*\S/.test(rest)) await hit(null, where, "unnamed", { sig: `${role} with no name`, name: l.trim(), region: "" }, "no name", "a name");
    if (role === "heading") heads.push({ name, level: Number(rest.match(/level=(\d)/)?.[1] ?? 0) });
    if (name && (role === "button" || role === "link")) names.set(`${role} "${name}"`, (names.get(`${role} "${name}"`) ?? 0) + 1);
  }
  const dups = [...names].filter(([, n]) => n > 1);
  if (dups.length) await hit(null, where, "duplicate-name", { sig: `same names on ${where.surface}`, name: dups.map(([k, n]) => `${k} x${n}`).join("; ").slice(0, 400), region: "" }, dups.length, 0);
  const h1 = heads.filter((h) => h.level === 1).length;
  if (h1 !== 1) await hit(null, where, "headings", { sig: `${h1} level-1 headings`, name: heads.map((h) => `h${h.level} ${h.name}`).join(" / ").slice(0, 200), region: "" }, h1, 1);
  for (let i = 1; i < heads.length; i++)
    if (heads[i].level > heads[i - 1].level + 1) await hit(null, where, "headings", { sig: `h${heads[i - 1].level} then h${heads[i].level}`, name: `${heads[i - 1].name} -> ${heads[i].name}`, region: "" }, `h${heads[i - 1].level} -> h${heads[i].level}`, "one step");
}

/** An HTML sheet of pictures in rows, saved as a PNG beside the other output. */
async function sheetOf(name, rows, png) {
  const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
  const html = `<!doctype html><meta charset="utf-8"><style>body{font:13px system-ui;margin:12px;background:#eee;color:#111}h3{margin:14px 0 4px;font-size:13px}.row{display:flex;flex-wrap:wrap;gap:8px;align-items:flex-start}figure{margin:0}img{display:block;border:1px solid #bbb;background:#fff}figcaption{font-size:11px;color:#444}table{border-collapse:collapse;margin:6px 0}td,th{border:1px solid #ccc;padding:2px 6px;font-size:12px;text-align:left;vertical-align:top}.sw{display:inline-block;width:14px;height:14px;border:1px solid #888;vertical-align:middle;margin-right:4px}</style>${rows
    .map((r) => `<h3>${esc(r.title)}</h3>${r.html ?? ""}<div class="row">${(r.cells ?? []).map((c) => `<figure><img src="../${esc(c.src)}" style="max-width:${r.width ?? 360}px"><figcaption>${esc(c.caption)}</figcaption></figure>`).join("")}</div>`)
    .join("")}`;
  const dir = join(out, png.split("/")[0]);
  const file = join(dir, `${name}.html`);
  // Pictures are named relative to --out and the sheet lives one folder down, hence the "../".
  writeFileSync(file, html);
  const page = await browser.newPage({ viewport: { width: 1600, height: 900 } });
  try {
    await page.goto(pathToFileURL(file).href);
    await page.evaluate(() => Promise.all([...document.images].map((i) => i.decode().catch(() => {}))));
    await page.screenshot({ path: join(out, png), fullPage: true });
  } finally {
    await page.close();
  }
}

/** One sheet per kind of control, grouped by height so near-duplicates sit together, plus the value tallies. */
async function inventorySheets() {
  const kinds = [...new Set(inventory.items.map((i) => i.kind))];
  for (const kind of kinds) {
    const groups = new Map();
    for (const it of inventory.items.filter((i) => i.kind === kind)) {
      const g = groups.get(it.sig) ?? { sig: it.sig, h: it.h, n: 0, surfaces: new Set(), crops: [], names: new Set() };
      g.n++;
      g.surfaces.add(`${it.surface} (${it.device})`);
      if (it.crop && g.crops.length < 2) g.crops.push(it.crop);
      if (it.name) g.names.add(it.name);
      groups.set(it.sig, g);
    }
    const sorted = [...groups.values()].sort((a, b) => a.h - b.h || b.n - a.n);
    await sheetOf(`sheet-${kind}`, [
      { title: `${kind}: ${sorted.length} distinct looks across ${inventory.items.filter((i) => i.kind === kind).length} rendered`, html: "" },
      ...sorted.map((g) => ({
        title: `${g.sig}  ×${g.n}`,
        html: `<div style="font-size:11px;color:#555">${[...g.names].slice(0, 6).map((n) => n.replace(/</g, "&lt;")).join(" · ")} — on ${[...g.surfaces].slice(0, 5).join(", ")}${g.surfaces.size > 5 ? ` and ${g.surfaces.size - 5} more` : ""}</div>`,
        cells: g.crops.map((c) => ({ src: c, caption: "" })),
        width: 420,
      })),
    ], `inventory/sheet-${kind}.png`);
  }
  const t = inventory.tally;
  const table = (obj, swatch) =>
    `<table>${Object.entries(obj ?? {})
      .sort((a, b) => b[1] - a[1])
      .slice(0, 80)
      .map(([k, n]) => `<tr><td>${swatch ? `<span class="sw" style="background:${k.split(" ").at(-1).split("/")[0]}"></span>` : ""}${k.replace(/</g, "&lt;")}</td><td>${n}</td></tr>`)
      .join("")}</table>`;
  await sheetOf("sheet-values", [
    { title: "Text colours (paper)", html: table(t.color, true) },
    { title: "Background colours (paper)", html: table(t.background, true) },
    { title: "Borders", html: table(t.border, true) },
    { title: "Type: size/leading weight family", html: table(t.type) },
    { title: "Spacing values (padding, margin, gap)", html: table(t.spacing) },
    { title: "Corner radii", html: table(t.radius) },
    { title: "Shadows", html: table(t.shadow) },
  ], "inventory/sheet-values.png");
}

// ---- Report ----------------------------------------------------------------------------------------------------------
function writeReport() {
  const all = readdirSync(out)
    .filter((f) => /^hits-.*\.json$/.test(f))
    .flatMap((f) => JSON.parse(readFileSync(join(out, f), "utf8")));
  const miss = existsSync(join(out, "missed.json")) ? JSON.parse(readFileSync(join(out, "missed.json"), "utf8")) : [];
  writeFileSync(join(out, "findings.json"), JSON.stringify(all, null, 1));
  const findings = all.filter((h) => RULES[h.rule]);
  const byRule = new Map();
  for (const h of findings) {
    const rows = byRule.get(h.rule) ?? new Map();
    const r = rows.get(h.sig) ?? { sig: h.sig, name: h.name, n: 0, surfaces: new Set(), devices: new Set(), themes: new Set(), conditions: new Set(), values: [], crop: null, extra: h };
    r.n++;
    r.surfaces.add(h.surface);
    r.devices.add(h.device);
    if (h.theme) r.themes.add(h.theme);
    if (h.viewport) r.conditions.add(h.viewport);
    r.values.push(h.value);
    r.crop ??= h.crop;
    rows.set(h.sig, r);
    byRule.set(h.rule, rows);
  }
  const cell = (s) => String(s ?? "").replace(/[\\|]/g, "\\$&").replace(/\n/g, " ");
  const list = (set, n = 4) => [...set].slice(0, n).join(", ") + (set.size > n ? ` +${set.size - n}` : "");
  const worst = (vals) => {
    const nums = vals.filter((v) => typeof v === "number");
    return nums.length ? Math.min(...nums) : vals[0];
  };
  const lines = [
    "# Design probes",
    "",
    `${findings.length} hits in ${[...byRule.values()].reduce((n, r) => n + r.size, 0)} findings (one per rule and kind of element). Generated by scripts/maintainers/design-probes.mjs.`,
    "",
    "| Rule | Findings | Hits | Source |",
    "|---|---|---|---|",
    ...Object.keys(RULES)
      .filter((k) => byRule.has(k))
      .map((k) => `| ${RULES[k][0]} | ${byRule.get(k).size} | ${[...byRule.get(k).values()].reduce((n, r) => n + r.n, 0)} | [${RULES[k][1]}](${RULES[k][2]}) |`),
    "",
  ];
  for (const k of Object.keys(RULES).filter((x) => byRule.has(x))) {
    const rows = [...byRule.get(k).values()].sort((a, b) => b.n - a.n);
    lines.push(`## ${RULES[k][0]}`, "", `${RULES[k][1]}: ${RULES[k][2]}`, "", "| Element | Measured | Hits | Where | Picture |", "|---|---|---|---|---|");
    for (const r of rows.slice(0, 60)) {
      const where = [list(r.surfaces), list(r.devices), r.themes.size > 1 ? `${r.themes.size} themes` : list(r.themes), list(r.conditions, 2)].filter(Boolean).join("; ");
      const extra = r.extra.colours ?? r.extra.text ?? r.extra.other ?? r.extra.help ?? "";
      lines.push(`| ${cell(r.sig)}${r.name && !r.sig.includes(r.name) ? `<br>${cell(r.name)}` : ""}${extra ? `<br><small>${cell(extra)}</small>` : ""} | ${cell(worst(r.values))} (limit ${cell(r.extra.limit)}) | ${r.n} | ${cell(where)} | ${r.crop ? `![](${r.crop})` : ""} |`);
    }
    if (rows.length > 60) lines.push("", `… and ${rows.length - 60} more in findings.json.`);
    lines.push("");
  }
  const metrics = all.filter((h) => h.rule === "metric-measure");
  if (metrics.length) {
    lines.push("## Article measure by device and text size", "", "| Surface | Device | Text size | Font px | Characters per line | Leading | Column px |", "|---|---|---|---|---|---|---|");
    for (const m of metrics) lines.push(`| ${m.surface} | ${m.device} | ${m.viewport} | ${m.font} | ${m.value} | ${m.leading} | ${m.width} |`);
    lines.push("");
  }
  if (miss.length) lines.push("## Not reached", "", ...miss.map((m) => `- ${m.surface} ${m.device} ${m.key ?? ""}: ${m.why}`), "");
  writeFileSync(join(out, "report.md"), lines.join("\n"));
  console.log(`report.md: ${findings.length} hits, ${[...byRule.values()].reduce((n, r) => n + r.size, 0)} findings`);
}

try {
  for (const p of probes) {
    const t0 = Date.now();
    hits.length = 0;
    await run[p]();
    writeFileSync(join(out, `hits-${p}.json`), JSON.stringify(hits, null, 1));
    console.log(`== ${p}: ${hits.length} hits in ${Math.round((Date.now() - t0) / 1000)} s`);
  }
  const prior = existsSync(join(out, "missed.json")) ? JSON.parse(readFileSync(join(out, "missed.json"), "utf8")) : [];
  writeFileSync(join(out, "missed.json"), JSON.stringify([...prior.filter((m) => !probes.includes(m.probe)), ...missed.map((m) => ({ ...m, probe: probes.join(",") }))], null, 1));
  for (const ctx of contexts.values()) await ctx.close();
  await session.removeDevices();
} finally {
  await browser.close();
}
writeReport();
