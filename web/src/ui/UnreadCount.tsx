import { useDevicePrefs, type UnreadBadge } from "@/lib/devicePrefs";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

/** The count shown on a badge: capped at 99+, so a permanent backlog does not turn into a wall of digits. */
export const badgeText = (n: number): string => (n > 99 ? "99+" : String(n));

/** What a badge draws for `n` unread under a setting: the count, a dot, or nothing. */
export function badgeKind(n: number, mode: UnreadBadge): "count" | "dot" | null {
  if (n <= 0 || mode === "off") return null;
  return mode;
}

/**
 * The name of a row that carries an unread badge, for a control whose name is set by hand (a tree item, which cannot
 * take its name from content because the favorite star inside it would be part of it). It keeps the visible text in
 * the same order and adds the word the badge hides from sighted users: "Feed name, 12 unread" (WCAG 2.5.3, Label in
 * Name). A dot says "unread" without a number, and no badge adds nothing.
 */
export function unreadName(name: string, n: number, mode: UnreadBadge): string {
  const kind = badgeKind(n, mode);
  if (kind === "count") return `${name}, ${badgeText(n)} unread`;
  return kind === "dot" ? `${name}, unread` : name;
}

/**
 * A count in a pill: the one badge for every number beside a label (unread, muted, starred, a feed's articles).
 * `accent` is the loud one for the tab bar's icon; `quiet` sits on a surface beside a row. The number is capped at 99+.
 */
function CountBadge({ n, tone = "quiet", testid, className, before, after }: { n: number; tone?: "accent" | "quiet"; testid?: string; className?: string; /** Spoken before the number. */ before?: ReactNode; /** Spoken after it. */ after?: ReactNode }) {
  return (
    <span
      data-testid={testid}
      className={cn(
        "shrink-0 rounded-full tabular-nums",
        tone === "accent" ? "bg-accent px-1.5 text-micro leading-4 font-bold text-bg" : "bg-surface px-2 py-0.5 text-xs font-semibold text-fg2",
        className,
      )}
    >
      {before}
      {badgeText(n)}
      {after}
    </span>
  );
}

/**
 * The unread badge on the tab bar and in the sidebar. The device setting "Unread badge" (Count, Dot only, Off)
 * decides what shows. The unread state is never color alone: the dot has a screen reader label and the count
 * is text.
 */
export function UnreadCount({ n, tone = "quiet", className, noun = "unread" }: { n: number; tone?: "accent" | "quiet"; className?: string; /** Read after the number (the badge sits in a flex row, so it is its own word); empty beside a label that already says Unread. */ noun?: string }) {
  const { unreadBadge } = useDevicePrefs();
  const kind = badgeKind(n, unreadBadge);
  if (!kind) return null;
  if (kind === "dot") {
    return (
      <span data-testid="unread-dot" className={cn("inline-block size-2 shrink-0 rounded-full bg-unread", className)}>
        <span className="sr-only-live">{noun || "articles waiting"}</span>
      </span>
    );
  }
  return (
    <CountBadge n={n} tone={tone} testid="unread-count" className={className} after={noun ? <span className="sr-only-live"> {noun}</span> : null} />
  );
}

/** A plain count of articles (muted, starred): not an unread badge, so the "Unread badge" setting does not hide it. */
function PlainCount({ n, noun, testid, className }: { n: number; noun: string; testid: string; className?: string }) {
  if (n <= 0) return null;
  return (
    <CountBadge n={n} testid={testid} className={className} before={<span className="sr-only-live">{noun} </span>} />
  );
}

/** How many articles are muted. */
export const MutedCount = (p: { n: number; className?: string }) => <PlainCount noun="Muted" testid="muted-count" {...p} />;

/** How many articles are starred. */
export const StarredCount = (p: { n: number; className?: string }) => <PlainCount noun="Starred" testid="starred-count" {...p} />;
