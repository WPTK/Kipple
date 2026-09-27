import { useId, useLayoutEffect, useRef } from "react";
import { cn } from "@/lib/cn";

interface Props<T extends string | number> {
  legend: string;
  hint?: string;
  value: T;
  options: readonly { value: T; label: string; disabled?: boolean }[];
  onChange: (v: T) => void;
  /** Long labels: let the options wrap onto a second row instead of squeezing into one. */
  wrap?: boolean;
  /**
   * Choose on purpose: the arrow keys only move focus, and Enter or Space (or a click) picks. Native radios pick on
   * every arrow press, which would fire onChange for each option passed on the way; wrong for a setting whose change
   * the server acts on at once.
   */
  explicit?: boolean;
}

/** The nearest ancestor that scrolls vertically, or null when the page itself does. */
export function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let n = el?.parentElement ?? null; n; n = n.parentElement) {
    const oy = getComputedStyle(n).overflowY;
    if ((oy === "auto" || oy === "scroll") && n.scrollHeight > n.clientHeight) return n;
  }
  return null;
}

/**
 * Named steps as a segmented control (native radios underneath, so arrow keys work, drawn as pressed
 * buttons). Picking a step can reflow the page above it (text size scales every rem, density resizes
 * the live preview), so the control's own position on screen is held: what you just clicked never
 * slides out from under the pointer or off the screen.
 */
export function Segmented<T extends string | number>({ legend, hint, value, options, onChange, wrap, explicit }: Props<T>) {
  const name = useId();
  const box = useRef<HTMLDivElement>(null);
  const inputs = useRef<(HTMLInputElement | null)[]>([]);
  const anchor = useRef<{ top: number; timer: ReturnType<typeof setTimeout> } | null>(null);

  useLayoutEffect(() => {
    const a = anchor.current;
    if (!a || !box.current) return;
    const delta = box.current.getBoundingClientRect().top - a.top;
    clearTimeout(a.timer);
    anchor.current = null;
    if (Math.abs(delta) >= 1) {
      const sc = scrollParent(box.current);
      if (sc) sc.scrollTop += delta;
    }
  }, [value]);

  const pick = (v: T) => {
    if (box.current) {
      if (anchor.current) clearTimeout(anchor.current.timer);
      anchor.current = {
        top: box.current.getBoundingClientRect().top,
        // A choice that changes nothing on screen must not leave a stale anchor for the next render.
        timer: setTimeout(() => (anchor.current = null), 400),
      };
    }
    onChange(v);
  };

  return (
    <fieldset className="min-w-0">
      <legend className="text-sm font-semibold">{legend}</legend>
      {hint ? <p className="mb-2 text-xs text-fg2">{hint}</p> : <div className="mb-2" />}
      <div ref={box} className={cn("flex w-full gap-px overflow-hidden rounded-xl border border-line bg-line", wrap && "flex-wrap")}>
        {options.map((o, i) => {
          const on = o.value === value;
          return (
            <label
              key={String(o.value)}
              data-pressed={on}
              className={cn(
                "relative flex min-h-11 min-w-0 flex-1 cursor-pointer items-center justify-center px-1 text-center text-xs font-medium select-none min-[380px]:text-sm",
                "has-[:focus-visible]:outline-2 has-[:focus-visible]:-outline-offset-2 has-[:focus-visible]:outline-accent",
                wrap && "basis-[30%]",
                o.disabled && "cursor-not-allowed opacity-50",
                on ? "bg-accent font-semibold text-bg shadow-[inset_0_2px_5px_rgb(0_0_0/0.35)]" : "bg-surface text-fg hover:bg-selection",
              )}
            >
              <input
                type="radio"
                name={name}
                value={String(o.value)}
                checked={on}
                disabled={o.disabled}
                ref={(el) => {
                  inputs.current[i] = el;
                }}
                onChange={() => pick(o.value)}
                onKeyDown={
                  explicit
                    ? (e) => {
                        const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
                        if (d) {
                          e.preventDefault();
                          inputs.current[(i + d + options.length) % options.length]?.focus();
                        } else if (e.key === "Enter") {
                          e.preventDefault();
                          if (!on) pick(o.value);
                        }
                      }
                    : undefined
                }
                className="sr-only-live"
              />
              {o.label}
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}
