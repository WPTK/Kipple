import DOMPurify from "dompurify";

// The server already sanitizes and image-proxies content_html. This is a second,
// client-side pass (defense in depth) plus link and image hygiene.

let hooked = false;
function ensureHooks(): void {
  if (hooked) return;
  hooked = true;
  DOMPurify.addHook("afterSanitizeAttributes", (node) => {
    if (node instanceof Element) {
      // Footnote links (#kp-...) stay in the article; every other link opens in a new tab.
      if (node.tagName === "A" && node.hasAttribute("href")) {
        if ((node.getAttribute("href") ?? "").startsWith("#")) {
          // Even a server-supplied target must not survive on an in-article link.
          node.removeAttribute("target");
        } else {
          node.setAttribute("target", "_blank");
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

export function sanitizeArticleHtml(html: string): string {
  ensureHooks();
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ["style", "form", "input", "button", "textarea", "select", "iframe", "object", "embed", "base", "meta", "link"],
    FORBID_ATTR: ["style"],
    ADD_ATTR: ["target"],
  });
}
