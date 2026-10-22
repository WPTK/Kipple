import { useEffect, useRef, type ReactNode } from "react";

/**
 * Every empty, failed or missing screen: what this is (the title), why it is like this (one sentence) and the next step
 * (the action). The action is required so each screen has to decide; a screen with nothing to offer passes `null`.
 * An alert moves focus to its headline, which carries no focus box.
 */
export function EmptyState({
  title,
  why,
  action,
  role = "status",
  asH1,
}: {
  title: string;
  why: string;
  /** The next step: a button or a link. `null` when there truly is none. */
  action: Exclude<ReactNode, undefined>;
  role?: "status" | "alert";
  /** A screen of its own (nothing else names it): the title is the page's h1 rather than an h2 under one. */
  asH1?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const h1 = useRef<HTMLHeadingElement>(null);
  // Empty and error screens move focus to the headline (design section 6): the h1 where the block is the page's own
  // heading, else the block, which the alert role reads out.
  useEffect(() => {
    if (role === "alert") (h1.current ?? ref.current)?.focus({ preventScroll: true });
  }, [role]);
  return (
    <div ref={ref} tabIndex={-1} role={role} className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center outline-none">
      {asH1 ? (
        <h1 ref={h1} tabIndex={-1} className="text-lg font-semibold outline-none">
          {title}
        </h1>
      ) : (
        <h2 className="text-lg font-semibold">{title}</h2>
      )}
      <p className="text-sm text-fg2">{why}</p>
      {action}
    </div>
  );
}
