import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { Suspense, useEffect, useRef, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation, useMatch } from "react-router";
import { ApiError, authStore, failureCause, openRefusedStore, SESSION_EXPIRED, signingOutStore } from "@/api/client";
import { keys, useBootstrap } from "@/api/queries";
import { hydrateDevice, startDeviceSync } from "@/lib/deviceSync";
import { prefetchUnread, retryRead } from "@/lib/offline";
import { offlineStore } from "@/lib/offlineState";
import { reloadToSignIn } from "@/lib/reload";
import { useStore } from "@/lib/store";
import { OpenList, ReaderRoute } from "@/screens/ReaderRoute";
import { SearchScreen } from "@/screens/SearchScreen";
import { AppShell } from "@/shell/AppShell";
import { RoutedErrorBoundary } from "@/shell/ErrorBoundary";
import { endRerun, rerunStore, resetting } from "@/setup/session";
import { WELCOME_ENTRY } from "@/setup/steps";
import { RestoreWaiting } from "@/setup/RestoreWaiting";
import { OpenRefusedScreen } from "@/setup/OpenRefused";
import { SignedOut } from "@/setup/SignedOut";
import { launchAnswerStore, useSessionProbe } from "@/api/sessionProbe";
import { lazyScreen } from "@/lib/lazyScreen";
import { cn } from "@/lib/cn";
import { useWide } from "@/lib/useMedia";
import { NotFoundBlock } from "@/ui/NotFoundBlock";
import { LoadFailed, useKeptFailure } from "@/ui/LoadFailed";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { Skeleton } from "@/ui/kit";

// Settings, Feeds and Health load on first visit; the reader and list stay in the main chunk.
// A failed download can be tried again (lib/lazyScreen.ts): React.lazy alone would keep the failure for good.
const FeedsScreen = lazyScreen(() => import("@/screens/FeedsScreen").then((m) => ({ default: m.FeedsScreen })));
const FeedIndexScreen = lazyScreen(() => import("@/screens/FeedIndexScreen").then((m) => ({ default: m.FeedIndexScreen })));
const HealthScreen = lazyScreen(() => import("@/screens/HealthScreen").then((m) => ({ default: m.HealthScreen })));
const StatsScreen = lazyScreen(() => import("@/screens/StatsScreen").then((m) => ({ default: m.StatsScreen })));
const WrappedScreen = lazyScreen(() => import("@/screens/WrappedScreen").then((m) => ({ default: m.WrappedScreen })));
const Welcome = lazyScreen(() => import("@/setup/Welcome").then((m) => ({ default: m.Welcome })));
const SettingsScreen = lazyScreen(() => import("@/screens/SettingsScreen").then((m) => ({ default: m.SettingsScreen })));

/**
 * After a bootstrap from the worker's stored copy, ask the server again this soon: the worker waits five seconds
 * for the network before it answers with its copy, and refreshes that copy when the late answer lands.
 */
export const BOOT_RECHECK_MS = 6000;
export const BOOT_RECHECK_MAX_MS = 2 * 60_000;

/** A path nothing answers (a mistyped address, an old link): say so instead of silently showing Unread. */
function NotFound() {
  return <NotFoundBlock noun="page" why="Kipple has no page at this address." />;
}

/**
 * The header a screen draws before its code has arrived, laid out as the screen's own so nothing moves when it lands.
 * The title is not a heading: a heading means the screen has arrived (focus, tests and assistive tech wait for the real one), and
 * the placeholder says "Loading" itself. Settings on a phone, inside a page, has a back button and the page's name that only the screen knows: it draws none.
 */
function ScreenHeader({ screen }: { screen: "stats" | "settings" }) {
  const wide = useWide();
  const inPage = !!useMatch("/settings/:group");
  if (screen === "stats") {
    return (
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-3">
        <div className="mx-auto w-full max-w-3xl">
          <div aria-hidden="true" className="pt-2 pb-2 text-xl font-bold">Stats</div>
        </div>
      </header>
    );
  }
  if (inPage && !wide) return null;
  return (
    <header className="pt-safe shrink-0 border-b border-line">
      <div aria-hidden="true" className={cn("w-full px-4 pt-2 pb-2 text-xl font-bold", !wide && "mx-auto max-w-[720px]")}>
        Settings
      </div>
    </header>
  );
}

