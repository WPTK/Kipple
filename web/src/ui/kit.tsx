import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Dialog } from "radix-ui";
import { Collapsible } from "radix-ui";
import { ChevronDown, TriangleAlert } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "./button";
import { useLatestRef } from "@/lib/useLatestRef";

/**
 * Where the focus on `el` belongs: `el` itself, or, in an open menu, the button that opened the menu (the menu closes
 * and gives focus back to it). Null for nothing or the page itself, and for a menu that gives focus back to nothing
 * (`data-no-focus-return`: a row menu opened by a long press, with focus nowhere).
 */
export function focusOrigin(el: Element | null): HTMLElement | null {
  if (!(el instanceof HTMLElement) || el === document.body) return null;
  const menu = el.closest('[role="menu"]');
  if (menu?.hasAttribute("data-no-focus-return")) return null;
  return (menu?.id && document.querySelector<HTMLElement>(`[aria-controls="${CSS.escape(menu.id)}"]`)) || el;
}

/**
 * Props for a Radix `Dialog.Content` that is opened by state (a button, a key, a menu item) rather than by a
 * `Dialog.Trigger`. Radix gives focus back to the trigger on close and to nothing when there is none, so Escape would
 * drop the keyboard to the top of the page. This remembers what had focus when `open` turned true, read while rendering
 * (before the commit moves focus into the dialog, which an `autoFocus` field does before any effect can run), and
 * returns focus there. A dialog opened from a menu item returns focus to the button that opened the menu, since the
 * menu is gone by then. When the opener left the page while the dialog was open (a live update took its row out),
 * focus goes to the nearest focusable element around it that is still there, such as the list, not to the top of the
 * page. `onClosed` runs once focus is back: a change that takes the opener off the page (the editor muting the article
 * whose row opened it) waits for it, so the list can hand focus on from there.
 */
export function useReturnFocus(open: boolean, onClosed?: () => void) {
  const [wasOpen, setWasOpen] = useState(false);
  const [places, setPlaces] = useState<HTMLElement[]>([]);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setPlaces(focusPlaces(focusOrigin(document.activeElement)));
  }
  return {
    onCloseAutoFocus: (e: Event) => {
      // Around the opener only when focus has nowhere else to be: a caller may have put it somewhere on purpose.
      const a = document.activeElement;
      const lost = !a || a === document.body || !!a.closest('[role="dialog"]');
      const back = places.find((p, i) => p.isConnected && (i === 0 || lost));
      if (back) {
        e.preventDefault();
        back.focus();
      }
      onClosed?.();
    },
  };
}

/** `el` and the elements around it that take focus, nearest first. */
function focusPlaces(el: HTMLElement | null): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (let p = el?.parentElement; p && p !== document.body; p = p.parentElement) if (p.hasAttribute("tabindex")) out.push(p);
  return el ? [el, ...out] : out;
}

/**
 * A sheet opens with focus on its first control. One whose fields are still loading (a Skeleton in the body) would put
 * it on the footer's Cancel instead, ahead of the fields: the sheet itself takes focus then (its title is read), and
 * Tab goes to the first field once it is there.
 */
function holdWhileLoading(e: Event) {
  const sheet = e.currentTarget instanceof HTMLElement ? e.currentTarget : null;
  if (!sheet?.querySelector("[data-sheet-body] [data-skeleton]")) return;
  e.preventDefault();
  sheet.focus({ preventScroll: true });
}

