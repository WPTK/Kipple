/**
 * An article's or a feed's own address comes from the feed, so it is not trusted: only an absolute http or https
 * URL is opened or put in an href. Anything else (`javascript:`, `data:`, a relative path, garbage) gives
 * undefined, and the caller does nothing (or renders no link).
 */
export function safeHttpUrl(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  let u: URL;
  try {
    u = new URL(url.trim());
  } catch {
    return undefined;
  }
  return u.protocol === "http:" || u.protocol === "https:" ? u.href : undefined;
}
