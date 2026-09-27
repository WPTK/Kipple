import { afterEach, describe, expect, it, vi } from "vitest";
import type { WrappedModel, WrappedOptions } from "@/lib/wrapped";
import { cardFont, downloadBlob, renderCardPng, shareWrappedBlob } from "@/lib/wrappedShare";

const model: WrappedModel = {
  year: 2026,
  itemsRead: 12,
  activeSeconds: 5400,
  daysActive: 4,
  longestStreak: 3,
  busiestWeekday: 0,
  busiestHour: 19,
  busiestMonth: 2,
  avgReadSeconds: 150,
  topSources: [{ feedId: "1", name: "Secret Feed", items: 12 }],
  longestRead: { title: "Private Title", feed: "Secret Feed", seconds: 1320 },
  historyDays: 200,
  empty: false,
};
const opts: WrappedOptions = { topSources: false, longestRead: false };

const withNav = (extra: Record<string, unknown>) => {
  for (const [k, v] of Object.entries(extra)) Object.defineProperty(navigator, k, { configurable: true, value: v });
};

afterEach(() => {
  for (const k of ["share", "canShare"]) Reflect.deleteProperty(navigator, k);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("cardFont", () => {
  it("uses the given font when there is one", () => {
    expect(cardFont('"Fancy Font", serif')).toBe('"Fancy Font", serif');
  });

  it("falls back to the --font-sans token, never the reader's chosen reading font", () => {
    vi.spyOn(window, "getComputedStyle").mockReturnValue({ getPropertyValue: () => 'Literata, serif' } as unknown as CSSStyleDeclaration);
    expect(cardFont()).toBe("Literata, serif");
  });

  it("falls back again to a generic font when even that is empty", () => {
    vi.spyOn(window, "getComputedStyle").mockReturnValue({ getPropertyValue: () => "" } as unknown as CSSStyleDeclaration);
    expect(cardFont()).toBe("sans-serif");
  });
});

describe("renderCardPng", () => {
  function stubCanvas(drawn: () => void) {
    vi.stubGlobal("CanvasRenderingContext2D", class {});
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
      fillRect: drawn,
      fillText: drawn,
      beginPath() {},
      moveTo() {},
      arcTo() {},
      closePath() {},
      fill() {},
    } as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((cb) => cb(new Blob(["x"], { type: "image/png" })));
  }

  it("proceeds with whatever fonts are ready when document.fonts.ready never resolves", async () => {
    vi.useFakeTimers();
    stubCanvas(() => {});
    const never = new Promise<FontFaceSet>(() => {});
    vi.stubGlobal("document", Object.assign(document, { fonts: { ready: never } }));
    const p = renderCardPng(model, opts);
    await vi.advanceTimersByTimeAsync(1500);
    const png = await p;
    expect(png).not.toBeNull();
    vi.useRealTimers();
  });

  it("draws with the font it was given", async () => {
    const fonts: string[] = [];
    vi.stubGlobal("CanvasRenderingContext2D", class {});
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
      fillRect() {},
      fillText() {},
      beginPath() {},
      moveTo() {},
      arcTo() {},
      closePath() {},
      fill() {},
      set font(v: string) {
        fonts.push(v);
      },
    } as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((cb) => cb(new Blob(["x"])));
    await renderCardPng(model, opts, '"Preview Font", sans-serif');
    expect(fonts.some((f) => f.includes('"Preview Font", sans-serif'))).toBe(true);
  });
});

describe("shareWrappedBlob", () => {
  it("shares the file when the sheet takes it", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    withNav({ share, canShare: () => true });
    const blob = new Blob(["x"], { type: "image/png" });
    const r = await shareWrappedBlob(blob, model, opts);
    expect(r).toBe("shared");
    const arg = share.mock.calls[0]![0] as { files?: File[] };
    expect(arg.files?.[0]?.name).toBe("kipple-2026.png");
  });

  it("falls back to text-only when canShare refuses the file", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    withNav({ share, canShare: () => false });
    const blob = new Blob(["x"], { type: "image/png" });
    const r = await shareWrappedBlob(blob, model, opts);
    expect(r).toBe("shared");
    const arg = share.mock.calls[0]![0] as { files?: File[]; text: string };
    expect(arg.files).toBeUndefined();
    expect(arg.text).toContain("12 items");
  });

  it("falls back to text-only when canShare throws", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    withNav({
      share,
      canShare: () => {
        throw new Error("nope");
      },
    });
    const blob = new Blob(["x"], { type: "image/png" });
    const r = await shareWrappedBlob(blob, model, opts);
    expect(r).toBe("shared");
    expect(share).toHaveBeenCalledTimes(1);
    expect((share.mock.calls[0]![0] as { files?: File[] }).files).toBeUndefined();
  });

  it("falls back to text-only when the file share itself rejects for a reason other than AbortError", async () => {
    const share = vi.fn().mockRejectedValueOnce(new DOMException("nope", "NotAllowedError")).mockResolvedValueOnce(undefined);
    withNav({ share, canShare: () => true });
    const blob = new Blob(["x"], { type: "image/png" });
    const r = await shareWrappedBlob(blob, model, opts);
    expect(r).toBe("shared");
    expect(share).toHaveBeenCalledTimes(2);
    expect((share.mock.calls[1]![0] as { files?: File[] }).files).toBeUndefined();
  });

  it("reports failure only once the text-only retry also fails", async () => {
    const share = vi.fn().mockRejectedValue(new Error("boom"));
    withNav({ share, canShare: () => true });
    const blob = new Blob(["x"], { type: "image/png" });
    const r = await shareWrappedBlob(blob, model, opts);
    expect(r).toBe("failed");
    expect(share).toHaveBeenCalledTimes(2);
  });

  it("treats a cancelled file share as a silent cancel, without a text-only retry", async () => {
    const share = vi.fn().mockRejectedValue(new DOMException("x", "AbortError"));
    withNav({ share, canShare: () => true });
    const blob = new Blob(["x"], { type: "image/png" });
    const r = await shareWrappedBlob(blob, model, opts);
    expect(r).toBe("cancelled");
    expect(share).toHaveBeenCalledTimes(1);
  });

  it("treats a cancelled text-only share as a silent cancel too", async () => {
    const share = vi.fn().mockRejectedValue(new DOMException("x", "AbortError"));
    withNav({ share });
    const r = await shareWrappedBlob(null, model, opts);
    expect(r).toBe("cancelled");
  });
});

describe("downloadBlob", () => {
  it("triggers a download and revokes the object URL afterwards", () => {
    vi.useFakeTimers();
    const createUrl = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:x");
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const click = vi.fn();
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(click);
    const r = downloadBlob(new Blob(["x"]), 2026);
    expect(r).toBe("downloaded");
    expect(createUrl).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    vi.advanceTimersByTime(10_000);
    expect(revoke).toHaveBeenCalledWith("blob:x");
    vi.useRealTimers();
  });
});