/** A modal sheet: bottom-aligned on phones, centered on wide screens. Radix traps focus and restores it. */
export function Modal({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  size = "md",
  onClosed,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  /** lg: room for a form with a preview beside its fields (still a bottom sheet on a phone). */
  size?: "md" | "lg";
  /** Runs once the sheet has closed and given focus back (useReturnFocus). */
  onClosed?: () => void;
}) {
  const returnFocus = useReturnFocus(open, onClosed);
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <Dialog.Content
          {...returnFocus}
          onOpenAutoFocus={holdWhileLoading}
          className={cn(
            "fixed inset-x-0 bottom-0 z-50 mx-auto flex max-h-[92dvh] w-full flex-col rounded-t-2xl outline-none border border-line bg-bg text-fg shadow-xl min-[640px]:inset-y-auto min-[640px]:top-[6vh] min-[640px]:bottom-auto min-[640px]:rounded-2xl",
            size === "lg" ? "max-w-2xl min-[640px]:top-[3vh]" : "max-w-lg",
          )}
        >
          <div data-sheet-body className="min-h-0 flex-1 overflow-y-auto px-5 pt-5 pb-3">
            <Dialog.Title className="text-lg font-bold">{title}</Dialog.Title>
            {description ? <Dialog.Description className="mt-1 mb-3 text-sm text-fg2">{description}</Dialog.Description> : <Dialog.Description className="sr-only-live">{title}</Dialog.Description>}
            <div className="flex flex-col gap-4">{children}</div>
          </div>
          {footer ? <div className="pb-safe flex flex-wrap justify-end gap-2 border-t border-line px-5 py-3">{footer}</div> : null}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export const inputCls =
  "min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg placeholder:text-fg2 aria-[invalid=true]:border-danger";

/** A labelled control with optional help and an inline error, wired with aria-describedby. */
export function Field({
  label,
  help,
  error,
  children,
}: {
  label: string;
  help?: ReactNode;
  error?: string | null;
  children: (a: { id: string; "aria-describedby"?: string; "aria-invalid"?: boolean }) => ReactNode;
}) {
  const id = useId();
  const desc = [help ? `${id}-h` : "", error ? `${id}-e` : ""].filter(Boolean).join(" ") || undefined;
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-semibold">
        {label}
      </label>
      {children({ id, "aria-describedby": desc, "aria-invalid": error ? true : undefined })}
      {help ? (
        <p id={`${id}-h`} className="text-xs text-fg2">
          {help}
        </p>
      ) : null}
      {error ? (
        <p id={`${id}-e`} role="alert" className="flex items-start gap-1 text-sm text-danger">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>{error}</span>
        </p>
      ) : null}
    </div>
  );
}

/** An on/off switch (native checkbox with role switch) with a label and help text. */
export function Switch({
  label,
  help,
  checked,
  onChange,
  disabled,
  error,
}: {
  label: string;
  help?: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  error?: string | null;
}) {
  const id = useId();
  return (
    <div>
      <label htmlFor={id} className="relative flex min-h-11 cursor-pointer items-start gap-3 text-sm">
        <input
          id={id}
          type="checkbox"
          role="switch"
          checked={checked}
          disabled={disabled}
          aria-describedby={`${id}-h${error ? ` ${id}-e` : ""}`}
          onChange={(e) => onChange(e.target.checked)}
          className="peer sr-only-live"
        />
        <span
          aria-hidden="true"
          className={cn(
            "relative mt-0.5 h-7 w-12 shrink-0 rounded-full border border-line transition-colors peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accent",
            checked ? "bg-accent" : "bg-surface",
          )}
        >
          <span className={cn("absolute top-0.5 size-5 rounded-full border border-line bg-bg transition-[left]", checked ? "left-6" : "left-0.5")} />
        </span>
        <span className="min-w-0">
          <span className="font-semibold">{label}</span>
          {/* Shown, not spoken: the switch announces its own state, and this text would run onto its name. */}
          <span aria-hidden="true" className="ml-2 text-xs text-fg2">{checked ? "On" : "Off"}</span>
        </span>
      </label>
      {help ? (
        <p id={`${id}-h`} className="ml-15 -mt-1 text-xs text-fg2">
          {help}
        </p>
      ) : (
        <span id={`${id}-h`} hidden />
      )}
      {error ? (
        <p id={`${id}-e`} role="alert" className="ml-15 flex items-start gap-1 text-sm text-danger">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>{error}</span>
        </p>
      ) : null}
    </div>
  );
}

/**
 * A checkbox with a label and help text: the control for a yes/no choice in a form or dialog that a button (Save, Add,
 * Export, Continue) applies later. A Switch is for a setting that takes effect the moment it is flipped; the two
 * differ the way they do on iOS, Android and the web, so a form never offers a switch that does nothing until Save.
 */
