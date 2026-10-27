import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * The title row every main screen's header shares: the title on the left, the controls on the right. The title wraps to
 * two lines and keeps at least 6.5rem; when two lines are still not enough, the controls drop to a row of their own and
 * the title takes the whole width, so a long name is not cut for the sake of the buttons. The title is the heading's
 * text (its accessible name) and its tooltip.
 *
 * Whether the title is cut is read from the layout (the browser clamps it), once per title and row width: the controls
 * dropping gives the title more room, which must not hand the room back and bring the controls up again. Where nothing is
 * laid out (a test without layout) the controls stay beside the title.
 */
export function TitleRow({
  title,
  as: Heading = "h1",
  children,
  className,
  before,
  after,
  label,
  id,
  leading,
  middle,
  tight,
}: {
  title: string;
  as?: "h1" | "h2";
  /** The controls, laid out as a row at the right. */
  children?: ReactNode;
  className?: string;
  /** Small text above the title (the folder a feed is in). */
  before?: ReactNode;
  /** Beside the title, after it (the unread count). */
  after?: ReactNode;
  /** The heading's accessible name when it is not the title alone. */
  label?: string;
  id?: string;
  /** Before the title, inside the title's share of the row (a back button). */
  leading?: ReactNode;
  /** Items on the title's row between the title and the controls, which stay on it (a short screen has no room for a second row). */
  middle?: ReactNode;
  /** The title may shrink to 3rem (a short, narrow screen whose row also holds a select and the controls). */
  tight?: boolean;
}) {
  const row = useRef<HTMLDivElement>(null);
  const text = useRef<HTMLSpanElement>(null);
  const natural = useRef<HTMLSpanElement>(null);
  // What the last measure depended on: the row's width, the title's natural width on one line (the probe below; it follows
  // the text size and a font swap) and the row's content (a count or a control appearing). Dropping the controls changes
  // none of them (it only sets a class), so a new value starts again with the controls beside the title and the measure
  // cannot flip back and forth.
  const [env, setEnv] = useState({ width: 0, natural: 0, content: 0 });
  const key = `${title}|${env.width}|${env.natural}|${env.content}`;
  const [measured, setMeasured] = useState<string | null>(null);
  const dropped = measured === key;
  useLayoutEffect(() => {
    const el = row.current;
    const probe = natural.current;
    if (!el || !probe || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() =>
      setEnv((e) => {
        const width = Math.round(el.clientWidth);
        const nat = Math.round(probe.getBoundingClientRect().width);
        return e.width === width && e.natural === nat ? e : { ...e, width, natural: nat };
      }),
    );
    ro.observe(el);
    ro.observe(probe);
    const mo = typeof MutationObserver === "undefined" ? null : new MutationObserver(() => setEnv((e) => ({ ...e, content: e.content + 1 })));
    mo?.observe(el, { childList: true, subtree: true, characterData: true });
    return () => {
      ro.disconnect();
      mo?.disconnect();
    };
  }, []);
  useLayoutEffect(() => {
    const el = text.current;
    // A box with no height has not been laid out: nothing to read.
    if (!dropped && el && el.clientHeight > 0 && el.scrollHeight > el.clientHeight + 1) setMeasured(key);
  }, [dropped, key]);
  return (
    <div ref={row} data-title-row className={cn("flex w-full max-w-full flex-wrap items-center gap-x-0.5 pt-2", className)}>
      <div className={cn("flex flex-1 gap-1", tight ? "min-w-[min(100%,3rem)]" : "min-w-[min(100%,6.5rem)]", leading ? "items-center" : "items-baseline")}>
        {leading}
        <Heading id={id} className="min-w-0 text-xl font-bold [overflow-wrap:anywhere]" tabIndex={-1} data-route-heading title={dropped ? title : undefined} aria-label={label}>
          {before}
          <span ref={text} className="line-clamp-2">
            {title}
          </span>
          {/* The title on one line, unseen: its width is the title's own size, whatever room the row gives it. */}
          <span ref={natural} data-t={title} aria-hidden="true" className="pointer-events-none invisible absolute h-0 overflow-hidden whitespace-nowrap before:content-[attr(data-t)]" />
        </Heading>
        {after}
      </div>
      {middle}
      {children ? <div className={cn("ml-auto flex max-w-full flex-wrap items-center justify-end gap-0.5", dropped && "w-full")}>{children}</div> : null}
    </div>
  );
}
