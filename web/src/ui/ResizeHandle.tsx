import { useRef, useState } from "react";
import { cn } from "@/lib/cn";

/** Width after a drag: the width at the start plus the pointer's travel, kept inside [min, max]. */
export function dragWidth(start: number, dx: number, min: number, max: number, dir: 1 | -1 = 1): number {
  return Math.min(max, Math.max(min, Math.round(start + dir * dx)));
}

/** Width after a key: arrows move by 16 px (48 with Shift), Home and End jump to the ends; null for other keys. */
export function keyWidth(key: string, shift: boolean, current: number, min: number, max: number): number | null {
  const step = shift ? 48 : 16;
  if (key === "ArrowLeft" || key === "ArrowUp") return Math.max(min, current - step);
  if (key === "ArrowRight" || key === "ArrowDown") return Math.min(max, current + step);
  if (key === "Home") return min;
  if (key === "End") return max;
  return null;
}

/**
 * A vertical drag handle on the right edge of a column (the parent must be `relative`). Pointer drag and the
 * arrow keys resize it; Home and End go to the limits; double-click resets. It is a WAI-ARIA window splitter
 * (`role="separator"` with a value), so a screen reader announces the width.
 */
export function ResizeHandle({
  label,
  value,
  min,
  max,
  onChange,
  onReset,
  className,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (px: number) => void;
  onReset?: () => void;
  className?: string;
}) {
  const start = useRef<{ x: number; w: number } | null>(null);
  const [active, setActive] = useState(false);
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={Math.round(value)}
      tabIndex={0}
      title="Drag to resize. Double-click to reset."
      data-active={active || undefined}
      className={cn(
        "group absolute inset-y-0 -right-1.5 z-30 w-3 cursor-col-resize touch-none outline-none",
        className,
      )}
      onPointerDown={(e) => {
        if (e.button !== 0) return;
        start.current = { x: e.clientX, w: value };
        setActive(true);
        try {
          e.currentTarget.setPointerCapture(e.pointerId);
        } catch {
          /* synthetic pointer */
        }
      }}
      onPointerMove={(e) => {
        if (start.current) onChange(dragWidth(start.current.w, e.clientX - start.current.x, min, max));
      }}
      onPointerUp={() => {
        start.current = null;
        setActive(false);
      }}
      onPointerCancel={() => {
        start.current = null;
        setActive(false);
      }}
      onDoubleClick={() => onReset?.()}
      onKeyDown={(e) => {
        const w = keyWidth(e.key, e.shiftKey, value, min, max);
        if (w === null) return;
        e.preventDefault();
        onChange(w);
      }}
    >
      <span
        aria-hidden="true"
        className="absolute inset-y-0 left-1/2 w-0.5 -translate-x-1/2 bg-transparent group-hover:bg-accent group-focus-visible:bg-accent group-data-[active]:bg-accent"
      />
    </div>
  );
}