/** While a screen's code downloads: its header at once when `header` names one, then bars. The real heading takes focus when it mounts. */
export function Lazy({ children, label = "Loading screen", header }: { children: React.ReactNode; label?: string; header?: "stats" | "settings" }) {
  return (
    <Suspense
      fallback={
        header ? (
          <div data-testid="lazy-fallback" className="ui-font flex h-full min-h-0 flex-col">
            <ScreenHeader screen={header} />
            <Skeleton label={label} />
          </div>
        ) : (
          <Skeleton label={label} />
        )
      }
    >
      {children}
    </Suspense>
  );
}

/**
 * Network mode "always" for reads and writes (#108). Under TanStack Query's default ("online") a query or mutation
 * started after the browser's `offline` event is paused before it sends anything, so it never reaches the service
 * worker, which answers the bootstrap, lists and articles from its copies (web/sw/sw.js), and a screen with nothing
 * kept shows a skeleton that never ends instead of its error. Writes too: the offline queue (lib/offline.ts) only
 * runs when the request is actually tried and fails, and a write it does not queue fails with its usual error.
 */
export function makeQueryClient(opts: { retry?: boolean } = {}): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        networkMode: "always",
        retry: (n, e) => opts.retry !== false && retryRead(n, e),
        refetchOnWindowFocus: false,
        // TanStack Query turns this off by default under "always"; keep it, so a screen that failed offline loads
        // again by itself when the network is back (lib/offline.ts starts the online state from the browser's).
        refetchOnReconnect: true,
      },
      mutations: { networkMode: "always" },
    },
  });
}

