import { useEffect, useRef, useState } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate, useSearchParams } from "react-router";
import { BarChart3, BellOff, Inbox, List, Rss, Search, Settings, Star, TriangleAlert, X } from "lucide-react";
import { useServerEvents } from "@/api/events";
import { useBootstrap } from "@/api/queries";
import { useStatsEnabled } from "@/lib/statsSender";
import { useSyncHighlights } from "@/lib/useHighlights";
import { SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_MIN, DEFAULT_DEVICE_PREFS, updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { useWide } from "@/lib/useMedia";
import { cn } from "@/lib/cn";
import { navItemClass } from "@/ui/navItem";
import { FeedTree } from "@/screens/FeedTree";
import { WhatsNewHost } from "./WhatsNew";
import { ResizeHandle } from "@/ui/ResizeHandle";
import { ARTICLE_MIN, LIST_MIN_FOR_SIDEBAR, maxFor, useViewportWidth } from "@/lib/useWidth";
import { MutedCount, UnreadCount } from "@/ui/UnreadCount";
import { ApplyProgress, FeedEditorHost, FilterEditorHost } from "./FilterHost";
import { OfflineNotice } from "./OfflineNotice";
import { DeviceSaveStatus } from "./SaveStatus";
import { UndoToast } from "./UndoToast";
import { LiveRegion, Toasts, announce } from "./toasts";

// Five equal tabs that always fit: the label follows the text size up to 4% of the screen width, the most a fifth of
// the bar holds "Settings" in (12.8 px at 320 wide, 15 px at 375, 17 px at 430). A larger label could only be cut off,
// as phone tab bars cap theirs; the icons above keep growing with the text size.
//
// On a short screen (a phone held sideways) the icon sits beside its name instead of above it, which takes the bar from
// 56 px to 44 px: the list keeps the height (#420).
const tab =
  "flex min-h-11 min-w-0 flex-1 flex-col items-center justify-center gap-0.5 px-1 text-[length:min(0.75rem,4vw)] font-medium [@media(max-height:500px)]:flex-row [@media(max-height:500px)]:gap-2";

/** The Unread tab's badge: count (capped at 99+), a dot, or nothing, by the device's "Unread badge" setting. */
function UnreadBadge() {
  const boot = useBootstrap();
  return <UnreadCount n={boot.data?.counts.unread ?? 0} tone="accent" noun="" className="absolute -top-1 -right-[10px]" />;
}

function TabBar() {
  const { pathname } = useLocation();
  const stats = useStatsEnabled();
  const [search] = useSearchParams();
  // A list scoped to a feed or folder is reached from the Feeds tab (so is Feed Health); only the four whole lists
  // (Unread, All, Starred, Muted) belong to the Unread tab.
  const scoped = search.has("feed") || search.has("folder");
  const inList = pathname.startsWith("/l/") || pathname === "/l";
  const unreadTab = inList && !scoped;
  const feedsTab = pathname === "/feeds" || pathname === "/health" || (inList && scoped);
  const cls = ({ isActive }: { isActive: boolean }) => cn(tab, isActive ? "text-accent" : "text-fg2");
  return (
    <nav
      aria-label="Primary"
      className="pb-safe pt-2 pl-safe pr-safe flex shrink-0 border-t border-line bg-bg shadow-[0_-6px_10px_-8px_rgb(0_0_0/0.5)] [@media(max-height:500px)]:pt-0"
      style={{ minHeight: "var(--tabbar-h)" }}
    >
      {/* Plain links: NavLink would only mark the tab whose own address is the current one. */}
      <Link to="/l/unread" className={cn(tab, unreadTab ? "text-accent" : "text-fg2")} aria-current={unreadTab ? "page" : undefined}>
        {/* The badge sits on the icon's corner and grows over the icon, so this tab is no wider than the others. */}
        <span className="relative flex">
          <Inbox aria-hidden="true" className="size-6" />
          <UnreadBadge />
        </span>
        Unread
      </Link>
      <Link to="/feeds" className={cn(tab, feedsTab ? "text-accent" : "text-fg2")} aria-current={feedsTab ? "page" : undefined}>
        <List aria-hidden="true" className="size-6" />
        Feeds
      </Link>
      <NavLink to="/search" className={cls}>
        <Search aria-hidden="true" className="size-6" />
        Search
      </NavLink>
      {stats ? (
        <NavLink to="/stats" className={cls}>
          <BarChart3 aria-hidden="true" className="size-6" />
          Stats
        </NavLink>
      ) : null}
      <NavLink to="/settings" className={cls}>
        <Settings aria-hidden="true" className="size-6" />
        Settings
      </NavLink>
    </nav>
  );
}

function Sidebar() {
  const boot = useBootstrap();
  const stats = useStatsEnabled();
  const dp = useDevicePrefs();
  const c = boot.data?.counts;
  // The sidebar leaves the list and the article their minimums, so at the 900 px breakpoint it cannot squeeze them.
  const limit = maxFor(useViewportWidth(), LIST_MIN_FOR_SIDEBAR + ARTICLE_MIN, SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX);
  const width = Math.min(limit, dp.sidebarWidth);
  const box = useRef<HTMLDivElement>(null);
  const item = navItemClass;
  return (
    <div ref={box} className="relative h-full shrink-0" style={{ width }}>
      <nav aria-label="Primary" className="pt-safe pl-safe relative flex h-full flex-col border-r border-line bg-surface px-2">
        <div className="flex shrink-0 flex-col gap-1">
          <p className="px-3 py-3 text-lg font-bold">Kipple</p>
          <NavLink to="/l/unread" end className={item}>
            <Inbox aria-hidden="true" className="size-5" />
            Unread
            <span className="ml-auto" />
            <UnreadCount n={c?.unread ?? 0} noun="" />
          </NavLink>
          <NavLink to="/l/all" className={item}>
            <List aria-hidden="true" className="size-5" />
            All articles
          </NavLink>
          <NavLink to="/l/starred" className={item}>
            <Star aria-hidden="true" className="size-5" />
            Starred
          </NavLink>
          <NavLink to="/l/muted" className={item}>
            <BellOff aria-hidden="true" className="size-5" />
            Muted
            <span className="ml-auto" />
            <MutedCount n={c?.muted ?? 0} />
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
        <div data-reveal-root="" className="min-h-0 flex-1 overflow-y-auto pb-2">
          <FeedTree />
        </div>
        <div className="flex shrink-0 flex-col gap-1 border-t border-line py-2">
          {stats ? (
            <NavLink to="/stats" className={item}>
              <BarChart3 aria-hidden="true" className="size-5" />
              Stats
            </NavLink>
          ) : null}
          <NavLink to="/settings" className={item}>
            <Settings aria-hidden="true" className="size-5" />
            Settings
          </NavLink>
        </div>
        <ResizeHandle
          label="Resize sidebar"
          value={width}
          min={SIDEBAR_WIDTH_MIN}
          max={limit}
          onPreview={(px) => {
            if (box.current) box.current.style.width = `${px ?? width}px`;
          }}
          onChange={(sidebarWidth) => updateDevicePrefs({ sidebarWidth })}
          onReset={() => updateDevicePrefs({ sidebarWidth: DEFAULT_DEVICE_PREFS.sidebarWidth })}
        />
      </nav>
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
  const { pathname, search, hash, state } = useLocation();
  const navigate = useNavigate();
  useServerEvents(true);
  useSyncHighlights();

  // Move focus to the screen heading when the screen (not just the article) changes.
  const mainRef = useRef<HTMLElement>(null);
  const first = useRef(true);
  const section = pathname.split("/")[1] ?? "";
  // This is the one writer of arrival focus. A screen names its target instead of calling focus() itself, which
  // this effect would overwrite since child effects run first: data-load-focus for a page load or reload,
  // data-route-focus for an in-app arrival (else the heading, which also announces the new screen), and none while
  // focus is in a data-keep-focus widget outside the screen. A lazy screen
  // still loading here has neither target nor heading yet.
  // A screen that hands over to the app (the end of setup) navigates with state { arrival: true }: the shell's first
  // screen is then an arrival, not the page load.
  const handedOver = (state as { arrival?: boolean } | null)?.arrival === true;
  useEffect(() => {
    if (first.current) {
      first.current = false;
      // Read once: left in the history entry, a reload of it would count as an arrival too.
      if (handedOver) navigate({ pathname, search, hash }, { replace: true, state: null });
      if (!handedOver) {
        // The page load: focus stays where the browser put it unless the screen named a target.
        mainRef.current?.querySelector<HTMLElement>("[data-load-focus]")?.focus({ preventScroll: true });
        return;
      }
    }
    if (section === "i") return; // the article focuses its own title
    const find = () => mainRef.current?.querySelector<HTMLElement>("[data-route-focus]") ?? mainRef.current?.querySelector<HTMLElement>("[data-route-heading]");
    // Focus on an item of a widget outside the screen that is walked to open one screen after another
    // (data-keep-focus: a sidebar tree item, focused from the keyboard) stays there: the new screen is announced
    // instead of focused, and the next item is one key away.
    const from = document.activeElement;
    const keep = from instanceof HTMLElement && from.isConnected && from.hasAttribute("data-keep-focus") && !mainRef.current?.contains(from);
    const arrive = (target: HTMLElement) => {
      if (!keep) return target.focus({ preventScroll: true });
      const heading = mainRef.current?.querySelector<HTMLElement>("[data-route-heading]") ?? target;
      announce(heading.getAttribute("aria-label") || heading.textContent?.trim() || document.title);
    };
    const target = find();
    if (target) {
      arrive(target);
      return;
    }
    // A lazy screen still loading has no heading yet: main takes focus now, and the heading takes it when it mounts, if
    // focus is still on main (or lost to the body). Once that has happened, or focus has gone anywhere else, or a
    // moment has passed, the watch ends, so a later re-render of the screen never pulls focus back.
    const main = mainRef.current;
    if (!keep) main?.focus({ preventScroll: true });
    if (!main) return;
    const stop = () => {
      watch.disconnect();
      clearTimeout(timer);
    };
    const watch = new MutationObserver(() => {
      const heading = find();
      if (!heading) return;
      const at = document.activeElement;
      if (keep || at === main || at === document.body || at === null) arrive(heading);
      stop();
    });
    const timer = setTimeout(stop, 10_000);
    watch.observe(main, { childList: true, subtree: true });
    return stop;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- read once, at the shell's first screen
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
      <div className="kp-stack flex min-w-0 flex-1 flex-col">
        <OfflineNotice />
        <WarningsBanner />
        <ApplyProgress />
        <main id="main" ref={mainRef} tabIndex={-1} className="pl-safe pr-safe min-h-0 flex-1 outline-none">
          <Outlet />
        </main>
        {showTabs ? <TabBar /> : null}
      </div>
      <LiveRegion />
      <DeviceSaveStatus />
      <Toasts inset={wide ? "none" : inArticle ? "toolbar" : "tabbar"}>
        <UndoToast />
      </Toasts>
      <WhatsNewHost />
      <FilterEditorHost />
      <FeedEditorHost />
    </div>
  );
}
