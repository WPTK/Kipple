// Recomputes WCAG contrast and color-vision-deficiency separation for every
// scheme in src/theme/schemes.json. Exits 1 if a threshold is missed.
//   npm run contrast              print a summary, fail on violations
//   npm run contrast -- --markdown  print the tables used in src/theme/README.md
// Needs Node >= 22.18 (built-in TypeScript type stripping).
import { readFileSync } from "node:fs";
import { HEAT_MIX, TEXT2_SELECTION_KNOWN_GAPS, contrast, deltaE, mixHex } from "../src/theme/contrast.ts";

const schemes = JSON.parse(readFileSync(new URL("../src/theme/schemes.json", import.meta.url), "utf8"));
const md = process.argv.includes("--markdown");

// WCAG 2.x: text 4.5:1, non-text UI (accent, unread dot, star) 3:1.
const TEXT_MIN = 4.5;
const UI_MIN = 3;
// CIE76 delta E at or above this is "clearly separate" at icon size.
const CVD_CLEAR = 15;

// Stats heatmap ramp (--heat in src/index.css): each step, including level 1 against the empty cell, keeps this.
const HEAT_MIN = 1.5;
const heatRows = [];
const failures = [];
const gaps = [];
const rows = [];
for (const id of TEXT2_SELECTION_KNOWN_GAPS) {
  if (!schemes.some((s) => s.id === id)) failures.push(`TEXT2_SELECTION_KNOWN_GAPS lists "${id}", which is not a scheme id`);
}
const cvdRows = [];

for (const s of schemes) {
  const t = s.tokens;
  const r = {
    text: contrast(t.text, t.bg),
    text2: contrast(t.text2, t.bg),
    text2s: contrast(t.text2, t.surface),
    texts: contrast(t.text, t.surface),
    link: contrast(t.link, t.bg),
    links: contrast(t.link, t.surface),
    danger: contrast(t.danger, t.bg),
    dangers: contrast(t.danger, t.surface),
    accent: contrast(t.accent, t.bg),
    unread: contrast(t.unread, t.bg),
    star: contrast(t.star, t.bg),
    sel: contrast(t.text, t.selection),
  };
  const need = {
    text: TEXT_MIN, text2: TEXT_MIN, text2s: TEXT_MIN, texts: TEXT_MIN, link: TEXT_MIN, links: TEXT_MIN,
    danger: TEXT_MIN, dangers: TEXT_MIN, accent: UI_MIN, unread: UI_MIN, star: UI_MIN, sel: TEXT_MIN,
  };
  for (const [k, min] of Object.entries(need)) {
    if (r[k] < min) failures.push(`${s.name}: ${k} ${r[k].toFixed(2)} < ${min}`);
  }
  // Secondary text on the selection color (a selected row's meta line, a selected option's hint): 4.5:1, except the
  // listed known gaps, which are reported until fixed and must be unlisted once they pass.
  const text2sel = contrast(t.text2, t.selection);
  const knownGap = TEXT2_SELECTION_KNOWN_GAPS.includes(s.id);
  if (text2sel < TEXT_MIN) (knownGap ? gaps : failures).push(`${s.name}: text2 on selection ${text2sel.toFixed(2)} < ${TEXT_MIN}`);
  else if (knownGap) failures.push(`${s.name}: text2 on selection passes (${text2sel.toFixed(2)}); remove it from TEXT2_SELECTION_KNOWN_GAPS`);
  // Toasts (undo, help, errors): text on the accent-tinted surface (--kp-toast-bg in index.css: 18% accent into
  // the surface), and the accent and danger borders against the page.
  const toastBg = mixHex(t.accent, t.surface, 18);
  const toast = { text: contrast(t.text, toastBg), border: contrast(t.accent, t.bg), errBorder: contrast(t.danger, t.bg) };
  if (toast.text < TEXT_MIN) failures.push(`${s.name}: toast text ${toast.text.toFixed(2)} < ${TEXT_MIN}`);
  if (toast.border < UI_MIN) failures.push(`${s.name}: toast border ${toast.border.toFixed(2)} < ${UI_MIN}`);
  if (toast.errBorder < UI_MIN) failures.push(`${s.name}: error toast border ${toast.errBorder.toFixed(2)} < ${UI_MIN}`);
  // Highlighted keywords (--kp-hl-bg in index.css: 20% star into the page background): the scheme's text on that
  // tint, and the star underline against the page.
  const hlBg = mixHex(t.star, t.bg, 20);
  const hl = { text: contrast(t.text, hlBg), underline: contrast(t.star, t.bg) };
  if (hl.text < TEXT_MIN) failures.push(`${s.name}: highlight text ${hl.text.toFixed(2)} < ${TEXT_MIN}`);
  if (hl.underline < UI_MIN) failures.push(`${s.name}: highlight underline ${hl.underline.toFixed(2)} < ${UI_MIN}`);
  rows.push([s.name, ...["text", "text2", "text2s", "link", "links", "danger", "accent", "star", "sel"].map((k) => r[k].toFixed(2)), text2sel.toFixed(2)]);

  const heat = HEAT_MIX.map((p) => mixHex(t.text, t.surface, p));
  const heatSteps = heat.slice(1).map((c, i) => contrast(c, heat[i]));
  heatRows.push([s.name, ...heatSteps.map((v) => v.toFixed(2))]);
  heatSteps.forEach((v, i) => {
    if (v < HEAT_MIN) failures.push(`${s.name}: heatmap step ${i} to ${i + 1} ${v.toFixed(2)} < ${HEAT_MIN}`);
  });

  const cvd = {};
  for (const sim of ["deuteranopia", "protanopia", "tritanopia"]) {
    cvd[sim] = Math.min(
      deltaE(t.accent, t.star, sim),
      deltaE(t.accent, t.danger, sim),
      deltaE(t.star, t.danger, sim),
    );
  }
  cvdRows.push([s.name, ...Object.values(cvd).map((v) => v.toFixed(1))]);
  // Schemes that claim color-blind safety, plus Signal after its danger fix,
  // must keep all three pairs clear for every simulated deficiency.
  if (["tracing", "carbon", "inkwell", "teletype", "signal"].includes(s.id)) {
    for (const [sim, v] of Object.entries(cvd)) {
      if (v < CVD_CLEAR) failures.push(`${s.name}: ${sim} worst pair ${v.toFixed(1)} < ${CVD_CLEAR}`);
    }
  }
}

