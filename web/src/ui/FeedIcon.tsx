import { useState } from "react";
import { cn } from "@/lib/cn";

// The tile colors: the scheme's text and secondary text colors, each held to 4.5:1 against the page in every scheme
// (contrast.mjs), so the letter, which is the page color, is AA on every tile. Neither is ever a status color (unread,
// accent, star, danger) in any scheme, so a healthy feed never looks unread, starred or broken; FeedIcon.test.tsx checks
// that over schemes.json. The link color is left out: it is the unread color in several schemes.
export const TILES = [
  { cls: "bg-fg", token: "text" },
  { cls: "bg-fg2", token: "text2" },
] as const;

/** A stable choice of tile color for a name: the same feed is the same color on every screen and every visit. */
function tileFor(name: string): string {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.codePointAt(0)!) >>> 0;
  return TILES[h % TILES.length]!.cls;
}

/**
 * A feed's icon in a fixed 16 px box, so the name beside it never shifts. A feed with no icon, or one that fails to
 * load, gets a tile with the first letter of its title in a color taken from the title; with no title either, an empty
 * box of the same size keeps names lined up.
 */
export function FeedIcon({ src, title, className }: { src: string | null | undefined; title?: string; className?: string }) {
  const [failed, setFailed] = useState<string | null>(null);
  const box = cn("size-4 shrink-0 rounded-sm", className);
  if (src && failed !== src) {
    return <img src={src} alt="" width={16} height={16} loading="lazy" onError={() => setFailed(src)} className={box} />;
  }
  const letter = title?.trim() ? [...title.trim()][0]!.toLocaleUpperCase() : "";
  if (!letter) return <span aria-hidden="true" className={box} />;
  return (
    // The letter is generated content, not text: it is decoration, and a letter in the DOM would be visible text that
    // the row's accessible name does not start with (axe "label-content-name-mismatch" on every tree item).
    <span
      aria-hidden="true"
      data-letter={letter}
      className={cn(box, tileFor(title!.trim()), "inline-flex items-center justify-center text-micro leading-none font-bold text-bg before:content-[attr(data-letter)]")}
    />
  );
}
