import { useId, useMemo, useState } from "react";
import { settingsIssues, useSettings, usePatchSettings } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { Button } from "@/ui/button";
import { Notice, Skeleton, inputCls } from "@/ui/kit";
import { toast } from "@/shell/toasts";
import { StepActions, WizardFrame } from "./Frame";
import { isRerun } from "./session";
import { stepById } from "./steps";
import { browserZone, offsetLabel, searchZones, zoneEntries, zoneNames } from "./zones";

/** The time in `zone` right now, for "it is 3:42 PM there", or "" when the browser cannot say. */
function nowIn(zone: string): string {
  try {
    return new Date().toLocaleTimeString([], { timeZone: zone, hour: "numeric", minute: "2-digit" });
  } catch {
    return "";
  }
}

/**
 * Step 3: the time zone Kipple keeps its daily statistics and nightly upkeep in. Starts on the browser's own zone (or
 * the one already saved, when this is a repeat run), with a searchable list of every zone and its offset. Read-only when
 * the server was started with a TZ setting, which then decides.
 */
export function TimeZoneStep({ onNext, onSkipAll, skipAllBusy }: { onNext: () => void; onSkipAll: () => void; skipAllBusy?: boolean }) {
  const settings = useSettings();
  const patch = usePatchSettings();
  const searchId = useId();
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [fellBack, setFellBack] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const entries = useMemo(() => zoneEntries(zoneNames()), []);
  const known = useMemo(() => new Set(entries.map((e) => e.name)), [entries]);
  const browser = browserZone();

  const meta = settings.data?.settings.find((s) => s.key === "tz");
  const env = typeof meta?.env_override === "string" && meta.env_override !== "" ? meta.env_override : null;
  const saved = typeof meta?.value === "string" ? meta.value : "UTC";
  const isDefault = saved === (typeof meta?.default === "string" ? meta.default : "UTC");
  // A zone somebody chose earlier (Run setup again, an upgraded install) stays, UTC included; on a fresh install the
  // browser's zone leads.
  const chosen = !isDefault || isRerun();
  const suggested = chosen ? saved : browser && known.has(browser) ? browser : "UTC";
  const unknownBrowser = !chosen && browser !== "" && !known.has(browser) ? browser : null;
  const selected = picked ?? suggested;

  const shown = useMemo(() => searchZones(entries, query), [entries, query]);

  if (settings.isPending) {
    return (
      <WizardFrame step={stepById("timezone")} onSkipAll={onSkipAll} skipAllBusy={skipAllBusy}>
        <Skeleton rows={3} label="Loading time zones" />
      </WizardFrame>
    );
  }
  if (settings.isError && !settings.data) {
    return (
      <WizardFrame step={stepById("timezone")} onSkipAll={onSkipAll} skipAllBusy={skipAllBusy}>
        <Notice tone="error">Kipple couldn't load its settings. {errorMessage(settings.error)}</Notice>
        <StepActions>
          <Button onClick={() => void settings.refetch()}>Try again</Button>
          <Button variant="solid" onClick={onNext}>
            Skip this step
          </Button>
        </StepActions>
      </WizardFrame>
    );
  }

  /** `skipping`: the zone shown is kept as it would be by Continue, but a failure to save does not hold anyone here. */
  const save = async (skipping = false) => {
    setError(null);
    if (env || selected === saved) {
      onNext();
      return;
    }
    setBusy(true);
    try {
      await patch.mutateAsync({ tz: selected });
      onNext();
    } catch (e) {
      if (skipping) {
        toast("Kipple couldn't save the time zone. You can set it in Settings, Account & Devices.", "error");
        onNext();
        return;
      }
      const bad = settingsIssues(e)?.issues.find((i) => i.key === "tz");
      if (bad) {
        // The server is the validator: a zone it doesn't know falls back to UTC, and says so.
        setFellBack(selected);
        setPicked("UTC");
        setError(`Kipple doesn't know the time zone "${selected}". UTC is selected instead; pick another from the list if you'd like.`);
      } else setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const sel = entries.find((e) => e.name === selected);
  const selectedLabel = sel?.label ?? selected;
  const time = nowIn(selected);

  return (
    <WizardFrame
      step={stepById("timezone")}
      description="Kipple uses this for your daily reading statistics and its nightly upkeep. Your devices still show times in their own zone."
      onSkipAll={onSkipAll}
      skipAllBusy={skipAllBusy}
    >
      <div className="flex flex-1 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {env ? (
          <>
            <Notice>Set by the TZ environment variable; remove it to choose here.</Notice>
            <p className="text-base">
              Kipple is using <strong>{env}</strong> {offsetLabel(env) ? `(${offsetLabel(env)})` : ""}.
            </p>
          </>
        ) : (
          <>
            {unknownBrowser && !fellBack ? (
              <Notice>
                Your browser reported the time zone "{unknownBrowser}", which Kipple doesn't have in its list, so UTC is selected. Choose yours below.
              </Notice>
            ) : null}
            <div className="rounded-xl border border-line bg-surface px-4 py-3" data-testid="selected-zone">
              <p className="text-xs font-semibold tracking-wide text-fg2 uppercase">Selected</p>
              <p className="text-lg font-semibold">{selectedLabel}</p>
              {time ? <p className="text-sm text-fg2">It's {time} there now.</p> : null}
              {chosen && selected === saved ? <p className="mt-1 text-xs text-fg2">Kipple is already set to this zone.</p> : browser && known.has(browser) && selected === browser ? <p className="mt-1 text-xs text-fg2">Suggested from your browser.</p> : null}
            </div>
            <div className="flex flex-col gap-1">
              <label htmlFor={searchId} className="text-sm font-semibold">
                Search time zones
              </label>
              <input id={searchId} type="search" autoComplete="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder="A city, a region or an offset, such as tokyo or +9" className={inputCls} aria-describedby={`${searchId}-h`} />
              <p id={`${searchId}-h`} className="text-xs text-fg2">
                <span role="status">{shown.length === 0 ? "No time zone matches." : `${shown.length} time zone${shown.length === 1 ? "" : "s"} ${query.trim() ? "match" : "in the list"}.`}</span> Use the arrow keys to move through the list.
              </p>
            </div>
            <select
              aria-label="Time zones"
              size={7}
              value={selected}
              onChange={(e) => {
                setPicked(e.target.value);
                setError(null);
              }}
              className={`${inputCls} py-1`}
            >
              {shown.map((z) => (
                <option key={z.name} value={z.name}>
                  {z.label}
                </option>
              ))}
            </select>
          </>
        )}
        <StepActions>
          <Button disabled={busy} onClick={() => void save(true)}>
            Skip
          </Button>
          <Button variant="solid" disabled={busy} onClick={() => void save()}>
            {busy ? "Saving" : "Continue"}
          </Button>
        </StepActions>
        {!env ? <p className="-mt-2 text-right text-xs text-fg2">Skip keeps the zone shown above, so Kipple never stays on UTC by accident.</p> : null}
      </div>
    </WizardFrame>
  );
}
