import { useEffect, useState, type ReactNode } from "react";
import { CircleAlert, X } from "lucide-react";
import { createStore, useStore } from "@/lib/store";
import { dismissUndoToast, holdUndoToast, undoStore, undoToast } from "@/lib/undo";

export interface ToastItem {
  id: number;
  message: string;
  kind: "info" | "error";
  action?: { label: string; run: () => void };
  /** What the toast is about ("star", "read"): a later success on the same topic clears its error. */
  topic?: string;
}

const toasts = createStore<ToastItem[]>([]);
let seq = 0;

/** Polite live-region announcement (new items, refresh results, star state). */
const announcement = createStore<{ n: number; message: string }>({ n: 0, message: "" });

export function announce(message: string): void {
  announcement.set((a) => ({ n: a.n + 1, message }));
}

/** Errors are announced assertively, through their own persistent region. */
const alertAnnouncement = createStore<{ n: number; message: string }>({ n: 0, message: "" });

export function toast(message: string, kind: ToastItem["kind"] = "info", action?: ToastItem["action"], topic?: string): number {
  const id = ++seq;
  // The visible toast is not itself a live region: a freshly mounted role=status/alert node is
  // often not announced on iOS VoiceOver. The persistent regions in <LiveRegion> speak instead.
  if (kind === "error") alertAnnouncement.set((a) => ({ n: a.n + 1, message }));
  else announce(message);
  // One toast per kind at a time: a newer info or error replaces the older one instead of stacking under it.
  toasts.set((t) => [...t.filter((x) => x.kind !== kind), { id, message, kind, action, topic }]);
  return id;
}

export function dismissToast(id: number): void {
  toasts.set((t) => t.filter((x) => x.id !== id));
}

/** The change on `topic` went through: drop an error toast it left behind. */
export function settleToasts(topic: string): void {
  toasts.set((t) => (t.some((x) => x.kind === "error" && x.topic === topic) ? t.filter((x) => !(x.kind === "error" && x.topic === topic)) : t));
}

export function clearToasts(): void {
  toasts.set([]);
  announcement.set({ n: 0, message: "" });
  alertAnnouncement.set({ n: 0, message: "" });
}

/** Space the toasts leave at the bottom for the chrome that is showing (tab bar, article toolbar, none). */
export type ToastInset = "tabbar" | "toolbar" | "none";
const INSET: Record<ToastInset, string> = {
  tabbar: "var(--tabbar-h)",
  toolbar: "calc(var(--toolbar-h) + 1px)",
  none: "0px",
};

/** How long a toast stays: help and info text 8 s, a toast with an action (Undo) 15 s, an error until dismissed. */
export const INFO_MS = 8_000;
export const ACTION_MS = 15_000;
export function toastMs(t: Pick<ToastItem, "kind" | "action">): number | null {
  if (t.kind === "error") return null;
  return t.action ? ACTION_MS : INFO_MS;
}

/** Toast look, shared with the undo toast: an accent-tinted surface with an accent border and a real shadow. */
export const TOAST_SURFACE =
  "border-2 bg-[var(--kp-toast-bg)] text-fg shadow-toast data-[kind=error]:border-danger border-accent";

/**
 * Toast region. Toasts stay 8 s (15 s with an action, errors until dismissed) and hold while hovered or focused.
 * Announcements go through LiveRegion. `anchor="column"` puts the region at the bottom-left of its positioned parent
 * (the shell's main column, so beside the sidebar and under the list) instead of centred on the window.
 */
export function Toasts({ children, inset = "tabbar", anchor = "window" }: { children?: ReactNode; inset?: ToastInset; anchor?: "window" | "column" }) {
  const list = useStore(toasts);
  return (
    <div
      className={`pointer-events-none inset-x-0 z-50 flex flex-col gap-2 px-4 ${anchor === "column" ? "absolute items-start" : "fixed items-center"}`}
      data-testid="toast-region"
      data-inset={inset}
      data-anchor={anchor}
      style={{ bottom: `calc(${INSET[inset]} + var(--bottom-bar-h, 0px) + env(safe-area-inset-bottom) + 0.5rem)` }}
    >
      {list.map((t) => (
        <ToastView key={t.id} t={t} />
      ))}
      {children}
    </div>
  );
}

