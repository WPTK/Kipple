import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { Suspense, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router";
import { ApiError, authStore, openRefusedStore, SESSION_EXPIRED } from "@/api/client";
import { keys, useBootstrap } from "@/api/queries";
import { hydrateDevice, startDeviceSync } from "@/lib/deviceSync";
import { failedWhileOffline, prefetchUnread } from "@/lib/offline";
import { offlineStore } from "@/lib/offlineState";
import { reloadToSignIn } from "@/lib/reload";
import { useStore } from "@/lib/store";
import { OpenList, ReaderRoute } from "@/screens/ReaderRoute";
import { SearchScreen } from "@/screens/SearchScreen";
import { AppShell } from "@/shell/AppShell";
import { RoutedErrorBoundary } from "@/shell/ErrorBoundary";
import { forgetWizardMemory, resetting, welcomeEntry } from "@/setup/session";
import { RestoreWaiting } from "@/setup/RestoreWaiting";
import { OpenRefusedScreen } from "@/setup/OpenRefused";
import { SignedOut } from "@/setup/SignedOut";
import { lazyScreen } from "@/lib/lazyScreen";
import { StatusBlock } from "@/screens/ListPane";
import { Button } from "@/ui/button";
import { Skeleton } from "@/ui/kit";

// Settings, Feeds and Health load on first visit; the reader and list stay in the main chunk.
// A failed download can be tried again (lib/lazyScreen.ts): React.lazy alone would keep the failure for good.
const FeedsScreen = lazyScreen(() => import("@/screens/FeedsScreen").then((m) => ({ default: m.FeedsScreen })));
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

function Lazy({ children }: { children: React.ReactNode }) {
  return <Suspense fallback={<Skeleton label="Loading screen" />}>{children}</Suspense>;
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
        retry: (n, e) =>
          opts.retry !== false && !(e instanceof ApiError && (e.status === 401 || e.status === 404)) && !failedWhileOffline(e) && n < 2,
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
  const { online } = useStore(offlineStore);
  const qc = useQueryClient();
  const boot = useBootstrap(auth !== "out");
  const refused = useStore(openRefusedStore);
  const reset = useStore(resetting);
  const { pathname } = useLocation();

  // Signed out: drop everything cached so nothing from the last session shows.
  useEffect(() => {
    if (auth === "out") {
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
      forgetWizardMemory();
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
  if (auth === "out") return <SignedOut />;
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
  if (boot.isPending) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
    );
  }
  // The full-screen errors are for a launch with nothing to show. A background refetch that fails later keeps its
  // data (and sets isError alongside it): the reader stays up and the notice at the top explains what happened.
  const failed = boot.isError && !boot.data;
  if (failed && boot.error instanceof ApiError && boot.error.code === SESSION_EXPIRED) {
    return (
      <StatusBlock role="alert" title="Your sign-in has expired" body="The sign-in in front of Kipple timed out. Reload to sign in again.">
        <Button onClick={() => reloadToSignIn()}>Reload</Button>
      </StatusBlock>
    );
  }
  if (failed && !online) {
    return (
      <StatusBlock role="alert" title="You're offline" body="Kipple has nothing saved on this device yet. Open it once while you're online and your unread articles will be here next time.">
        <Button onClick={() => void boot.refetch()}>Try again</Button>
      </StatusBlock>
    );
  }
  if (failed) {
    return (
      <StatusBlock role="alert" title="Something went wrong" body="Kipple couldn't reach the server. Try again, and check the Kipple logs if it keeps happening.">
        <Button onClick={() => void boot.refetch()}>Try again</Button>
      </StatusBlock>
    );
  }
  // First-run setup pending (a fresh account, or "Run setup again"): every screen but /welcome gives way to it. Not on a
  // stored copy of the bootstrap, which may still say so after setup was finished elsewhere.
  if (boot.data?.user.setup_pending === true && !fromCache && pathname !== "/welcome" && !pathname.startsWith("/welcome/")) return <Navigate to={welcomeEntry()} replace />;
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
        <Route path="feeds" element={<Lazy><FeedsScreen /></Lazy>} />
        <Route path="health" element={<Lazy><HealthScreen /></Lazy>} />
        <Route path="search" element={<SearchScreen />} />
        <Route path="stats" element={<Lazy><StatsScreen /></Lazy>} />
        <Route path="stats/wrapped" element={<Lazy><WrappedScreen /></Lazy>} />
        <Route path="settings/*" element={<Lazy><SettingsScreen /></Lazy>} />
        <Route path="*" element={<Navigate to="/l/unread" replace />} />
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
