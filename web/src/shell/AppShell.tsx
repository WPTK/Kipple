import { useEffect, useRef, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { Inbox, List, Rss, Search, Settings, Star, TriangleAlert, X } from "lucide-react";
import { useServerEvents } from "@/api/events";
import { useBootstrap } from "@/api/queries";
import { useRefreshAll } from "@/api/refresh";
import { useHotkeys } from "@/lib/keys";
import { SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_MIN, DEFAULT_DEVICE_PREFS, updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { useWide } from "@/lib/useMedia";
import { listTo } from "@/lib/routes";
import { cn } from "@/lib/cn";
import { FeedTree } from "@/screens/FeedTree";
import { undoLast } from "@/lib/undo";
import { HelpDialog, openHelp } from "./HelpDialog";
import { ResizeHandle } from "@/ui/ResizeHandle";
import { UnreadCount } from "@/ui/UnreadCount";
import { UndoToast } from "./UndoToast";
import { LiveRegion, Toasts } from "./toasts";

const tab = "flex min-h-11 flex-1 flex-col items-center justify-center gap-0.5 px-1 text-xs font-medium";

/** The Unread tab's badge: count (capped at 99+), a dot, or nothing, by the device's "Unread badge" setting. */
function UnreadBadge() {
  const boot = useBootstrap();
  return <UnreadCount n={boot.data?.counts.unread ?? 0} tone="accent" />;
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
  const dp = useDevicePrefs();
  const c = boot.data?.counts;
  const item = ({ isActive }: { isActive: boolean }) =>
    cn("flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm font-medium hover:bg-selection", isActive && "bg-selection");
  return (
    <div className="relative h-full shrink-0" style={{ width: dp.sidebarWidth }}>
      <nav aria-label="Primary" className="pt-safe pl-safe flex h-full flex-col border-r border-line bg-surface px-2">
        <div className="flex shrink-0 flex-col gap-1">
          <p className="px-3 py-3 text-lg font-bold">Kipple</p>
          <NavLink to="/l/unread" end className={item}>
            <Inbox aria-hidden="true" className="size-5" />
            Unread
            <span className="ml-auto" />
            <UnreadCount n={c?.unread ?? 0} />
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
          <NavLink to="/feeds" className={item}>
            <Rss aria-hidden="true" className="size-5" />
            Manage feeds
          </NavLink>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto pb-2">
          <FeedTree />
        </div>
        <div className="shrink-0 border-t border-line py-2">
          <NavLink to="/settings" className={item}>
            <Settings aria-hidden="true" className="size-5" />
            Settings
          </NavLink>
        </div>
      </nav>
      <ResizeHandle
        label="Resize sidebar"
        value={dp.sidebarWidth}
        min={SIDEBAR_WIDTH_MIN}
        max={SIDEBAR_WIDTH_MAX}
        onChange={(sidebarWidth) => updateDevicePrefs({ sidebarWidth })}
        onReset={() => updateDevicePrefs({ sidebarWidth: DEFAULT_DEVICE_PREFS.sidebarWidth })}
      />
    </div>
  );
}

const WARN_KEY = "kipple.warnings.dismissed";

/** Bootstrap warnings (clock ahead, stale snapshot, too many unread for sync apps): shown once per session. */
function WarningsBanner() {
  const boot = useBootstrap();
  const [gone, setGone] = useState(() => {
    try {
      return sessionStorage.getItem(WARN_KEY);
    } catch {
      return null;
    }
  });
  const ws = boot.data?.warnings ?? [];
  const sig = ws.map((w) => w.code).join(",");
  if (ws.length === 0 || gone === sig) return null;
  return (
    <div role="status" className="pt-safe flex shrink-0 items-start gap-2 border-b border-line bg-surface px-4 py-2 text-sm">
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-danger" />
      <div className="min-w-0 flex-1">
        {ws.map((w) => (
          <p key={w.code}>{w.message}</p>
        ))}
        <NavLink to="/health" className="text-link underline underline-offset-2">
          Open feed health
        </NavLink>
      </div>
      <button
        type="button"
        aria-label="Dismiss warnings"
        className="hit -my-2 inline-flex items-center justify-center rounded-lg hover:bg-selection"
        onClick={() => {
          setGone(sig);
          try {
            sessionStorage.setItem(WARN_KEY, sig);
          } catch {
            /* not remembered */
          }
        }}
      >
        <X aria-hidden="true" className="size-5" />
      </button>
    </div>
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
  useServerEvents(true);
  const refresh = useRefreshAll();

  useHotkeys(
    {
      refresh: () => refresh.mutate(),
      goUnread: () => navigate(listTo({ view: "unread" })),
      goAll: () => navigate(listTo({ view: "all" })),
      goStarred: () => navigate(listTo({ view: "starred" })),
      goFeeds: () => navigate("/feeds"),
      goSettings: () => navigate("/settings"),
      search: () => navigate("/search"),
      help: openHelp,
      undo: () => void undoLast(),
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
        <WarningsBanner />
        <main id="main" ref={mainRef} tabIndex={-1} className="pl-safe pr-safe min-h-0 flex-1 outline-none">
          <Outlet />
        </main>
        {showTabs ? <TabBar /> : null}
      </div>
      <LiveRegion />
      <Toasts inset={wide ? "none" : inArticle ? "toolbar" : "tabbar"}>
        <UndoToast />
      </Toasts>
      <HelpDialog />
    </div>
  );
}