function ToastView({ t }: { t: ToastItem }) {
  // Hovering or focusing a toast holds its clock; leaving it starts the full time again.
  const [held, setHeld] = useState(false);
  const ms = toastMs(t);
  useEffect(() => {
    if (ms === null || held) return;
    const h = setTimeout(() => dismissToast(t.id), ms);
    return () => clearTimeout(h);
  }, [t.id, ms, held]);
  return (
    <ToastCard
      kind={t.kind}
      message={t.message}
      action={t.action && { label: t.action.label, run: () => t.action?.run() }}
      onDismiss={() => dismissToast(t.id)}
      onHold={setHeld}
    />
  );
}

/**
 * The one toast look and behaviour: message, an optional action, and a Dismiss button on every toast (a toast that
 * times out must also be closable, WCAG 2.2.1). Hovering or focusing it holds its clock through `onHold`.
 */
function ToastCard({
  kind,
  message,
  action,
  onDismiss,
  onHold,
}: {
  kind: ToastItem["kind"];
  message: string;
  action?: { label: string; run: () => void; disabled?: boolean };
  onDismiss: () => void;
  onHold: (hold: boolean) => void;
}) {
  return (
    <div
      data-kind={kind}
      className={`pointer-events-auto flex min-h-11 w-full max-w-[min(28rem,calc(var(--list-w,100vw)-2rem))] items-center gap-3 rounded-xl px-4 py-2 text-sm ${TOAST_SURFACE}`}
      onPointerEnter={() => onHold(true)}
      onPointerLeave={() => onHold(false)}
      onFocus={() => onHold(true)}
      onBlur={() => onHold(false)}
    >
      {kind === "error" ? <CircleAlert aria-hidden="true" className="size-5 shrink-0 text-danger" /> : null}
      <span className="flex-1 font-medium">{message}</span>
      {action && (
        <button
          type="button"
          className="hit rounded-md px-2 font-bold underline underline-offset-2 aria-disabled:opacity-50"
          // A busy action keeps its button (focus is not lost) but does nothing: the work is already under way.
          aria-disabled={action.disabled ? true : undefined}
          onClick={() => {
            if (!action.disabled) action.run();
          }}
        >
          {action.label}
        </button>
      )}
      <button type="button" aria-label="Dismiss" className="hit -mr-2 inline-flex items-center justify-center rounded-md" onClick={onDismiss}>
        <X className="size-5" aria-hidden="true" />
      </button>
    </div>
  );
}

/**
 * The undo toast: one slot, 15 s, merged text ("4 articles marked read"). The wrapper is a persistent role=status
 * region so a screen reader announces each new message; Undo is a real, focusable button and does not steal focus
 * when it appears. The countdown pauses while the pointer is over it or a button has focus.
 */
export function UndoToast() {
  const { toast } = useStore(undoStore);
  return (
    <div role="status" aria-live="polite" aria-atomic="true" data-testid="undo-region" className="contents">
      {toast ? (
        <ToastCard
          key={toast.id}
          kind="info"
          message={toast.text}
          action={{ label: "Undo", run: () => void undoToast(), disabled: toast.busy }}
          onDismiss={dismissUndoToast}
          onHold={holdUndoToast}
        />
      ) : null}
    </div>
  );
}

/** Visually hidden polite live region; mount once in the shell. */
export function LiveRegion() {
  const a = useStore(announcement);
  const e = useStore(alertAnnouncement);
  return (
    <>
      <div className="sr-only-live" role="status" aria-live="polite" aria-atomic="true" data-testid="live-region">
        {/* Toggling a zero-width space makes repeated identical messages re-announce. */}
        {a.message}
        {a.n % 2 ? "​" : ""}
      </div>
      <div className="sr-only-live" role="alert" aria-live="assertive" aria-atomic="true" data-testid="alert-region">
        {e.message}
        {e.n % 2 ? "​" : ""}
      </div>
    </>
  );
}
