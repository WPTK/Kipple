import { useEffect, useId, useRef, useState, type InputHTMLAttributes, type ReactNode, type Ref } from "react";
import { Dialog } from "radix-ui";
import { Collapsible } from "radix-ui";
import { ChevronDown, CircleAlert, TriangleAlert, X } from "lucide-react";
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
        <Dialog.Overlay className="fixed inset-0 z-40 bg-scrim" />
        <Dialog.Content
          {...returnFocus}
          onOpenAutoFocus={holdWhileLoading}
          className={cn(
            "fixed inset-x-0 bottom-0 z-50 mx-auto flex max-h-[92dvh] w-full flex-col rounded-t-2xl outline-none border border-line bg-bg text-fg shadow-sheet min-[640px]:inset-y-auto min-[640px]:top-[6vh] min-[640px]:bottom-auto min-[640px]:rounded-2xl",
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

/** A text field, select or textarea. Its boundary is the scheme's field color (3:1 against the page), not the decorative line color. */
export const inputCls =
  "min-h-11 w-full rounded-lg border border-field bg-surface px-3 text-base text-fg placeholder:text-fg2 aria-[invalid=true]:border-danger";

/**
 * The one way a control says what is wrong with its value: a single message under the control, with an icon (so it is
 * not colour alone), announced when it appears. `id` is what the control's aria-describedby names. A control shows at
 * most one message; the form's own Notice is for a failure that belongs to no one field.
 */
export function FieldError({ id, children, className }: { id: string; children: ReactNode; className?: string }) {
  return (
    <p id={id} role="alert" className={cn("flex items-start gap-1 text-sm text-danger", className)}>
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <span>{children}</span>
    </p>
  );
}

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
      {error ? <FieldError id={`${id}-e`}>{error}</FieldError> : null}
    </div>
  );
}

/**
 * A password input with a Show control: one field for every password in the app (sign in, set, change, confirm). It is
 * asked for once, with the Show control for checking what was typed, so there is no second "again" field to fill in.
 */
export function PasswordField({
  label,
  help,
  error,
  value,
  onChange,
  autoComplete,
  inputRef,
  ...rest
}: {
  label: string;
  help?: ReactNode;
  error?: string | null;
  value: string;
  onChange: (v: string) => void;
  autoComplete: "current-password" | "new-password" | "off";
  inputRef?: Ref<HTMLInputElement>;
} & Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type" | "autoComplete" | "className" | "id" | "ref">) {
  const [shown, setShown] = useState(false);
  // Named after its field when the label ends in "password" ("Show new password"), so two fields in one dialog have two
  // different Show buttons.
  const what = /password$/i.test(label) ? label.replace(/^./, (c) => c.toLowerCase()) : "password";
  return (
    <Field label={label} help={help} error={error}>
      {(a) => (
        <div className="relative">
          <input {...a} {...rest} ref={inputRef} type={shown ? "text" : "password"} autoComplete={autoComplete} autoCapitalize="none" spellCheck={false} value={value} onChange={(e) => onChange(e.target.value)} className={cn(inputCls, "pr-16")} />
          <button
            type="button"
            aria-label={`${shown ? "Hide" : "Show"} ${what}`}
            aria-controls={a.id}
            disabled={rest.disabled}
            onClick={() => setShown((s) => !s)}
            className="absolute inset-y-0 right-0 inline-flex min-w-11 items-center justify-center rounded-lg px-3 text-sm font-medium text-link disabled:opacity-50"
          >
            {shown ? "Hide" : "Show"}
          </button>
        </div>
      )}
    </Field>
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
            "relative mt-0.5 h-7 w-12 shrink-0 rounded-full border border-field transition-colors peer-focus-visible:kp-ring",
            checked ? "bg-accent" : "bg-surface",
          )}
        >
          <span className={cn("absolute top-0.5 size-5 rounded-full border border-field bg-bg transition-[left]", checked ? "left-6" : "left-0.5")} />
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
      {error ? <FieldError id={`${id}-e`} className="ml-15">{error}</FieldError> : null}
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
      {error ? <FieldError id={`${id}-e`} className="pb-2 pl-8">{error}</FieldError> : null}
    </div>
  );
}

/** A chosen card (a radio card, a theme card): the selection fill and a 2 px accent ring, so the choice is not colour alone. */
export const selectedCard = "border-accent bg-selection ring-1 ring-accent";

const radioBox = "size-5 shrink-0 accent-[var(--kp-accent)]";

/**
 * One option of a group of radios that each want a sentence: a bordered card with a title, an optional line of help
 * and, when the choice needs more room, content below it. Put the cards in a `<fieldset>` with a `<legend>`; cards that
 * share a `name` are one native radio group (arrow keys move between them, the form value is `value`).
 *
 * `help` is part of the option's name. `children` sit under the title, indented to its text, outside the label and
 * described by it (a description, or fields that belong to the option); a card with children also has the larger title.
 * The whole card is one hit area (44 px tall, 56 px with Large targets, via `label.min-h-11` in index.css). With children
 * the label cannot wrap them (they may hold fields), so the card forwards a click on its padding or its text to the radio
 * and leaves a click on a field, button or link in the children alone.
 */