export function Checkbox({
  label,
  help,
  checked,
  onChange,
  disabled,
  busy,
  error,
}: {
  label: string;
  help?: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  /** A change is being worked out (a preview is loading): announced, and the box stays where it is. */
  busy?: boolean;
  error?: string | null;
}) {
  const id = useId();
  // The box and the text are one label, so a tap anywhere on the row toggles it. The row is 44 px (56 px under Large
  // targets, `--check-row` in index.css); the box and the first line of text are each centred in that row height, so
  // they line up at both sizes and for a label that wraps. The help sits under the label, indented to the text, and is
  // not part of the name.
  return (
    <div className={cn(disabled && "opacity-60")}>
      <label
        htmlFor={id}
        aria-busy={busy || undefined}
        className={cn("kp-check flex min-h-11 items-start gap-3 text-sm", disabled ? "cursor-not-allowed" : "cursor-pointer")}
      >
        <span className="flex h-[var(--check-row)] shrink-0 items-center">
          <input
            id={id}
            type="checkbox"
            checked={checked}
            disabled={disabled}
            aria-invalid={error ? true : undefined}
            aria-describedby={`${id}-h${error ? ` ${id}-e` : ""}`}
            onChange={(e) => onChange(e.target.checked)}
            className="size-5 accent-[var(--kp-accent)]"
          />
        </span>
        <span className="flex min-h-[var(--check-row)] min-w-0 items-center leading-5">{label}</span>
      </label>
      {help ? (
        <p id={`${id}-h`} className="pb-2 pl-8 text-xs text-fg2">
          {help}
        </p>
      ) : (
        <span id={`${id}-h`} hidden />
      )}
      {error ? (
        <p id={`${id}-e`} role="alert" className="flex items-start gap-1 pb-2 pl-8 text-sm text-danger">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>{error}</span>
        </p>
      ) : null}
    </div>
  );
}

/** Wait after the last keystroke or +/- click before a value is committed (blur and Enter commit at once). */
export const STEPPER_DEBOUNCE_MS = 700;

/** Why `raw` is not a usable value, or null when it is. */
export function stepperProblem(raw: string, min: number | undefined, max: number | undefined, step: number): string | null {
  const range = min !== undefined && max !== undefined ? ` from ${min} to ${max}` : min !== undefined ? ` of at least ${min}` : max !== undefined ? ` of at most ${max}` : "";
  const n = raw.trim() === "" ? NaN : Number(raw);
  const whole = Number.isInteger(step);
  if (!Number.isFinite(n) || (whole && !Number.isInteger(n))) return `Enter a ${whole ? "whole " : ""}number${range}.`;
  if ((min !== undefined && n < min) || (max !== undefined && n > max)) return `Enter a number${range || " in range"}.`;
  return null;
}

/**
 * A number with minus and plus buttons; typing works too. Not a slider. `onChange` only ever receives a
 * valid, in-range value, and only once the user has finished: on blur or Enter, or 700 ms after the last
 * keystroke or button click. Typing "120" therefore commits 120, not 1 and 12 on the way.
 */
