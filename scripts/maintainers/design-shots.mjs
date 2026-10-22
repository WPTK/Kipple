// Captures every screen of the app for the design review: each surface (a screen, a state or an open menu) on a phone,
// a tablet and a desktop, in the chosen themes, plus the smallest and largest text sizes on the screens that carry the
// most text. Writes PNGs, a manifest and contact sheets (index.html, and one PNG per area) to --out. It judges nothing:
// it is what a person or a reviewer looks at. docs/maintainers/design-review.md says how the review uses it.
//
//   KIPPLE_SEED_SET=design npm run seed            (in one terminal; it waits for its feeds itself)
//   node ../scripts/maintainers/design-shots.mjs --out ../.design/shots [--themes paper,midnight|all] [--only <regexp>]
//        [--devices phone,tablet,desktop] [--no-sizes] [--url http://127.0.0.1:1919]
// Run from web/ (Playwright is the one web/ installs); the output is relative to it. .design/ is gitignored.
//
// Each device and theme is one signed-in browser context, so one device on the server, whose profile gets the theme, text
// size and layout through PATCH /api/device before its surfaces load. Motion is reduced and the first-run hints are
// marked seen, so two runs of the same build give the same pictures. A screen taller than the window is captured a
// window at a time (up to four), scrolling whatever scrolls. A surface that cannot be reached is listed in the manifest
// with the reason and the run carries on; the exit code is 1 when any was missed. Only a loopback address is accepted,
// with the seed's throwaway credentials, and the devices it registered are removed at the end.
/* global document -- used inside page.evaluate, which runs in the browser */
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { COMMON_OPTIONS, chromium, resolveCommon, schemes, scrollOnce, session, settle } from "./lib/design-surfaces.mjs";

const { values: opt } = parseArgs({ options: { ...COMMON_OPTIONS, "no-sizes": { type: "boolean", default: false } } });
const { origin, themes, devices, surfaces } = resolveCommon(
  opt,
  "usage: node ../scripts/maintainers/design-shots.mjs --out <dir> [--themes a,b|all] [--devices phone,tablet,desktop] [--only <regexp>] [--no-sizes]",
);
/** The text sizes beside the default (web/src/lib/prefs.ts TEXT_SIZES), captured on surfaces marked `sizes`, first theme only. */
const EXTRA_SIZES = opt["no-sizes"] ? [] : [0.875, 1.5];

// ---- Capture ----------------------------------------------------------------------------------------------------------
const out = resolve(opt.out);
mkdirSync(out, { recursive: true });
const shots = [];
const missed = [];
const browser = await chromium.launch();
const { context, setProfile, library, removeDevices } = session(browser, { origin, user: opt.user, password: opt.password });

async function capture(ctx, lib, s, device, theme, size) {
  const name = `${s.id}__${device}__${theme}${size === 1 ? "" : `__text${size}`}`;
  const page = await ctx.newPage();
  // Requests still waiting for an answer, named in a miss: they tell a slow server from a page that never asked.
  const pending = new Map();
  const errors = [];
  page.on("request", (r) => pending.set(r, Date.now()));
  page.on("requestfinished", (r) => pending.delete(r));
  page.on("requestfailed", (r) => pending.delete(r));
  page.on("pageerror", (e) => errors.push(String(e).split("\n")[0]));
  try {
    if (!s.signedOut) await setProfile(ctx, { "client.layout": s.layout ?? "magazine" });
    await s.go(page, lib);
    await settle(page, s.area === "menus");
    const files = [`${name}.png`];
    await page.screenshot({ path: join(out, files[0]) });
    for (let i = 2; s.tall && i <= 4 && (await scrollOnce(page)); i++) {
      await settle(page, false);
      files.push(`${name}__${i}.png`);
      await page.screenshot({ path: join(out, files.at(-1)) });
    }
    shots.push({ surface: s.id, area: s.area, title: s.title, device, theme, size, files });
    console.log(`ok   ${name}${files.length > 1 ? ` (${files.length} windows)` : ""}`);
  } catch (e) {
    const why = String(e.message).split("\n")[0];
    const waiting = [...pending].filter(([r]) => !r.url().endsWith("/api/events")).map(([r, t]) => `${r.method()} ${new URL(r.url()).pathname} (${Math.round((Date.now() - t) / 1000)} s)`);
    missed.push({ surface: s.id, device, theme, size, why, url: page.url(), waiting, errors });
    console.error(`MISS ${name}: ${why}`);
    await page.screenshot({ path: join(out, `${name}__missed.png`) }).catch(() => {});
  } finally {
    await page.close();
  }
}

