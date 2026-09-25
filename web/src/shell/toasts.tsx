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

export function toast(message: string, kind: ToastItem["kind"] = "info", action?: ToastItem["action"]): number {
  const id = ++seq;
  toasts.set((t) => [...t.filter((x) => x.message !== message), { id, message, kind, action }].slice(-3));
  return id;
}

export function dismissToast(id: number): void {
  toasts.set((t) => t.filter((x) => x.id !== id));
}

export function clearToasts(): void {
  toasts.set([]);
}

/** Toast region. Errors use role=alert; info toasts are polite. Auto-dismiss 6 s (15 s with an action). */
export function Toasts({ children }: { children?: ReactNode }) {
  const list = useStore(toasts);
  return (
    <div
      className="pointer-events-none fixed inset-x-0 z-50 flex flex-col items-center gap-2 px-4"
      style={{ bottom: "calc(var(--tabbar-h) + env(safe-area-inset-bottom) + 0.5rem)" }}
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
      role={t.kind === "error" ? "alert" : "status"}
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
  return (
    <div className="sr-only-live" role="status" aria-live="polite" aria-atomic="true" data-testid="live-region">
      {/* Toggling a zero-width space makes repeated identical messages re-announce. */}
      {a.message}
      {a.n % 2 ? "​" : ""}
    </div>
  );
}
