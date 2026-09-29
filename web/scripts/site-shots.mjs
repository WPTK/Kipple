// Captures the kipple.cc screenshots (and, with --site, the social preview) from a seeded local instance.
//
//   KIPPLE_SEED_SET=site npm run seed              (in one terminal; wait a minute for the feeds to fetch)
//   node scripts/site-shots.mjs --out ../../kipple-website/screenshots [--site ../../kipple-website]
//
// Writes the four WebP files the site uses, at the sizes the site's design system (DESIGN-SYSTEM.md, "Screenshots")
// names: desktop-paper.webp and desktop-midnight.webp 1800x1125 (a 1440x900 viewport at 1.25x), phone-paper-reader.webp
// and phone-midnight-list.webp 585x1266 (390x844 at 1.5x). Light is Paper and dark is Midnight, both from the browser's
// colour-scheme setting. The list uses the device's default layout (Editorial), as the site's shots always have. With --site <dir> it also renders <dir>/design-system/social-preview.html to <dir>/og.png
// (1280x640). Only a loopback address is accepted, with the seed's throwaway credentials. Needs Chromium
// (`npx playwright install chromium`, as for `npm run uat`). Run by hand at each release (docs/RELEASING.md).
/* global document, createImageBitmap, OffscreenCanvas -- used inside page.evaluate, which runs in the browser */
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { chromium } from "@playwright/test";

const { values: opt } = parseArgs({
  options: {
    url: { type: "string", default: "http://127.0.0.1:7080" },
    out: { type: "string" },
    site: { type: "string" },
    feed: { type: "string", default: "Wikimedia Picture of the Day" },
    user: { type: "string", default: "dev" },
    // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
    password: { type: "string", default: "dev-password-only-for-local-testing" },
  },
});
if (!opt.out) {
  console.error("usage: node scripts/site-shots.mjs --out <screenshots dir> [--site <kipple-website dir>] [--feed <feed title>]");
  process.exit(2);
}
const origin = new URL(opt.url);
if (!["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname)) {
  console.error(`refusing ${origin.host}: this signs in with the seed's credentials, so only a loopback instance is allowed`);
  process.exit(2);
}

const DESKTOP = { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1.25, size: [1800, 1125] };
const PHONE = { viewport: { width: 390, height: 844 }, deviceScaleFactor: 1.5, size: [585, 1266], mobile: true };
const SHOTS = [
  { file: "desktop-paper", device: DESKTOP, scheme: "light", view: "reader" },
  { file: "desktop-midnight", device: DESKTOP, scheme: "dark", view: "reader" },
  { file: "phone-paper-reader", device: PHONE, scheme: "light", view: "article" },
  { file: "phone-midnight-list", device: PHONE, scheme: "dark", view: "list" },
];

const browser = await chromium.launch();
try {
  // Sign in once and reuse the cookies.
  const login = await browser.newContext();
  const lp = await login.newPage();
  await lp.goto(origin.origin + "/");
  await lp.getByLabel("Username").fill(opt.user);
  await lp.getByLabel("Password").fill(opt.password);
  await lp.getByRole("button", { name: "Sign in" }).click();
  await lp.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
  const boot = await (await lp.request.get(origin.origin + "/api/bootstrap")).json();
  const feed = boot.feeds.find((f) => f.title.includes(opt.feed));
  if (!feed) throw new Error(`no feed titled like "${opt.feed}" (seed with KIPPLE_SEED_SET=site and wait for the first fetch)`);
  const storageState = await login.storageState();
  await login.close();

  mkdirSync(opt.out, { recursive: true });
  for (const shot of SHOTS) {
    const { device } = shot;
    const ctx = await browser.newContext({
      baseURL: origin.origin,
      colorScheme: shot.scheme,
      viewport: device.viewport,
      deviceScaleFactor: device.deviceScaleFactor,
      isMobile: !!device.mobile,
      hasTouch: !!device.mobile,
      storageState,
      serviceWorkers: "block",
    });
    const page = await ctx.newPage();
    await page.goto(`/l/all?feed=${encodeURIComponent(feed.id)}`);
    const first = page.locator('article[data-item-id] a[href^="/i/"]').first();
    await first.waitFor({ timeout: 20000 });
    if (shot.view !== "list") await first.click();
    if (shot.view === "reader") await page.getByRole("heading", { level: 1 }).first().waitFor({ timeout: 15000 });
    if (shot.view === "article") await page.getByRole("article").or(page.locator("article, main")).first().waitFor({ timeout: 15000 });
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.waitForTimeout(1500); // lazy images
    const png = await page.screenshot({ type: "png" });
    const webp = await toWebp(png);
    const [w, h] = device.size;
    const got = pngSize(png);
    if (got.width !== w || got.height !== h) throw new Error(`${shot.file}: captured ${got.width}x${got.height}, expected ${w}x${h}`);
    writeFileSync(resolve(opt.out, `${shot.file}.webp`), webp);
    console.log(`${shot.file}.webp  ${w}x${h}  ${(webp.length / 1024).toFixed(0)} KB`);
    await ctx.close();
  }

  if (opt.site) {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 640 } });
    const page = await ctx.newPage();
    await page.goto(pathToFileURL(resolve(opt.site, "design-system/social-preview.html")).href);
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(500);
    writeFileSync(resolve(opt.site, "og.png"), await page.screenshot({ type: "png" }));
    console.log("og.png  1280x640");
    await ctx.close();
  }
} finally {
  await browser.close();
}

/** PNG width and height from its IHDR chunk. */
function pngSize(buf) {
  return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
}

/** Encodes a PNG as WebP with the browser's own encoder, so no image library is needed. */
async function toWebp(png) {
  const page = await browser.newPage();
  try {
    const b64 = await page.evaluate(async (data) => {
      const bin = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
      const bmp = await createImageBitmap(new Blob([bin], { type: "image/png" }));
      const canvas = new OffscreenCanvas(bmp.width, bmp.height);
      canvas.getContext("2d").drawImage(bmp, 0, 0);
      const blob = await canvas.convertToBlob({ type: "image/webp", quality: 0.85 });
      const bytes = new Uint8Array(await blob.arrayBuffer());
      let s = "";
      for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
      return btoa(s);
    }, png.toString("base64"));
    return Buffer.from(b64, "base64");
  } finally {
    await page.close();
  }
}
