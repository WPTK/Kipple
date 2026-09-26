import { useEffect, useState, type ReactNode } from "react";
import { X } from "lucide-react";
import { createStore, useStore } from "@/lib/store";

export interface ToastItem {
  id: number;
  message: string;
  kind: "info" | "error";
  action?: { label: string; run: () => void };
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

export function toast(message: string, kind: ToastItem["kind"] = "info", action?: ToastItem["action"]): number {
  const id = ++seq;
  // The visible toast is not itself a live region: a freshly mounted role=status/alert node is
  // often not announced on iOS VoiceOver. The persistent regions in <LiveRegion> speak instead.
  if (kind === "error") alertAnnouncement.set((a) => ({ n: a.n + 1, message }));
  else announce(message);
  toasts.set((t) => [...t.filter((x) => x.message !== message), { id, message, kind, action }].slice(-3));
  return id;
}

export function dismissToast(id: number): void {
  toasts.set((t) => t.filter((x) => x.id !== id));
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
  "border-2 bg-[var(--kp-toast-bg)] text-fg shadow-[0_8px_24px_rgb(0_0_0/0.3)] data-[kind=error]:border-danger border-accent";

/** Toast region. Toasts stay 8 s (15 s with an action, errors until dismissed) and hold while hovered or focused. Announcements go through LiveRegion. */
export function Toasts({ children, inset = "tabbar" }: { children?: ReactNode; inset?: ToastInset }) {
  const list = useStore(toasts);
  return (
    <div
      className="pointer-events-none fixed inset-x-0 z-50 flex flex-col items-center gap-2 px-4"
      data-testid="toast-region"
      data-inset={inset}
      style={{ bottom: `calc(${INSET[inset]} + env(safe-area-inset-bottom) + 0.5rem)` }}
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
    <div
      data-kind={t.kind}
      className={`pointer-events-auto flex min-h-11 w-full max-w-md items-center gap-3 rounded-xl px-4 py-2 text-sm ${TOAST_SURFACE}`}
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <span className="flex-1 font-medium">{t.message}</span>
      {t.action && (
        <button
          type="button"
          className="hit rounded-md px-2 font-bold underline underline-offset-2"
          onClick={() => {
            t.action?.run();
            dismissToast(t.id);
          }}
        >
          {t.action.label}
        </button>
      )}
      {t.kind === "error" ? (
        <button type="button" aria-label="Dismiss" className="hit -mr-2 inline-flex items-center justify-center rounded-md" onClick={() => dismissToast(t.id)}>
          <X className="size-5" aria-hidden="true" />
        </button>
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
