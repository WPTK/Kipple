import { useState } from "react";
import { cn } from "@/lib/cn";

// The scheme colors a letter tile may take: each is held to 3:1 against the page in every scheme (contrast.mjs), and the letter is the page color.
const TILES = ["bg-accent", "bg-star", "bg-danger", "bg-link", "bg-unread"] as const;

/** A stable choice of tile color for a name: the same feed is the same color on every screen and every visit. */
function tileFor(name: string): string {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.codePointAt(0)!) >>> 0;
  return TILES[h % TILES.length]!;
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
    <span aria-hidden="true" className={cn(box, tileFor(title!.trim()), "inline-flex items-center justify-center text-micro leading-none font-bold text-bg")}>
      {letter}
    </span>
  );
}
