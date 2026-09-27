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

/** The card as a PNG, or null when there is no canvas (or it failed). */
export async function renderCardPng(model: WrappedModel, options: WrappedOptions): Promise<Blob | null> {
  if (!canRenderImage()) return null;
  try {
    await document.fonts?.ready;
    const canvas = document.createElement("canvas");
    canvas.width = CARD_W;
    canvas.height = CARD_H;
    const ctx = canvas.getContext("2d");
    if (!ctx) return null;
    const font = getComputedStyle(document.body).fontFamily || "sans-serif";
    drawCard(ctx, buildWrappedCard(model, options), themeColors(), font);
    return await new Promise<Blob | null>((res) => canvas.toBlob((b) => res(b), "image/png"));
  } catch {
    return null;
  }
}

export const cardFileName = (year: number): string => `kipple-${year}.png`;

/**
 * Hand the card to the device's share sheet: the image when the sheet takes files, else the text. Dismissing the
 * sheet is "cancelled", not an error. Only called from a button press; nothing is sent anywhere else.
 */
export async function shareWrapped(model: WrappedModel, options: WrappedOptions): Promise<WrappedShareResult> {
  if (typeof navigator === "undefined" || typeof navigator.share !== "function") return "failed";
  const text = wrappedText(model, options);
  const title = `My ${model.year} in Kipple`;
  try {
    const png = await renderCardPng(model, options);
    if (png && typeof File === "function") {
      const file = new File([png], cardFileName(model.year), { type: "image/png" });
      if (typeof navigator.canShare === "function" && navigator.canShare({ files: [file] })) {
        await navigator.share({ files: [file], title, text });
        return "shared";
      }
    }
    await navigator.share({ title, text });
    return "shared";
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") return "cancelled";
    return "failed";
  }
}

export async function copyWrappedText(model: WrappedModel, options: WrappedOptions): Promise<WrappedShareResult> {
  return (await copyToClipboard(wrappedText(model, options))) ? "copied" : "failed";
}

export async function downloadWrappedImage(model: WrappedModel, options: WrappedOptions): Promise<WrappedShareResult> {
  const png = await renderCardPng(model, options);
  if (!png) return "failed";
  const url = URL.createObjectURL(png);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = cardFileName(model.year);
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  }
  return "downloaded";
}
