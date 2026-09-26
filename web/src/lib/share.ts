import { announce, toast } from "@/shell/toasts";

export type ShareResult = "shared" | "copied" | "cancelled" | "failed";

export const canNativeShare = (): boolean => typeof navigator !== "undefined" && typeof navigator.share === "function";

/** Copy a link to the clipboard. Returns false when the browser has no clipboard or refuses. */
export async function copyToClipboard(text: string): Promise<boolean> {
  const clip = typeof navigator !== "undefined" ? navigator.clipboard : undefined;
  if (!clip) return false;
  try {
    await clip.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/**
 * Share an article's link. Where the device has a share sheet (iOS, Android, some desktops) that opens with the
 * title and URL. Everywhere else the link is copied and a toast says so. Nothing is sent to Kipple: the share
 * stats event kind is defined on the server but no client sends it yet, and sharing needs none.
 */
export async function shareLink(item: { title: string; url: string }): Promise<ShareResult> {
  if (canNativeShare()) {
    try {
      await navigator.share({ title: item.title, url: item.url });
      return "shared";
    } catch (e) {
      // Dismissing the sheet is not an error.
      if (e instanceof DOMException && e.name === "AbortError") return "cancelled";
      // Any other failure falls through to copying.
    }
  }
  if (await copyToClipboard(item.url)) {
    toast("Link copied");
    return "copied";
  }
  toast("Couldn't copy the link. Long-press the link to copy it.", "error");
  return "failed";
}

/** Copy only (the menu's "Copy link"): announces politely, like the share fallback does with a toast. */
export async function copyLink(url: string): Promise<boolean> {
  if (await copyToClipboard(url)) {
    announce("Link copied");
    return true;
  }
  toast("Couldn't copy the link. Long-press the link to copy it.", "error");
  return false;
}
