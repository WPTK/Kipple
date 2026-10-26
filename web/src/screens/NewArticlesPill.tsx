import { useState } from "react";

/**
 * "N new articles". It is always mounted and fades and slides on transform and opacity only (compositor
 * work: nothing in the list reflows, and the list is never pushed down), and it keeps the last count while
 * it fades out so the text does not blank. Reduced motion is honoured by the global rule in index.css.
 */
export function NewArticlesPill({ count, onClick }: { count: number; onClick: () => void }) {
  const [n, setN] = useState(count);
  if (count > 0 && count !== n) setN(count);
  const open = count > 0;
  return (
    <div className="pointer-events-none absolute inset-x-0 top-2 z-20 flex justify-center" data-testid="new-pill-wrap">
      <button
        type="button"
        className="kp-pill pointer-events-auto min-h-11 rounded-full bg-accent px-5 text-sm font-medium text-bg tabular-nums shadow-lg"
        data-open={open}
        aria-hidden={!open}
        tabIndex={open ? 0 : -1}
        onClick={onClick}
      >
        {n} new article{n === 1 ? "" : "s"}
      </button>
    </div>
  );
}