export function RadioCard({
  name,
  value,
  checked,
  onSelect,
  disabled,
  title,
  help,
  children,
}: {
  name: string;
  value?: string;
  checked: boolean;
  onSelect: () => void;
  disabled?: boolean;
  title: ReactNode;
  help?: ReactNode;
  children?: ReactNode;
}) {
  const id = useId();
  const radio = useRef<HTMLInputElement>(null);
  const roomy = children != null && children !== false;
  const card = cn(
    "rounded-xl border p-3 has-[:focus-visible]:kp-ring",
    checked ? selectedCard : "border-field bg-surface",
    disabled ? "opacity-60" : "hover:bg-selection",
  );
  const label = cn("flex min-h-11 items-start gap-3", disabled ? "cursor-not-allowed" : "cursor-pointer");
  const input = (
    <input
      ref={radio}
      id={id}
      type="radio"
      name={name}
      value={value}
      checked={checked}
      disabled={disabled}
      aria-describedby={roomy ? `${id}-d` : undefined}
      onChange={onSelect}
      className={cn(radioBox, roomy ? "mt-1" : "mt-0.5")}
    />
  );
  const text = (
    <span className="min-w-0">
      <span className={cn("font-semibold", roomy && "text-base")}>{title}</span>
      {help ? <span className="block text-xs text-fg2">{help}</span> : null}
    </span>
  );
  if (!roomy) {
    return (
      <label htmlFor={id} className={cn(card, label, "text-sm")}>
        {input}
        {text}
      </label>
    );
  }
  return (
    // The radio and its label stay the keyboard and screen reader control; this click is for the pointer only.
    <div
      className={cn(card, "text-sm", !disabled && "cursor-pointer")}
      onClick={(e) => {
        if (disabled || (e.target as HTMLElement).closest("input, select, textarea, button, a, label, summary, [contenteditable], [role=button], [tabindex]")) return;
        radio.current?.click();
      }}
    >
      <label htmlFor={id} className={label}>
        {input}
        {text}
      </label>
      <div id={`${id}-d`} className="ml-8 flex flex-col gap-3 text-fg2">
        {children}
      </div>
    </div>
  );
}

/** One plain radio with its label, for a short choice that needs no card (a few options in a dialog). */
export function RadioRow({ name, value, checked, onSelect, disabled, children }: { name: string; value?: string; checked: boolean; onSelect: () => void; disabled?: boolean; children: ReactNode }) {
  return (
    <label className={cn("flex min-h-11 items-center gap-3", disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer")}>
      <input type="radio" name={name} value={value} checked={checked} disabled={disabled} onChange={onSelect} className={radioBox} />
      {children}
    </label>
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
      {problem ? <FieldError id={errId!} className="mt-1">{problem}</FieldError> : null}
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

/**
 * A titled section of a screen (Settings, Statistics): a heading that names the region, a rule under it and the content.
 * `level` is the heading level the page's outline needs; `sub` is a section inside another (a rule above, a smaller
 * heading); `stack` lays the content out as a column of settings.
 */
export function Section({ title, children, level = 2, sub = false, stack = false }: { title: string; children: ReactNode; level?: 2 | 3; sub?: boolean; stack?: boolean }) {
  const id = useId();
  const H = sub || level === 3 ? "h3" : "h2";
  return (
    <section aria-labelledby={id} className={sub ? "border-t border-line pt-4" : "border-b border-line py-5"}>
      <H id={id} className={cn("mb-3 font-semibold", sub ? "text-base" : "text-lg")}>
        {title}
      </H>
      {stack ? <div className="flex flex-col gap-5">{children}</div> : children}
    </section>
  );
}

/** The look of a chip: a pill with a boundary, 44 px tall, for a filter that is on or a term in a list. */
export const chipCls = "inline-flex min-h-11 max-w-full items-center gap-1.5 rounded-full border border-field bg-surface text-sm";

/** A removable chip: the word, and a button that takes it away. */
export function Chip({ children, removeLabel, onRemove, className }: { children: ReactNode; removeLabel: string; onRemove: () => void; className?: string }) {
  return (
    <span className={cn(chipCls, "gap-1 pr-1 pl-3", className)}>
      <span className="min-w-0 truncate">{children}</span>
      <button type="button" aria-label={removeLabel} onClick={onRemove} className="hit-row inline-flex shrink-0 items-center justify-center rounded-full hover:bg-selection">
        <X aria-hidden="true" className="size-4" />
      </button>
    </span>
  );
}

/** Empty, error and loading blocks share one look. */
export function Notice({ tone = "info", children, role }: { tone?: "info" | "warn" | "error"; children: ReactNode; role?: "status" | "alert" }) {
  return (
    <div
      role={role ?? (tone === "error" ? "alert" : "status")}
      data-tone={tone}
      className={cn(
        "flex items-start gap-2 rounded-xl border px-3 py-2 text-sm bg-surface text-fg",
        tone === "info" ? "border-line" : tone === "warn" ? "border-accent" : "border-danger",
      )}
    >
      {tone === "warn" ? <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-accent" /> : null}
      {tone === "error" ? <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-danger" /> : null}
      <div className="min-w-0">{children}</div>
    </div>
  );
}

/**
 * Placeholder rows while something loads. Where the loaded content's size is known, pass it so nothing below jumps:
 * `rows` as the height class of each row (measured from the real row), and `className` for the box around them.
 */
export function Skeleton({ rows = 4, label = "Loading", className }: { rows?: number | readonly string[]; label?: string; className?: string }) {
  const heights = typeof rows === "number" ? Array.from({ length: rows }, () => "h-12") : rows;
  return (
    <div role="status" aria-busy="true" data-skeleton className={cn("flex flex-col gap-3 p-4", className)}>
      <span className="sr-only-live">{label}</span>
      {heights.map((h, i) => (
        <div key={i} aria-hidden="true" className={cn("animate-pulse rounded-xl bg-surface", h)} />
      ))}
    </div>
  );
}