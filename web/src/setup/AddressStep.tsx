import { useState } from "react";
import { settingsIssues, useSettings, usePatchSettings } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { CONNECTION_KEYS } from "@/screens/ConnectionSection";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Notice, Skeleton, inputCls } from "@/ui/kit";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

/**
 * Never a suggestion: an IP address or localhost (only this computer or this network uses them), and a name any device
 * on the local network can answer (a single-word name, or one under .local, .lan, .home.arpa, .internal, .home,
 * .localdomain, .fritz.box or .corp). Such a name is still accepted when typed, but open mode answers it only once it is
 * listed under Allowed host names, so suggesting it would offer an address that may not open without a password.
 */
function notSuggested(hostname: string): boolean {
  const h = hostname.toLowerCase().replace(/^\[|\]$/g, "").replace(/\.$/, "");
  if (h === "localhost" || h.endsWith(".localhost") || /^[0-9.]+$/.test(h) || h.includes(":")) return true;
  return !h.includes(".") || LAN_ZONES.some((z) => h === z || h.endsWith(`.${z}`));
}

/** The name zones a device on the local network can answer (the server's setup.LANClaimable list). */
const LAN_ZONES = ["local", "lan", "home.arpa", "internal", "home", "localdomain", "fritz.box", "corp"];

/** The address this page was opened at, when it is a name worth keeping (see notSuggested). */
export function suggestedAddress(loc: Pick<Location, "origin" | "hostname"> = window.location): string {
  return notSuggested(loc.hostname) ? "" : loc.origin;
}

/**
 * Step 6: the public URL, the address other devices open Kipple at. Starts on the saved one, or on the address this
 * page was opened at when that is a name. Continue saves it; Skip leaves it as it is. Everything else about reaching
 * Kipple (allowed names, a reverse proxy, Cloudflare Access) is in Settings.
 */
export function AddressStep({ onBack, onNext, onSkipAll, skipAllBusy }: { onBack: () => void; onNext: () => void; onSkipAll: () => void; skipAllBusy?: boolean }) {
  const settings = useSettings();
  const patch = usePatchSettings();
  const [typed, setTyped] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const step = stepById("address");

  if (settings.isPending) {
    return (
      <WizardFrame step={step} onSkipAll={onSkipAll} skipAllBusy>
        <Skeleton rows={2} label="Loading settings" />
      </WizardFrame>
    );
  }
  if (settings.isError && !settings.data) {
    return (
      <WizardFrame step={step} onSkipAll={onSkipAll} skipAllBusy={skipAllBusy}>
        <Notice tone="error">Kipple couldn't load its settings. {errorMessage(settings.error)}</Notice>
        <StepActions back={<Button onClick={onBack}>Back</Button>}>
          <Button onClick={() => void settings.refetch()}>Try again</Button>
          <Button variant="solid" onClick={onNext}>
            Skip this step
          </Button>
        </StepActions>
      </WizardFrame>
    );
  }

  const meta = settings.data?.settings.find((s) => s.key === CONNECTION_KEYS.publicUrl);
  const saved = typeof meta?.value === "string" ? meta.value : "";
  const suggestion = suggestedAddress();
  const draft = typed ?? (saved || suggestion);
  const suggested = typed === null && saved === "" && suggestion !== "";

  /**
   * Saves the address shown when it differs from the saved one, then goes on with `proceed`. `skipping`: a failure to save
   * is said and does not hold anyone here.
   */
  const save = async (proceed: () => void, skipping = false) => {
    setError(null);
    const value = draft.trim();
    if (value === saved) {
      proceed();
      return;
    }
    setBusy(true);
    try {
      await patch.mutateAsync({ [CONNECTION_KEYS.publicUrl]: value });
      proceed();
    } catch (e) {
      if (skipping) {
        toast("Kipple couldn't save the address. You can set it in Settings, Account & Devices.", "error");
        proceed();
        return;
      }
      const bad = settingsIssues(e)?.issues.find((i) => i.key === CONNECTION_KEYS.publicUrl);
      setError(bad ? bad.message : errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <WizardFrame
      step={step}
      description="The address you open Kipple at from your other devices. Sync apps get feed icons from it, and Kipple always answers at it."
      // Skipping the rest keeps an address typed here, never one only suggested.
      onSkipAll={() => (typed === null ? onSkipAll() : void save(onSkipAll, true))}
      skipAllBusy={skipAllBusy || busy}
    >
      <form
        className="flex flex-1 flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          void save(onNext);
        }}
      >
        <Field
          label="Public URL (optional)"
          help={suggested ? "Suggested from the address you opened Kipple at. Clear it if your other devices use a different one." : "Such as https://rss.example.com. Leave it empty if Kipple only runs on this computer."}
          error={error}
        >
          {(a) => (
            <input
              {...a}
              type="text"
              inputMode="url"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              placeholder="https://rss.example.com"
              value={draft}
              onChange={(e) => {
                setTyped(e.target.value);
                setError(null);
              }}
              className={inputCls}
            />
          )}
        </Field>
        <p className="text-sm text-fg2">
          Behind a reverse proxy or Cloudflare Access, or opening Kipple by another name on your network? Set that up later in Settings, Account &amp; Devices, Address and access. Changes there apply at once.
        </p>
        <StepActions back={<Button onClick={onBack}>Back</Button>}>
          <Button type="button" disabled={busy} onClick={onNext}>
            Skip
          </Button>
          <Button type="submit" variant="solid" disabled={busy}>
            {busy ? "Saving" : "Continue"}
          </Button>
        </StepActions>
      </form>
    </WizardFrame>
  );
}
