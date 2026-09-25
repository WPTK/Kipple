import { useEffect, useRef, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { Dialog } from "radix-ui";
import { Inbox, List, Search, Settings, Star } from "lucide-react";
import { useServerEvents } from "@/api/events";
import { useBootstrap } from "@/api/queries";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { useWide } from "@/lib/useMedia";
import { listTo } from "@/lib/routes";
import { cn } from "@/lib/cn";
import { FeedTree } from "@/screens/FeedTree";
import { Button } from "@/ui/button";
import { LiveRegion, Toasts } from "./toasts";

const tab = "flex min-h-11 flex-1 flex-col items-center justify-center gap-0.5 px-1 text-xs font-medium";

function UnreadBadge() {
  const boot = useBootstrap();
  const n = boot.data?.counts.unread ?? 0;
  if (n <= 0) return null;
  return (
    <span className="rounded-full bg-accent px-1.5 text-[0.6875rem] leading-4 font-bold text-bg tabular-nums">
      <span className="sr-only-live">Unread </span>
      {n > 999 ? "999+" : n}
    </span>
  );
}

function TabBar() {
  const { pathname } = useLocation();
  const inReader = pathname.startsWith("/l/") || pathname.startsWith("/i/");
  const cls = ({ isActive }: { isActive: boolean }) => cn(tab, isActive ? "text-accent" : "text-fg2");
  return (
    <nav
      aria-label="Primary"
      className="pb-safe pl-safe pr-safe flex shrink-0 border-t border-line bg-bg"
      style={{ minHeight: "var(--tabbar-h)" }}
    >
      <NavLink to="/l/unread" className={() => cn(tab, inReader ? "text-accent" : "text-fg2")} aria-current={inReader ? "page" : undefined}>
        <span className="flex items-center gap-1">
          <Inbox aria-hidden="true" className="size-6" />
          <UnreadBadge />
        </span>
        Unread
      </NavLink>
      <NavLink to="/feeds" className={cls}>
        <List aria-hidden="true" className="size-6" />
        Feeds
      </NavLink>
      <NavLink to="/search" className={cls}>
        <Search aria-hidden="true" className="size-6" />
        Search
      </NavLink>
      <NavLink to="/settings" className={cls}>
        <Settings aria-hidden="true" className="size-6" />
        Settings
      </NavLink>
    </nav>
  );
}

function Sidebar() {
  const boot = useBootstrap();
  const c = boot.data?.counts;
  const item = ({ isActive }: { isActive: boolean }) =>
    cn("flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm font-medium hover:bg-selection", isActive && "bg-selection");
  return (
    <nav aria-label="Primary" className="pt-safe pl-safe flex h-full w-60 shrink-0 flex-col gap-1 overflow-y-auto border-r border-line bg-surface px-2 pb-4">
      <p className="px-3 py-3 text-lg font-bold">Kipple</p>
      <NavLink to="/l/unread" end className={item}>
        <Inbox aria-hidden="true" className="size-5" />
        Unread
        {c && c.unread > 0 ? <span className="ml-auto text-xs text-fg2 tabular-nums">{c.unread}</span> : null}
      </NavLink>
      <NavLink to="/l/all" className={item}>
        <List aria-hidden="true" className="size-5" />
        All articles
      </NavLink>
      <NavLink to="/l/starred" className={item}>
        <Star aria-hidden="true" className="size-5" />
        Starred
      </NavLink>
      <NavLink to="/search" className={item}>
        <Search aria-hidden="true" className="size-5" />
        Search
      </NavLink>
      <NavLink to="/settings" className={item}>
        <Settings aria-hidden="true" className="size-5" />
        Settings
      </NavLink>
      <h2 className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Feeds</h2>
      <FeedTree />
    </nav>
  );
}

const HELP: [string, string][] = [
  ["j / k", "Next / previous article"],
  ["Enter", "Open the selected article"],
  ["o", "Open original"],
  ["s", "Star or unstar"],
  ["m", "Mark read or unread"],
  ["f", "Toggle full text (article)"],
  ["r", "Refresh all feeds"],
  ["u / Esc", "Back to the list"],
  ["g g", "Jump to top"],
  ["G", "Jump to bottom"],
  ["g i / a / s / f / ,", "Go to Unread / All / Starred / Feeds / Settings"],
  ["/", "Search"],
  ["?", "This help"],
];

function HelpDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <Dialog.Content className="fixed inset-x-4 top-[10vh] z-50 mx-auto max-h-[80dvh] max-w-md overflow-y-auto rounded-2xl border border-line bg-bg p-5 text-fg shadow-xl">
          <Dialog.Title className="text-lg font-bold">Keyboard shortcuts</Dialog.Title>
          <Dialog.Description className="mb-3 text-sm text-fg2">Single-key shortcuts can be turned off in Settings.</Dialog.Description>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            {HELP.map(([k, d]) => (
              <div key={k} className="contents">
                <dt className="font-mono font-semibold">{k}</dt>
                <dd className="text-fg2">{d}</dd>
              </div>
            ))}
          </dl>
          <Dialog.Close asChild>
            <Button className="mt-4">Close</Button>
          </Dialog.Close>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

/**
 * The signed-in frame. Landmarks: nav (tab bar or sidebar), main. A polite live
 * region and the toast area live here, and focus moves to the new screen's
 * heading after each navigation between screens.
 */
export function AppShell() {
  const wide = useWide();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const prefs = useStore(prefsStore);
  const [help, setHelp] = useState(false);
  useServerEvents(true);

  useHotkeys(
    {
      goUnread: () => navigate(listTo({ view: "unread" })),
      goAll: () => navigate(listTo({ view: "all" })),
      goStarred: () => navigate(listTo({ view: "starred" })),
      goFeeds: () => navigate("/feeds"),
      goSettings: () => navigate("/settings"),
      search: () => navigate("/search"),
      help: () => setHelp(true),
    },
    { singleKeys: prefs.shortcuts },
  );

  // Move focus to the screen heading when the screen (not just the article) changes.
  const mainRef = useRef<HTMLElement>(null);
  const first = useRef(true);
  const section = pathname.split("/")[1] ?? "";
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    if (section === "i") return; // the article focuses its own title
    const h = mainRef.current?.querySelector<HTMLElement>("[data-route-heading]");
    (h ?? mainRef.current)?.focus({ preventScroll: true });
  }, [section, pathname]);

  const inArticle = pathname.startsWith("/i/");
  const showTabs = !wide && !inArticle;

  return (
    <div className="flex h-full">
      <a
        href="#main"
        className="sr-only-live focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-50 focus:rounded-lg focus:bg-accent focus:px-4 focus:py-3 focus:text-bg"
        onClick={(e) => {
          e.preventDefault();
          mainRef.current?.focus();
        }}
      >
        Skip to content
      </a>
      {wide ? <Sidebar /> : null}
      <div className="flex min-w-0 flex-1 flex-col">
        <main id="main" ref={mainRef} tabIndex={-1} className="pl-safe pr-safe min-h-0 flex-1 outline-none">
          <Outlet />
        </main>
        {showTabs ? <TabBar /> : null}
      </div>
      <LiveRegion />
      <Toasts />
      <HelpDialog open={help} onOpenChange={setHelp} />
    </div>
  );
}
