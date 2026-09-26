import { useDevicePrefs, type UnreadBadge } from "@/lib/devicePrefs";
import { cn } from "@/lib/cn";

/** The count shown on a badge: capped at 99+, so a permanent backlog does not turn into a wall of digits. */
export const badgeText = (n: number): string => (n > 99 ? "99+" : String(n));

/** What a badge draws for `n` unread under a setting: the count, a dot, or nothing. */
export function badgeKind(n: number, mode: UnreadBadge): "count" | "dot" | null {
  if (n <= 0 || mode === "off") return null;
  return mode;
}

/**
 * The unread badge on the tab bar and in the sidebar. The device setting "Unread badge" (Count, Dot only, Off)
 * decides what shows. The unread state is never color alone: the dot has a screen reader label and the count
 * is text.
 */
export function UnreadCount({ n, tone = "quiet", className }: { n: number; tone?: "accent" | "quiet"; className?: string }) {
  const { unreadBadge } = useDevicePrefs();
  const kind = badgeKind(n, unreadBadge);
  if (!kind) return null;
  if (kind === "dot") {
    return (
      <span data-testid="unread-dot" className={cn("inline-block size-2 shrink-0 rounded-full bg-unread", className)}>
        <span className="sr-only-live">Unread</span>
      </span>
    );
  }
  return (
    <span
      data-testid="unread-count"
      className={cn(
        "shrink-0 rounded-full tabular-nums",
        tone === "accent" ? "bg-accent px-1.5 text-[0.6875rem] leading-4 font-bold text-bg" : "bg-surface px-2 py-0.5 text-xs font-semibold text-fg2",
        className,
      )}
    >
      <span className="sr-only-live">Unread </span>
      {badgeText(n)}
    </span>
  );
}