export function Stepper({
  label,
  value,
  min,
  max,
  step = 1,
  unit,
  onChange,
  describedBy,
  invalid,
}: {
  label: string;
  value: number;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  onChange: (n: number) => void;
  describedBy?: string;
  invalid?: boolean;
}) {
  const clamp = (n: number) => Math.min(max ?? Infinity, Math.max(min ?? -Infinity, n));
  const [text, setText] = useState(String(value));
  const [problem, setProblem] = useState<string | null>(null);
  const latest = useLatestRef({ value, onChange, text });
  // The saved value changed under us (a reset, a rollback): show it.
  const [seen, setSeen] = useState(value);
  if (seen !== value) {
    setSeen(value);
    setText(String(value));
    setProblem(null);
  }
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const pending = useRef(false);

  const commit = (raw: string) => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = undefined;
    pending.current = false;
    const bad = stepperProblem(raw, min, max, step);
    setProblem(bad);
    if (bad) return;
    const n = Number(raw);
    if (n !== latest.current.value) latest.current.onChange(n);
  };
  const schedule = (raw: string) => {
    if (timer.current) clearTimeout(timer.current);
    pending.current = true;
    timer.current = setTimeout(() => commit(raw), STEPPER_DEBOUNCE_MS);
  };
  // Leaving the screen inside the debounce window must not drop the edit.
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
      if (pending.current && stepperProblem(latest.current.text, min, max, step) === null && Number(latest.current.text) !== latest.current.value) {
        latest.current.onChange(Number(latest.current.text));
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  const bump = (dir: 1 | -1) => {
    const cur = stepperProblem(text, min, max, step) === null ? Number(text) : value;
    const n = clamp(cur + dir * step);
    setText(String(n));
    setProblem(null);
    schedule(String(n));
  };
  const current = stepperProblem(text, min, max, step) === null ? Number(text) : value;
  const errId = problem ? `${describedBy ?? "stepper"}-local` : undefined;
  return (
    <>
      <div className="flex items-center gap-2" role="group" aria-label={label}>
        <Button size="icon" aria-label={`Decrease ${label}`} disabled={min !== undefined && current <= min} onClick={() => bump(-1)}>
          <span aria-hidden="true">−</span>
        </Button>
        <input
          aria-label={label}
          aria-describedby={[describedBy, errId].filter(Boolean).join(" ") || undefined}
          aria-invalid={invalid || !!problem || undefined}
          inputMode="numeric"
          type="number"
          min={min}
          max={max}
          step={step}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setProblem(null);
            schedule(e.target.value);
          }}
          onBlur={() => commit(text)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit(text);
            }
          }}
          className={cn(inputCls, "w-24 text-center tabular-nums [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none")}
        />
        <Button size="icon" aria-label={`Increase ${label}`} disabled={max !== undefined && current >= max} onClick={() => bump(1)}>
          <span aria-hidden="true">+</span>
        </Button>
        {unit ? <span className="text-sm text-fg2">{unit}</span> : null}
      </div>
      {problem ? (
        <p id={errId} role="alert" className="mt-1 text-sm text-danger">
          {problem}
        </p>
      ) : null}
    </>
  );
}

/** A disclosure section. */
export function Disclosure({ label, children, defaultOpen = false, tone }: { label: string; children: ReactNode; defaultOpen?: boolean; tone?: "warn" }) {
  return (
    <Collapsible.Root defaultOpen={defaultOpen} className="rounded-xl border border-line">
      <Collapsible.Trigger className="group flex min-h-11 w-full items-center justify-between gap-2 rounded-xl px-3 text-left text-sm font-semibold">
        <span className="flex items-center gap-2">
          {tone === "warn" ? <TriangleAlert aria-hidden="true" className="size-4 text-danger" /> : null}
          {label}
        </span>
        <ChevronDown aria-hidden="true" className="size-5 transition-transform group-data-[state=open]:rotate-180" />
      </Collapsible.Trigger>
      <Collapsible.Content className="flex flex-col gap-4 px-3 pt-1 pb-3">{children}</Collapsible.Content>
    </Collapsible.Root>
  );
}

/** Empty, error and loading blocks share one look. */
export function Notice({ tone = "info", children, role }: { tone?: "info" | "warn" | "error"; children: ReactNode; role?: "status" | "alert" }) {
  return (
    <div
      role={role ?? (tone === "error" ? "alert" : "status")}
      className={cn("flex items-start gap-2 rounded-xl border px-3 py-2 text-sm", tone === "info" ? "border-line bg-surface text-fg" : "border-danger bg-surface text-fg")}
    >
      {tone !== "info" ? <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-danger" /> : null}
      <div className="min-w-0">{children}</div>
    </div>
  );
}

export function Skeleton({ rows = 4, label = "Loading" }: { rows?: number; label?: string }) {
  return (
    <div role="status" aria-busy="true" data-skeleton className="flex flex-col gap-3 p-4">
      <span className="sr-only-live">{label}</span>
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} aria-hidden="true" className="h-12 animate-pulse rounded-xl bg-surface" />
      ))}
    </div>
  );
}
