import { useEffect, type ReactNode } from "react";
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

/** Toast region. Auto-dismiss 6 s (15 s with an action). Announcements go through LiveRegion. */
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
  useEffect(() => {
    const h = setTimeout(() => dismissToast(t.id), t.action ? 15_000 : 6_000);
    return () => clearTimeout(h);
  }, [t.id, t.action]);
  return (
    <div
      data-kind={t.kind}
      className="pointer-events-auto flex min-h-11 max-w-md items-center gap-3 rounded-xl border border-line bg-surface px-4 py-2 text-sm text-fg shadow-lg"
    >
      <span className={t.kind === "error" ? "text-danger" : undefined}>{t.message}</span>
      {t.action && (
        <button
          type="button"
          className="hit rounded-md px-2 font-semibold text-link underline"
          onClick={() => {
            t.action?.run();
            dismissToast(t.id);
          }}
        >
          {t.action.label}
        </button>
      )}
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
