import { devicePrefsStore, type LinkTarget } from "./devicePrefs";

// Where external links open. A new tab is right on a desktop, but on iPhone and iPad Safari (and an installed
// PWA) a link that a native app claims (ESPN, YouTube, ...) hands off to the app and leaves the new tab behind
// as an empty about:blank page. Opening in the SAME tab lets the system hand the link off cleanly, and Back
// returns to Kipple. So the default is "same" on Apple touch devices and "new" everywhere else; a device
// setting ("Open links in") overrides it. Every link keeps rel="noopener noreferrer".

interface Nav {
  userAgent?: string;
  platform?: string;
  maxTouchPoints?: number;
}

/** iPhone, iPod and iPad, including an iPad that reports itself as a Mac (desktop-class Safari) but has touch. */
export function isAppleTouch(nav: Nav = typeof navigator === "undefined" ? {} : navigator): boolean {
  const ua = nav.userAgent ?? "";
  if (/iPhone|iPad|iPod/.test(ua)) return true;
  return (nav.platform === "MacIntel" || /Macintosh/.test(ua)) && (nav.maxTouchPoints ?? 0) > 1;
}

export function defaultLinkTarget(nav?: Nav): LinkTarget {
  return isAppleTouch(nav) ? "same" : "new";
}

/** The effective target: the device setting when there is one, else the platform default. */
export function resolveLinkTarget(pref: LinkTarget | null, nav?: Nav): LinkTarget {
  return pref ?? defaultLinkTarget(nav);
}

/** Open an external URL the way this device wants: this tab, or a new one with no opener and no referrer. */
export function openExternal(url: string, target: LinkTarget = resolveLinkTarget(devicePrefsStore.get().linkTarget)): void {
  if (target === "same") window.location.assign(url);
  else window.open(url, "_blank", "noopener,noreferrer");
}
