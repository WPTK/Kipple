import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { api } from "@/api/client";
import { keys, type BootstrapAnswer } from "@/api/queries";
import { settingsKey, type SettingsResponse } from "@/api/admin";
import { completeOnboarding } from "./api";
import { endRerun, markRerun, rerunStore } from "./session";
import { welcomePath } from "./steps";

/**
 * Records in the app's copy of the account that first-run setup is (or is not) pending, so the routing follows at once.
 * A bootstrap fetch still in flight (adding feeds refreshes it) was answered before setup ended and would write
 * "pending" back over this, leaving the wizard open: it is cancelled first.
 */
async function setPending(qc: QueryClient, pending: boolean): Promise<void> {
  await qc.cancelQueries({ queryKey: keys.bootstrap });
  qc.setQueryData<BootstrapAnswer>(keys.bootstrap, (old) =>
    old ? { ...old, user: { ...old.user, setup_pending: pending } } : old,
  );
  void qc.invalidateQueries({ queryKey: keys.me });
}

/**
 * Kipple keeps its daily statistics and nightly upkeep in one time zone, UTC until set. Setup takes the browser's zone
 * as that default, so no step asks for it; Settings shows it and changes it. A zone someone already chose is left alone,
 * and a zone the server does not know leaves UTC in place (Settings shows that too), so a failure here never holds
 * setup back.
 */
export async function adoptBrowserZone(): Promise<void> {
  try {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (!zone) return;
    const { settings } = await api<SettingsResponse>("/api/settings", {
      quiet: true,
    });
    const meta = settings.find((s) => s.key === "tz");
    if (!meta || meta.value !== meta.default || meta.value === zone) return;
    await api("/api/settings", {
      method: "PATCH",
      body: { tz: zone },
      quiet: true,
    });
  } catch {
    /* the zone stays as it was; it is in Settings */
  }
}

/**
 * "Continue" on the feeds step (POST /api/onboarding/complete), which throws on a failed call; the caller says so. "Run setup
 * again" opens the step in this tab only and calls nothing.
 */
export function useSetupActions() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return {
    /** Ends the wizard. A first run takes the browser's time zone as Kipple's; Run setup again keeps the saved one. */
    finish: async () => {
      if (!rerunStore.get()) await adoptBrowserZone();
      void qc.invalidateQueries({ queryKey: settingsKey });
      await completeOnboarding();
      endRerun();
      await setPending(qc, false);
    },
    /** Opens the first-run step again in this tab for an account that finished it. Nothing is changed on the server. */
    restart: () => {
      markRerun();
      void navigate(welcomePath("feeds"));
    },
  };
}
