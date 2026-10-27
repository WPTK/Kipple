import type { LayoutId } from "@/lib/devicePrefs";

/** Each layout drawn as a few bars in a small frame: [left, top, width, height] in a 24 x 18 box. Decorative: the row's text is the name. */
const BARS: Record<LayoutId, readonly (readonly [number, number, number, number])[]> = {
  compact: [[3, 4, 18, 2], [3, 9, 18, 2], [3, 14, 18, 2]],
  headlines: [[3, 4, 14, 3], [3, 10, 18, 3], [3, 16, 11, 3]],
  inbox: [[3, 3, 5, 5], [11, 4, 11, 2], [3, 11, 5, 5], [11, 12, 11, 2]],
  magazine: [[3, 3, 20, 9], [3, 15, 14, 2]],
  cards: [[3, 3, 9, 6], [14, 3, 9, 6], [3, 11, 9, 6], [14, 11, 9, 6]],
  gazette: [[3, 3, 9, 12], [14, 3, 9, 5], [14, 10, 9, 5]],
  digest: [[3, 3, 20, 2], [3, 7, 14, 2], [3, 12, 20, 2], [3, 16, 14, 2]],
};

/** A small picture of what a layout looks like, for the layout menu. Hidden from assistive technology. */
export function LayoutGlyph({ id }: { id: LayoutId }) {
  return (
    <svg width="26" height="20" viewBox="0 0 26 20" aria-hidden="true" focusable="false" className="shrink-0 text-fg2" data-layout-glyph={id}>
      <rect x="0.5" y="0.5" width="25" height="19" rx="3" fill="none" stroke="currentColor" />
      {BARS[id].map(([l, t, w, h]) => (
        <rect key={`${l}-${t}`} x={l + 1} y={t + 1} width={w} height={h} fill="currentColor" opacity="0.7" />
      ))}
    </svg>
  );
}