// Carbon and Fountain must not be near-twins.
const carbon = schemes.find((s) => s.id === "carbon").tokens;
const fountain = schemes.find((s) => s.id === "fountain").tokens;
const pair = {
  bg: deltaE(carbon.bg, fountain.bg),
  surface: deltaE(carbon.surface, fountain.surface),
  text: deltaE(carbon.text, fountain.text),
  accent: deltaE(carbon.accent, fountain.accent),
};
if (pair.bg < 25) failures.push(`Carbon/Fountain background delta E ${pair.bg.toFixed(1)} < 25`);
if (pair.accent < 40) failures.push(`Carbon/Fountain accent delta E ${pair.accent.toFixed(1)} < 40`);

const fmt = (head, body) =>
  [head, head.map(() => "---"), ...body].map((r) => "| " + r.join(" | ") + " |").join("\n");

if (md) {
  console.log(fmt(["Scheme", "text", "text2", "text2/surf", "link", "link/surf", "danger", "accent", "star", "text/sel", "text2/sel"], rows));
  console.log();
  console.log(fmt(["Scheme", "heat 0-1", "1-2", "2-3", "3-4"], heatRows));
  console.log();
  console.log(fmt(["Scheme", "Deutan", "Protan", "Tritan"], cvdRows));
  console.log();
  console.log("Carbon vs Fountain delta E:", Object.entries(pair).map(([k, v]) => `${k} ${v.toFixed(1)}`).join(", "));
} else {
  console.log(`${schemes.length} schemes checked. Carbon vs Fountain delta E:`, Object.entries(pair).map(([k, v]) => `${k} ${v.toFixed(1)}`).join(", "));
}
if (gaps.length && !md) console.log("\nKNOWN GAPS (TEXT2_SELECTION_KNOWN_GAPS in src/theme/contrast.ts):\n" + gaps.join("\n") + "\n");
if (failures.length) {
  console.error("\nFAILURES:\n" + failures.join("\n"));
  process.exit(1);
}
if (!md) console.log(`All contrast and separation thresholds met${gaps.length ? ", except the known gaps listed above" : ""}.`);