try {
  let lib;
  for (const device of devices) {
    const runs = themes.map((theme) => ({ theme, size: 1 })).concat(EXTRA_SIZES.map((size) => ({ theme: themes[0], size })));
    for (const { theme, size } of runs) {
      const todo = surfaces.filter((s) => !s.signedOut && (size === 1 || s.sizes));
      if (todo.length) {
        const ctx = await context(device);
        try {
          await setProfile(ctx, { "ui.theme": theme, "client.text_size": size, "client.peek_seen": true });
          if (!lib) lib = await library(ctx);
          for (const s of todo) await capture(ctx, lib, s, device, theme, size);
        } finally {
          await ctx.close();
        }
      }
      // A signed-out page follows the device's light or dark setting, so it is taken in the matching colour scheme.
      for (const s of surfaces.filter((x) => x.signedOut && size === 1)) {
        const ctx = await context(device, { signedOutScheme: schemes.find((x) => x.id === theme).kind });
        await capture(ctx, lib, s, device, theme, size);
        await ctx.close();
      }
    }
  }
  await removeDevices(devices[0]);
  writeFileSync(join(out, "manifest.json"), JSON.stringify({ origin, taken: new Date().toISOString(), themes, devices, shots, missed }, null, 2));
  writeFileSync(join(out, "index.html"), sheet(shots));
  await areaSheets();
} finally {
  await browser.close();
}
console.log(`\n${shots.length} surface captures in ${out}${missed.length ? `, ${missed.length} missed (see manifest.json)` : ""}`);
process.exit(missed.length ? 1 : 0);

/** The contact sheet: one section per area, one row per surface, every capture of it side by side. `?area=` shows one area. */
function sheet(list) {
  const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
  const areas = [...new Set(list.map((s) => s.area))];
  const body = areas
    .map((area) => {
      const rows = [...new Set(list.filter((s) => s.area === area).map((s) => s.surface))].map((id) => {
        const caps = list.filter((s) => s.surface === id);
        const cells = caps
          .flatMap((c) => c.files.map((f, i) => `<figure class="${c.device}"><a href="${esc(f)}"><img src="${esc(f)}" loading="lazy"></a><figcaption>${esc(`${c.device} · ${c.theme}${c.size === 1 ? "" : ` · text ${c.size}`}${i ? ` · ${i + 1}` : ""}`)}</figcaption></figure>`))
          .join("");
        return `<h3 id="${esc(id)}">${esc(caps[0].title)} <code>${esc(id)}</code></h3><div class="row">${cells}</div>`;
      });
      return `<section data-area="${esc(area)}"><h2>${esc(area)}</h2>${rows.join("")}</section>`;
    })
    .join("");
  return `<!doctype html><meta charset="utf-8"><title>Kipple design review captures</title><style>
body{font:14px system-ui,sans-serif;margin:16px;background:#e9e9ec;color:#111}h2{margin:32px 0 4px;text-transform:capitalize}
h3{margin:18px 0 6px;font-size:14px}code{color:#666;font-weight:400}.row{display:flex;flex-wrap:wrap;gap:10px;align-items:flex-start}
figure{margin:0;flex:none}img{display:block;border:1px solid #bbb;background:#fff}figcaption{font-size:11px;color:#444;margin-top:2px}
.phone img{width:180px}.tablet img{width:260px}.desktop img{width:420px}</style>
<script>addEventListener("DOMContentLoaded",()=>{const a=new URLSearchParams(location.search).get("area");if(a)for(const s of document.querySelectorAll("section"))if(s.dataset.area!==a)s.remove()})</script>
<h1>Kipple design review captures</h1><p>${esc(new Date().toISOString().slice(0, 16).replace("T", " "))} UTC · ${list.length} captures · themes ${esc(themes.join(", "))}</p>${body}`;
}

/** sheet-<area>.png: the contact sheet of one area as a picture, for sending where a folder of files cannot go. */
async function areaSheets() {
  const page = await browser.newPage({ viewport: { width: 1700, height: 1000 } });
  try {
    for (const area of [...new Set(shots.map((s) => s.area))]) {
      await page.goto(`${pathToFileURL(join(out, "index.html")).href}?area=${area}`);
      await page.evaluate(() => Promise.all([...document.images].map((i) => (i.loading = "eager", i.decode().catch(() => {})))));
      await page.screenshot({ path: join(out, `sheet-${area}.png`), fullPage: true });
    }
  } finally {
    await page.close();
  }
}
