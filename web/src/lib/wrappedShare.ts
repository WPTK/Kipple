import { copyToClipboard } from "@/lib/share";
import { CARD_H, CARD_W, buildWrappedCard, wrappedText, type CardColor, type CardOp, type WrappedModel, type WrappedOptions } from "@/lib/wrapped";

/** CSS variable behind each card color: the active theme, read when the image is drawn. */
export const CARD_TOKENS: Record<CardColor, string> = {
  bg: "--kp-bg",
  surface: "--kp-surface",
  text: "--kp-text",
  text2: "--kp-text2",
  accent: "--kp-accent",
};

export type WrappedShareResult = "shared" | "cancelled" | "failed" | "copied" | "downloaded";

/** Whether a canvas can be drawn on (not in jsdom or very old browsers). */
export function canRenderImage(): boolean {
  try {
    // Checked without asking for a context: jsdom has none and would only log that it is not implemented.
    return typeof document !== "undefined" && typeof CanvasRenderingContext2D !== "undefined" && typeof HTMLCanvasElement !== "undefined" && typeof HTMLCanvasElement.prototype.toBlob === "function";
  } catch {
    return false;
  }
}

const themeColors = (): Record<CardColor, string> => {
  const cs = getComputedStyle(document.documentElement);
  const out = {} as Record<CardColor, string>;
  for (const [k, v] of Object.entries(CARD_TOKENS)) out[k as CardColor] = cs.getPropertyValue(v).trim() || "#888";
  return out;
};

/** The dialog and preview both render in the UI font, never the reader's chosen reading font (index.css: `[role="dialog"]`
 * forces `--font-sans`). The PNG must match, so the caller passes the preview element's computed font when it has one;
 * otherwise this falls back to the same `--font-sans` token so the two never drift apart. */
export function cardFont(font?: string): string {
  if (font) return font;
  try {
    const v = getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim();
    if (v) return v;
  } catch {
    /* fall through to the generic default below */
  }
  return "sans-serif";
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}

/** Thin renderer: draws the instruction list, nothing else. */
export function drawCard(ctx: CanvasRenderingContext2D, ops: CardOp[], colors: Record<CardColor, string>, font: string): void {
  for (const op of ops) {
    if (op.kind === "rect") {
      ctx.fillStyle = colors[op.color];
      if (op.radius) {
        roundRect(ctx, op.x, op.y, op.w, op.h, op.radius);
        ctx.fill();
      } else ctx.fillRect(op.x, op.y, op.w, op.h);
    } else {
      ctx.fillStyle = colors[op.color];
      ctx.font = `${op.weight} ${op.size}px ${font}`;
      ctx.textBaseline = "alphabetic";
      ctx.fillText(op.text, op.x, op.y);
    }
  }
}

/** How long to wait for webfonts before drawing with whatever is available (a fallback font beats hanging forever). */
const FONT_READY_TIMEOUT_MS = 1500;

async function fontsReady(): Promise<void> {
  const ready = document.fonts?.ready;
  if (!ready) return;
  await Promise.race([ready.then(() => undefined), new Promise<void>((resolve) => setTimeout(resolve, FONT_READY_TIMEOUT_MS))]);
}

/**
 * The card as a PNG, or null when there is no canvas (or it failed). `font`, when given, should be the preview
 * element's computed font so the PNG matches what was shown (see `cardFont`); otherwise the same `--font-sans`
 * token is used as a stand-in for it.
 */
export async function renderCardPng(model: WrappedModel, options: WrappedOptions, font?: string): Promise<Blob | null> {
  if (!canRenderImage()) return null;
  try {
    await fontsReady();
    const canvas = document.createElement("canvas");
    canvas.width = CARD_W;
    canvas.height = CARD_H;
    const ctx = canvas.getContext("2d");
    if (!ctx) return null;
    drawCard(ctx, buildWrappedCard(model, options), themeColors(), cardFont(font));
    return await new Promise<Blob | null>((res) => canvas.toBlob((b) => res(b), "image/png"));
  } catch {
    return null;
  }
}

export const cardFileName = (year: number): string => `kipple-${year}.png`;

async function tryShare(data: ShareData): Promise<"shared" | "cancelled" | "failed"> {
  try {
    await navigator.share(data);
    return "shared";
  } catch (e) {
    // Dismissing the sheet is not an error, here or in the text-only retry below.
    if (e instanceof DOMException && e.name === "AbortError") return "cancelled";
    return "failed";
  }
}

/** `canShare` is spec'd to just return a boolean, but some hosts throw instead of returning false for a combination
 * they refuse (files + text). Either way, that means "fall back to text", not "crash". */
function canShareFiles(file: File): boolean {
  try {
    return typeof navigator.canShare === "function" && navigator.canShare({ files: [file] });
  } catch {
    return false;
  }
}

/**
 * Hand an already-rendered card to the device's share sheet: the image when the sheet takes files, else the text.
 * `blob` must be pre-rendered (see `renderCardPng`) so the share call runs synchronously against the user gesture
 * that triggered it — iOS revokes the gesture once an `await` (font loading, canvas encode) runs too long, which
 * throws `NotAllowedError` rather than a clean cancel. When the file share fails for any reason other than the
 * user dismissing it (canShare says no, share() itself rejects, the target refuses files+text), this retries with
 * a text-only share before giving up. Only that second failure is reported as "failed".
 */
export async function shareWrappedBlob(blob: Blob | null, model: WrappedModel, options: WrappedOptions): Promise<WrappedShareResult> {
  if (typeof navigator === "undefined" || typeof navigator.share !== "function") return "failed";
  const text = wrappedText(model, options);
  const title = `My ${model.year} in Kipple`;

  if (blob && typeof File === "function") {
    const file = new File([blob], cardFileName(model.year), { type: "image/png" });
    if (canShareFiles(file)) {
      const r = await tryShare({ files: [file], title, text });
      if (r !== "failed") return r;
      // Falls through to the text-only retry.
    }
  }
  return tryShare({ title, text });
}

export async function copyWrappedText(model: WrappedModel, options: WrappedOptions): Promise<WrappedShareResult> {
  return (await copyToClipboard(wrappedText(model, options))) ? "copied" : "failed";
}

/** Saves an already-rendered card. Synchronous aside from the click itself, same reasoning as `shareWrappedBlob`. */
export function downloadBlob(blob: Blob, year: number): WrappedShareResult {
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = cardFileName(year);
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  }
  return "downloaded";
}
