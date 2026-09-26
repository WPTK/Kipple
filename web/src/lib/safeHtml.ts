import DOMPurify from "dompurify";

// The server already sanitizes and image-proxies content_html. This is a second,
// client-side pass (defense in depth) plus link and image hygiene.

let hooked = false;
/** Set for the duration of one sanitize call: whether external links open in a new tab or the same one. */
let sameTab = false;
function ensureHooks(): void {
  if (hooked) return;
  hooked = true;
  DOMPurify.addHook("afterSanitizeAttributes", (node) => {
    if (node instanceof Element) {
      // Footnote links (#kp-...) stay in the article; every other link (a, or an image-map area) opens in a new tab.
      if ((node.tagName === "A" || node.tagName === "AREA") && node.hasAttribute("href")) {
        if ((node.getAttribute("href") ?? "").startsWith("#")) {
          // Even a server-supplied target must not survive on an in-article link.
          node.removeAttribute("target");
        } else {
          // "Same tab" (the iOS default) lets a link a native app claims hand off cleanly instead of leaving an
          // about:blank tab behind. rel stays either way.
          if (sameTab) node.removeAttribute("target");
          else node.setAttribute("target", "_blank");
          node.setAttribute("rel", "noopener noreferrer");
        }
      }
      if (node.tagName === "IMG") {
        node.setAttribute("loading", "lazy");
        node.setAttribute("decoding", "async");
        node.setAttribute("referrerpolicy", "no-referrer");
        if (!node.hasAttribute("alt")) node.setAttribute("alt", "");
      }
    }
  });
}

export function sanitizeArticleHtml(html: string, linkTarget: "new" | "same" = "new"): string {
  ensureHooks();
  sameTab = linkTarget === "same";
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ["style", "form", "input", "button", "textarea", "select", "iframe", "object", "embed", "base", "meta", "link"],
    FORBID_ATTR: ["style"],
    ADD_ATTR: ["target"],
  });
}
