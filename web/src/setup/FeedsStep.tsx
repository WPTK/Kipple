import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { invalidateFeeds } from "@/api/admin";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Notice, Skeleton, Switch } from "@/ui/kit";
import { fetchStarterFeeds, subscribeStarter, type StarterFeed } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

export const STARTER_KEY = ["starter-feeds"] as const;

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
 * Step 6: a starter set of feeds to subscribe to, in one checklist per category. Everything here is optional: nothing
 * is added until "Add" is pressed, a feed already in Kipple shows as added, and the list may be empty.
 */
export function FeedsStep({ onBack, onNext, onSkipAll, skipAllBusy }: { onBack: () => void; onNext: () => void; onSkipAll: () => void; skipAllBusy?: boolean }) {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: STARTER_KEY, queryFn: ({ signal }) => fetchStarterFeeds(signal), retry: false, staleTime: 0 });
  // What the person changed from the file's own suggestions; a feed not in here follows its `checked` flag.
  const [picks, setPicks] = useState<Record<string, boolean>>({});
  const [folders, setFolders] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isOn = (f: StarterFeed) => (f.subscribed ? false : (picks[f.id] ?? f.checked));
  const all = list.data?.categories.flatMap((c) => c.feeds) ?? [];
  const chosen = all.filter(isOn);

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
      onNext();
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
            <div className="flex flex-wrap gap-2">
              <Button className="min-h-11" aria-label={`Select all in ${c.title}`} onClick={() => setMany(c.feeds, true)}>
                Select all
              </Button>
              <Button className="min-h-11" aria-label={`Select none in ${c.title}`} onClick={() => setMany(c.feeds, false)}>
                Select none
              </Button>
            </div>
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
                        {host || f.lang ? (
                          <span className="block text-xs text-fg2">
                            {host}
                            {host && f.lang ? " · " : ""}
                            {f.lang ? f.lang.toUpperCase() : ""}
                          </span>
                        ) : null}
                      </span>
                    </label>
                  </li>
                );
              })}
            </ul>
          </fieldset>
        ))}
        <Switch label="Put each category in its own folder" help="Kipple makes a folder named after each category and files its feeds there." checked={folders} onChange={setFolders} />
        <p role="status" className="text-sm text-fg2">
          {chosen.length === 0 ? "No feeds selected." : `${chosen.length} feed${chosen.length === 1 ? "" : "s"} selected.`}
        </p>
      </>
    );
  }

  return (
    <WizardFrame
      step={stepById("feeds")}
      description="A few good feeds to start with, so Kipple isn't empty. Untick anything that isn't for you. You can add, remove or move feeds any time."
      onSkipAll={onSkipAll}
      skipAllBusy={skipAllBusy || busy}
    >
      <div className="flex flex-1 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {body}
        <StepActions
          back={
            <Button disabled={busy} onClick={onBack}>
              Back
            </Button>
          }
        >
          <Button disabled={busy} onClick={onNext}>
            Skip
          </Button>
          <Button variant="solid" disabled={busy || chosen.length === 0} onClick={() => void add()}>
            {busy ? "Adding" : chosen.length === 0 ? "Add feeds" : `Add ${chosen.length} feed${chosen.length === 1 ? "" : "s"}`}
          </Button>
        </StepActions>
      </div>
    </WizardFrame>
  );
}