function Gate() {
  const auth = useStore(authStore);
  const qc = useQueryClient();
  // Nothing that needs a session is asked for until the session marker, or failing it a short probe, says there may
  // be one: a signed-out launch sends no request that answers 401 (the browser logs those to the console).
  const probed = useSessionProbe();
  const launchAnswer = useStore(launchAnswerStore);
  const refused = useStore(openRefusedStore);
  // While a sign-out you chose wipes this device's copies, the bootstrap is not asked for again (useBootstrap) and this
  // screen stands in for the app, so nothing below it asks for anything either.
  const signingOut = useStore(signingOutStore);
  // A launch the probe found refused by the open gate shows that at once; the bootstrap would only be refused too.
  const boot = useBootstrap(auth === "in" || (auth === "unknown" && probed && !refused));
  // The launch's failure is kept while it is asked again, so the failed screen stays up (see useKeptFailure).
  const keptFailure = useKeptFailure(boot.error, !!boot.data, boot.isFetching, auth === "out");
  const reset = useStore(resetting);
  const { pathname } = useLocation();
  const rerun = useStore(rerunStore);
  const inWizard = pathname === "/welcome" || pathname.startsWith("/welcome/");

  // Leaving the step of a rerun (the Back button) ends it.
  const reached = useRef(false);
  useEffect(() => {
    if (!rerun) reached.current = false;
    else if (inWizard) reached.current = true;
    else if (reached.current) {
      reached.current = false;
      endRerun();
    }
  }, [rerun, inWizard]);

  // Signed out: drop everything cached so nothing from the last session shows.
  useEffect(() => {
    if (auth === "out") {
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
      endRerun();
    }
  }, [auth, qc]);

  // The device profile is the truth for per-device settings; the local cache is reconciled with it once, from a live
  // answer only: the worker's stored copy can predate changes made on this device since, and taking it as the truth
  // would put them back. Changes made before that are kept as unsent (lib/deviceSync.ts) and win over the profile.
  useEffect(() => startDeviceSync(), []);
  const device = boot.data?.device;
  const fromCache = boot.data?.fromCache === true;
  useEffect(() => {
    if (!fromCache) hydrateDevice(device);
  }, [device, fromCache]);

  // A stored copy is only a stand-in. The flag it carries survives every local edit of the bootstrap (counts,
  // statuses), so only a live answer clears it and turns device sync on: ask again once the worker's own refresh
  // has had time to land, with a growing pause while the answers keep coming from the copy, and at once when the
  // connection comes back.
  useEffect(() => {
    if (!fromCache) return;
    let delay = BOOT_RECHECK_MS;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    const recheck = () => {
      if (timer) clearTimeout(timer);
      // cancelRefetch off: a request already on its way (the live answer arriving is what flips `online`) is kept.
      void qc.invalidateQueries({ queryKey: keys.bootstrap }, { cancelRefetch: false }).finally(() => {
        if (stopped) return;
        delay = Math.min(delay * 2, BOOT_RECHECK_MAX_MS);
        timer = setTimeout(recheck, delay);
      });
    };
    timer = setTimeout(recheck, delay);
    let wasOnline = offlineStore.get().online;
    const unsub = offlineStore.subscribe(() => {
      const now = offlineStore.get().online;
      if (now && !wasOnline) recheck();
      wasOnline = now;
    });
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      unsub();
    };
  }, [fromCache, qc]);

  // Keep the first page of Unread on the device for offline reading (the service worker stores it).
  const ready = boot.isSuccess && auth === "in";
  useEffect(() => {
    if (ready) void prefetchUnread();
  }, [ready]);

  if (reset) return <RestoreWaiting kind="reset" estimateSeconds={reset.estimateSeconds} username={null} onSignIn={() => window.location.reload()} />;
  if (auth === "out") return <SignedOut initial={launchAnswer} />;
  // An account with no password, reached from somewhere the server does not allow: say why, and how to get in.
  if (refused) {
    return (
      <OpenRefusedScreen
        reason={(refused.reason as "host" | "peer" | "forwarded" | null) ?? null}
        busy={boot.isFetching}
        onRetry={() => {
          openRefusedStore.set(null);
          void qc.invalidateQueries();
        }}
      />
    );
  }
  if (boot.isPending && signingOut) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Signing out</span>
      </div>
    );
  }
  if (boot.isPending && keptFailure == null) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
    );
  }
  // The full-screen errors are for a launch with nothing to show. A background refetch that fails later keeps its
  // data (and sets isError alongside it): the reader stays up and the notice at the top explains what happened.
  const failure = boot.data ? null : boot.isError ? boot.error : keptFailure;
  const failed = failure != null;
  if (failure instanceof ApiError && failure.code === SESSION_EXPIRED) {
    return (
      <EmptyState role="alert" asH1 title="Your sign-in has expired" why="The sign-in in front of Kipple timed out. Reload to sign in again." action={<Button onClick={() => reloadToSignIn()}>Reload</Button>} />
    );
  }
  if (failed) {
    // Offline only when the browser says so: Kipple not answering is "can't be reached", whatever the last request did.
    const cause = failureCause(failure) ?? "other";
    return (
      <LoadFailed
        cause={cause}
        retryAfterMs={failure instanceof ApiError ? failure.retryAfterMs : 0}
        busy={boot.isFetching}
        onRetry={() => void boot.refetch()}
        why={cause === "offline" ? "Kipple has nothing saved on this device yet. Open it once while you're online and your unread articles will be here next time." : undefined}
      />
    );
  }
  // First-run setup pending (a fresh account): every screen but /welcome gives way to it. Not on a
  // stored copy of the bootstrap, which may still say so after setup was finished elsewhere.
  if (boot.data?.user.setup_pending === true && !fromCache && !inWizard) return <Navigate to={WELCOME_ENTRY} replace />;
  return (
    <Routes>
      <Route path="welcome/*" element={<Lazy><Welcome /></Lazy>} />
      <Route element={<AppShell />}>
        <Route index element={<Navigate to="/l/unread" replace />} />
        <Route path="l" element={<OpenList />} />
        <Route element={<ReaderRoute />}>
          <Route path="l/:view" />
          <Route path="i/:id" />
        </Route>
        <Route path="feeds" element={<Lazy label="Loading feeds"><FeedsScreen /></Lazy>} />
        <Route path="feeds/index" element={<Lazy label="Loading the feed index"><FeedIndexScreen /></Lazy>} />
        <Route path="health" element={<Lazy label="Loading feed health"><HealthScreen /></Lazy>} />
        <Route path="search" element={<SearchScreen />} />
        <Route path="stats" element={<Lazy label="Loading stats" header="stats"><StatsScreen /></Lazy>} />
        <Route path="stats/wrapped" element={<Lazy label="Loading your year"><WrappedScreen /></Lazy>} />
        <Route path="settings/*" element={<Lazy label="Loading settings" header="settings"><SettingsScreen /></Lazy>} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

export default function App({ client }: { client?: QueryClient }) {
  // Created once: a re-render of App must not throw the cache away.
  const [own] = useState(() => client ?? makeQueryClient());
  const qc = client ?? own;
  return (
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <RoutedErrorBoundary>
          <Gate />
        </RoutedErrorBoundary>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
