import { afterEach, describe, expect, it, vi } from "vitest";
import { clearToasts } from "@/shell/toasts";
import { canNativeShare, shareLink } from "./share";

afterEach(() => {
  vi.unstubAllGlobals();
  clearToasts();
});

const item = { title: "A story", url: "https://example.com/a" };

describe("share", () => {
  it("opens the share sheet with the title and URL where the device has one", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { share });
    expect(canNativeShare()).toBe(true);
    expect(await shareLink(item)).toBe("shared");
    expect(share).toHaveBeenCalledWith({ title: "A story", url: "https://example.com/a" });
  });

  it("dismissing the sheet is not an error and copies nothing", async () => {
    const writeText = vi.fn();
    vi.stubGlobal("navigator", { share: vi.fn().mockRejectedValue(new DOMException("cancelled", "AbortError")), clipboard: { writeText } });
    expect(await shareLink(item)).toBe("cancelled");
    expect(writeText).not.toHaveBeenCalled();
  });

  it("copies the link where there is no share sheet, and says so", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    expect(canNativeShare()).toBe(false);
    expect(await shareLink(item)).toBe("copied");
    expect(writeText).toHaveBeenCalledWith("https://example.com/a");
  });

  it("falls back to copying when the share sheet fails for another reason", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { share: vi.fn().mockRejectedValue(new Error("boom")), clipboard: { writeText } });
    expect(await shareLink(item)).toBe("copied");
  });

  it("reports failure when nothing can copy", async () => {
    vi.stubGlobal("navigator", {});
    expect(await shareLink(item)).toBe("failed");
  });
});
