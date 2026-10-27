import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { invalidateFeeds } from "@/api/admin";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Checkbox, Notice, Skeleton } from "@/ui/kit";
import { OpmlImportDialog } from "@/screens/feeds/OpmlDialog";
import { fetchStarterFeeds, subscribeStarter, type StarterFeed } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

const STARTER_KEY = ["starter-feeds"] as const;

function feedsError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "unknown_id") return "The list of recommended feeds changed while this page was open. Reload the page and pick again.";
    if (e.code === "unavailable") return "Kipple has no recommended feeds to add right now. You can add feeds yourself from Feeds.";
  }
  return errorMessage(e);
}

/** "example.com" from a feed's site address, for the small line under its title. */
function hostOf(url: string | undefined): string {
  if (!url) return "";
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

/**
 * Step 2: a starter set of feeds to subscribe to, in one checklist per category with a single select all or none.
 * Nothing is ticked at first and nothing is added until "Add" is pressed. A feed already in Kipple shows as added, and
 * the list may be empty. "Import a feeds file" opens the same import as Settings. The one button ends setup: it adds
 * what is ticked, or just continues when nothing is.
 */
export function FeedsStep({ onDone, finishing }: { onDone: () => void; finishing?: boolean }) {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: STARTER_KEY, queryFn: ({ signal }) => fetchStarterFeeds(signal), retry: false, staleTime: 0 });
  // The feeds the person ticked or unticked; a feed not in here is not ticked.
  const [picks, setPicks] = useState<Record<string, boolean>>({});
  const [folders, setFolders] = useState(true);
  const [busy, setBusy] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isOn = (f: StarterFeed) => (f.subscribed ? false : (picks[f.id] ?? false));
  const all = list.data?.categories.flatMap((c) => c.feeds) ?? [];
  const chosen = all.filter(isOn);
  const pickable = all.filter((f) => !f.subscribed);
  const allOn = pickable.length > 0 && pickable.every(isOn);

  const setMany = (feeds: StarterFeed[], on: boolean) =>
    setPicks((p) => ({ ...p, ...Object.fromEntries(feeds.filter((f) => !f.subscribed).map((f) => [f.id, on])) }));

  const add = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await subscribeStarter(chosen.map((f) => f.id), folders);
      invalidateFeeds(qc);
      void qc.invalidateQueries({ queryKey: STARTER_KEY });
      announce(`Added ${r.added} feed${r.added === 1 ? "" : "s"}${r.existing ? `, ${r.existing} already in Kipple` : ""}`);
      onDone();
    } catch (e) {
      setError(feedsError(e));
    } finally {
      setBusy(false);
    }
  };

  let body;
  if (list.isPending) body = <Skeleton rows={4} label="Loading recommended feeds" />;
  else if (list.isError) {
    body = (
      <Notice tone="error">
        Kipple couldn't load the recommended feeds. {errorMessage(list.error)}{" "}
        <Button variant="link" onClick={() => void list.refetch()}>
          Try again
        </Button>
      </Notice>
    );
  } else if (!list.data.available || list.data.categories.length === 0 || all.length === 0) {
    body = (
      <Notice>
        <p className="font-semibold">No recommended feeds are available.</p>
        <p className="mt-1">You can add feeds by their address, or import a file, from Feeds any time.</p>
      </Notice>
    );
  } else {
    body = (
      <>
        {list.data.categories.map((c) => (
          <fieldset key={c.id} className="flex min-w-0 flex-col gap-2 rounded-xl border border-line p-3" data-category={c.id}>
            <legend className="px-1 text-base font-bold">{c.title}</legend>
            <ul className="flex flex-col">
              {c.feeds.map((f) => {
                const host = hostOf(f.site ?? f.url);
                return (
                  <li key={f.id}>
                    <label className="flex min-h-11 cursor-pointer items-start gap-3 rounded-lg px-1 py-2 has-[:disabled]:cursor-default">
                      <input type="checkbox" checked={f.subscribed || isOn(f)} disabled={f.subscribed} onChange={(e) => setMany([f], e.target.checked)} className="mt-0.5 size-5 shrink-0 accent-[var(--kp-accent)]" />
                      <span className="min-w-0">
                        <span className="block text-sm font-semibold">
                          {f.title}
                          {f.subscribed ? <span className="ml-2 text-xs font-normal text-fg2">Already added</span> : null}
                        </span>
                        {f.description ? <span className="block text-xs text-fg2">{f.description}</span> : null}
                        {host ? <span className="block text-xs text-fg2">{host}</span> : null}
                      </span>
                    </label>
                  </li>
                );
              })}
            </ul>
          </fieldset>
        ))}
        <Checkbox label="Put each category in its own folder" checked={folders} onChange={setFolders} />
      </>
    );
  }

  return (
    <WizardFrame step={stepById("feeds")} description="Tick the feeds you want. You can change them any time.">
      <div className="flex flex-1 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {list.isPending ? null : (
          <div className="flex flex-wrap items-center justify-between gap-2">
            {pickable.length > 0 ? (
              <Button className="min-h-11" onClick={() => setMany(all, !allOn)}>
                {allOn ? "Select none" : "Select all"}
              </Button>
            ) : (
              <span />
            )}
            <Button variant="link" className="min-h-11" onClick={() => setImporting(true)}>
              Import a feeds file
            </Button>
          </div>
        )}
        {body}
        <StepActions>
          <Button variant="solid" disabled={busy || finishing} onClick={() => (chosen.length === 0 ? onDone() : void add())}>
            {busy ? "Adding" : finishing ? "Finishing" : chosen.length === 0 ? "Continue" : `Add ${chosen.length} feed${chosen.length === 1 ? "" : "s"}`}
          </Button>
        </StepActions>
      </div>
      {importing ? <OpmlImportDialog
          inWizard
          onClose={() => {
            setImporting(false);
            // The file may hold recommended feeds: the list shows those as already added.
            void qc.invalidateQueries({ queryKey: STARTER_KEY });
          }}
        /> : null}
    </WizardFrame>
  );
}
