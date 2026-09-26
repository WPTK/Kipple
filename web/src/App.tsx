import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { Suspense, lazy, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { ApiError, authStore, SESSION_EXPIRED } from "@/api/client";
import { useBootstrap } from "@/api/queries";
import { hydrateDevice } from "@/lib/deviceSync";
import { prefetchUnread } from "@/lib/offline";
import { offlineStore } from "@/lib/offlineState";
import { useStore } from "@/lib/store";
import { LoginScreen } from "@/screens/LoginScreen";
import { ReaderRoute } from "@/screens/ReaderRoute";
import { SearchScreen } from "@/screens/SearchScreen";
import { AppShell } from "@/shell/AppShell";
import { ErrorBoundary } from "@/shell/ErrorBoundary";
import { StatusBlock } from "@/screens/ListPane";
import { Button } from "@/ui/button";
import { Skeleton } from "@/ui/kit";

// Settings, Feeds and Health load on first visit; the reader and list stay in the main chunk.
const FeedsScreen = lazy(() => import("@/screens/FeedsScreen").then((m) => ({ default: m.FeedsScreen })));
const HealthScreen = lazy(() => import("@/screens/HealthScreen").then((m) => ({ default: m.HealthScreen })));
const SettingsScreen = lazy(() => import("@/screens/SettingsScreen").then((m) => ({ default: m.SettingsScreen })));

function Lazy({ children }: { children: React.ReactNode }) {
  return <Suspense fallback={<Skeleton label="Loading screen" />}>{children}</Suspense>;
}

export function makeQueryClient(opts: { retry?: boolean } = {}): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (n, e) => opts.retry !== false && !(e instanceof ApiError && (e.status === 401 || e.status === 404)) && n < 2,
        refetchOnWindowFocus: false,
      },
    },
  });
}

function Gate() {
  const auth = useStore(authStore);
  const { online } = useStore(offlineStore);
  const qc = useQueryClient();
  const boot = useBootstrap(auth !== "out");

  // Signed out: drop everything cached so nothing from the last session shows.
  useEffect(() => {
    if (auth === "out") qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
  }, [auth, qc]);

  // The device profile is the truth for per-device settings; the local cache is reconciled with it once.
  const device = boot.data?.device;
  useEffect(() => {
    hydrateDevice(device);
  }, [device]);

  // Keep the first page of Unread on the device for offline reading (the service worker stores it).
  const ready = boot.isSuccess && auth === "in";
  useEffect(() => {
    if (ready) void prefetchUnread();
  }, [ready]);

  if (auth === "out") return <LoginScreen />;
  if (boot.isPending) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
    );
  }
  if (boot.isError && boot.error instanceof ApiError && boot.error.code === SESSION_EXPIRED) {
    return (
      <StatusBlock role="alert" title="Your sign-in has expired" body="The sign-in in front of Kipple timed out. Reload to sign in again.">
        <Button onClick={() => window.location.reload()}>Reload</Button>
      </StatusBlock>
    );
  }
  if (boot.isError && !online) {
    return (
      <StatusBlock role="alert" title="You're offline" body="Kipple has nothing saved on this device yet. Open it once while you're online and your unread articles will be here next time.">
        <Button onClick={() => void boot.refetch()}>Try again</Button>
      </StatusBlock>
    );
  }
  if (boot.isError) {
    return (
      <StatusBlock role="alert" title="Something went wrong" body="Kipple couldn't reach the server. Try again, and check the Kipple logs if it keeps happening.">
        <Button onClick={() => void boot.refetch()}>Try again</Button>
      </StatusBlock>
    );
  }
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Navigate to="/l/unread" replace />} />
        <Route element={<ReaderRoute />}>
          <Route path="l/:view" />
          <Route path="i/:id" />
        </Route>
        <Route path="feeds" element={<Lazy><FeedsScreen /></Lazy>} />
        <Route path="health" element={<Lazy><HealthScreen /></Lazy>} />
        <Route path="search" element={<SearchScreen />} />
        <Route path="settings" element={<Lazy><SettingsScreen /></Lazy>} />
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
        <ErrorBoundary>
          <Gate />
        </ErrorBoundary>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
