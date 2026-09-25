import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { ApiError, authStore } from "@/api/client";
import { useBootstrap } from "@/api/queries";
import { useStore } from "@/lib/store";
import { FeedsScreen } from "@/screens/FeedTree";
import { LoginScreen } from "@/screens/LoginScreen";
import { ItemRoute, ListRoute } from "@/screens/ReaderRoute";
import { SearchScreen } from "@/screens/SearchScreen";
import { SettingsScreen } from "@/screens/SettingsScreen";
import { AppShell } from "@/shell/AppShell";
import { StatusBlock } from "@/screens/ListPane";
import { Button } from "@/ui/button";

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
  const qc = useQueryClient();
  const boot = useBootstrap(auth !== "out");

  // Signed out: drop everything cached so nothing from the last session shows.
  useEffect(() => {
    if (auth === "out") qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
  }, [auth, qc]);

  if (auth === "out") return <LoginScreen />;
  if (boot.isPending) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
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
        <Route path="l/:view" element={<ListRoute />} />
        <Route path="i/:id" element={<ItemRoute />} />
        <Route path="feeds" element={<FeedsScreen />} />
        <Route path="search" element={<SearchScreen />} />
        <Route path="settings" element={<SettingsScreen />} />
        <Route path="*" element={<Navigate to="/l/unread" replace />} />
      </Route>
    </Routes>
  );
}

export default function App({ client }: { client?: QueryClient }) {
  const qc = client ?? makeQueryClient();
  return (
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <Gate />
      </BrowserRouter>
    </QueryClientProvider>
  );
}
