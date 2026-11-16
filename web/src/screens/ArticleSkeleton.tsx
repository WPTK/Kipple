import type { CSSProperties } from "react";
import { ARTICLE_WIDTH_REM, useDevicePrefs } from "@/lib/devicePrefs";
import { Bone, BoneLines } from "@/ui/kit";
import { ListImage, useImageFailure } from "@/layouts/parts";
import type { Card } from "@/api/types";

/** The article column's max width for the "Article width" setting; Medium is undefined: the stylesheet gives it the density's own measure (.kp-article). */
export function columnWidth(w: keyof typeof ARTICLE_WIDTH_REM): string | undefined {
  const rem = ARTICLE_WIDTH_REM[w];
  return w === "medium" ? undefined : rem === null ? "100%" : `${rem}rem`;
}

/**
 * The classes of the article's own header, shared by the open article and the one still loading, so the two take the
 * same room whatever the text size and the reading density: nothing moves when the article arrives.
 */
export const ARTICLE_FRAME = "kp-article px-4 pt-4 pb-10 md:px-6";
export const ARTICLE_HEADER = "kp-ui mx-auto mb-5 max-w-[min(var(--kp-col),100%)]";
export const ARTICLE_TITLE = "mt-1 text-title leading-tight font-[650] text-balance wrap-anywhere outline-none [font-family:var(--kp-reading-font)] [font-size-adjust:0.5]";

/**
 * What an article shows while its text loads: the real article's frame, header and column with bars where the text will
 * be. What the list already knew of it (title, source, lead picture) is drawn in the header's own type, so a title wraps
 * as it will; with no list behind it the title and source are bars of the same heights. The body starts with the lead
 * picture's place when the list had one, in the reader column's width.
 */
export function ArticleSkeleton({ card }: { card?: Card }) {
  const dp = useDevicePrefs();
  const failure = useImageFailure(card?.image);
  return (
    <article aria-hidden="true" className={ARTICLE_FRAME} style={{ "--kp-col": columnWidth(dp.articleWidth) } as CSSProperties}>
      <header className={ARTICLE_HEADER}>
        <div dir="auto" className="text-sm text-fg2">
          {card ? card.source || " " : <Bone className="h-[1lh] w-1/3" />}
        </div>
        {card ? (
          <div data-testid="loading-card-title" dir="auto" className={`${ARTICLE_TITLE} text-fg`}>
            {card.title || "Untitled"}
          </div>
        ) : (
          <BoneLines lines={2} className={ARTICLE_TITLE} />
        )}
        <div className="mt-2 text-sm text-fg2">
          <Bone className="h-[1lh] w-2/5" />
        </div>
      </header>
      <div className="article-body">
        {card?.image ? <ListImage src={card.image} failure={failure} className="mb-[var(--kp-pgap)] aspect-video max-h-[min(70dvh,40rem)] w-full rounded-lg bg-surface" /> : null}
        <BoneLines lines={4} className="mb-[var(--kp-pgap)]" />
        <BoneLines lines={3} />
      </div>
    </article>
  );
}
