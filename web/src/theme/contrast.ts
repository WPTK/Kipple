// WCAG 2.x contrast and a Machado 2009 color-vision-deficiency simulation.
// Pure functions, shared by the tests and scripts/contrast.mjs.

export function hexToRgb(hex: string): [number, number, number] {
  const h = hex.replace("#", "");
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as [number, number, number];
}

const lin = (c: number): number => {
  const s = c / 255;
  return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
};

/** sRGB channel-wise mix, like CSS `color-mix(in srgb, a pct%, b)`: `pct` percent of `a`, the rest `b`. */
export function mixHex(a: string, b: string, pct: number): string {
  const [ra, rb] = [hexToRgb(a), hexToRgb(b)];
  const ch = ra.map((v, i) => Math.round((v * pct) / 100 + (rb[i] as number) * (1 - pct / 100)));
  return "#" + ch.map((v) => v.toString(16).padStart(2, "0")).join("");
}

export function luminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

export function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

type Mat = readonly [readonly [number, number, number], readonly [number, number, number], readonly [number, number, number]];

// Severity 1.0 matrices, applied in linear RGB.
export const CVD: Record<"protanopia" | "deuteranopia" | "tritanopia", Mat> = {
  protanopia: [
    [0.152286, 1.052583, -0.204868],
    [0.114503, 0.786281, 0.099216],
    [-0.003882, -0.048116, 1.051998],
  ],
  deuteranopia: [
    [0.367322, 0.860646, -0.227968],
    [0.280085, 0.672501, 0.047413],
    [-0.01182, 0.04294, 0.968881],
  ],
  tritanopia: [
    [1.255528, -0.076749, -0.178779],
    [-0.078411, 0.930809, 0.147602],
    [0.004733, 0.691367, 0.3039],
  ],
};

const clamp01 = (n: number) => Math.min(1, Math.max(0, n));

export function toLab(rgbLinear: [number, number, number]): [number, number, number] {
  const [r, g, b] = rgbLinear.map(clamp01) as [number, number, number];
  const x = (0.4124564 * r + 0.3575761 * g + 0.1804375 * b) / 0.95047;
  const y = 0.2126729 * r + 0.7151522 * g + 0.072175 * b;
  const z = (0.0193339 * r + 0.119192 * g + 0.9503041 * b) / 1.08883;
  const f = (t: number) => (t > 216 / 24389 ? Math.cbrt(t) : (24389 / 27 * t + 16) / 116);
  const [fx, fy, fz] = [f(x), f(y), f(z)];
  return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)];
}

export function labOf(hex: string, sim?: keyof typeof CVD): [number, number, number] {
  const rgb = hexToRgb(hex).map(lin) as [number, number, number];
  if (!sim) return toLab(rgb);
  const m = CVD[sim];
  const out = m.map((row) => row[0] * rgb[0] + row[1] * rgb[1] + row[2] * rgb[2]) as [number, number, number];
  return toLab(out);
}

/** CIE76 delta E, optionally after simulating a color-vision deficiency. */
export function deltaE(a: string, b: string, sim?: keyof typeof CVD): number {
  const la = labOf(a, sim);
  const lb = labOf(b, sim);
  return Math.hypot(la[0] - lb[0], la[1] - lb[1], la[2] - lb[2]);
}

/**
 * Scheme ids whose secondary text (text2) on the selection color is still under 4.5:1 and need a design call, not a
 * nudge: Graphite (3.59) would need a much lighter text2 or a much darker selection, and Cocoa Mid (4.00) has a
 * selection lighter than its page, so neither fix keeps the selected row visible. `npm run contrast` and theme.test.ts
 * report these as known gaps instead of failing, hold every other scheme to 4.5:1, and fail once a listed scheme
 * passes (so the entry is removed).
 */
export const TEXT2_SELECTION_KNOWN_GAPS: readonly string[] = ["graphite", "cocoa-mid"];

/** Heatmap ramp: percent of the scheme's text color mixed into its surface, levels 0 to 4. Adjacent steps must keep 1.5:1. */
export const HEAT_MIX = [0, 26, 48, 72, 100] as const;
