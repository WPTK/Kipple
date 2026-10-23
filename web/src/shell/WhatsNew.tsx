import { useEffect, useRef, useState } from "react";
import { usePatchSettings } from "@/api/admin";
import { useBootstrap } from "@/api/queries";
import { BUNDLE } from "@/lib/buildInfo";
import { createStore, useStore } from "@/lib/store";
import { WHATS_NEW_SEEN, releasesToShow, whatsNewDue } from "@/lib/whatsNew";
import { loadReleases } from "@/lib/whatsNewData";
import type { Release } from "@/lib/whatsNewParse";
import { Button } from "@/ui/button";
import { Modal } from "@/ui/kit";

/** Set by About's "What's new" button: show the latest releases on request (nothing is remembered from that). */
const whatsNewRequest = createStore(0);
export const openWhatsNew = (): void => whatsNewRequest.set((n) => n + 1);

const MANUAL_RELEASES = 3;

/**
 * Where focus goes once the sheet has closed and nothing has it: the sheet is opened by state, often with no control
 * focused (an upgrade shown on a cold load), so the screen's heading, else the page's main region.
 */
function focusFallback(): void {
  const a = document.activeElement;
  if (a && a !== document.body) return;
  (document.querySelector<HTMLElement>("[data-route-heading]") ?? document.getElementById("main"))?.focus({ preventScroll: true });
}

/** The panel: the sections of the releases it is given, newest first, in the app's sheet (focus trapped, Esc closes). */
export function WhatsNewDialog({ releases, onClose }: { releases: Release[]; onClose: () => void }) {
  return (
    <Modal
      open
      onOpenChange={(o) => (o ? undefined : onClose())}
      onClosed={focusFallback}
      title="What's new in Kipple"
      description={releases.length === 1 ? "What changed in this version." : "What changed since you last looked."}
      footer={<Button onClick={onClose}>Close</Button>}
    >
      {releases.length === 0 ? <p className="py-4 text-sm text-fg2">There is nothing to show.</p> : null}
      {releases.map((r) => (
        <section key={r.version} aria-labelledby={`wn-${r.version}`}>
          <h3 id={`wn-${r.version}`} className="text-base font-bold">
            {r.version}
            {r.date ? <span className="ml-2 text-sm font-normal text-fg2">{r.date}</span> : null}
          </h3>
          {r.intro ? <p className="mt-1 text-sm text-fg2">{r.intro}</p> : null}
          {r.groups.map((g) => (
            <div key={g.kind} className="mt-2">
              <h4 className="text-xs font-semibold tracking-wide text-fg2 uppercase">{g.kind}</h4>
              <ul className="mt-1 list-disc space-y-1 pl-5 text-sm">
                {g.items.map((it, i) => (
                  <li key={i}>{it}</li>
                ))}
              </ul>
            </div>
          ))}
        </section>
      ))}
    </Modal>
  );
}

/**
 * Shows "What's new" once after an upgrade and remembers that in the account's ui.whats_new_seen (global, not per
 * device: one reader should not have to dismiss it on every device). Mounted once in the app shell. It decides from the
 * network bootstrap only (a stored copy may predate the upgrade) and never runs for a development bundle. A new account
 * is created with the running version already on record (server side), so a first run is never shown it. Also opens on
 * request from the About screen.
 */
export function WhatsNewHost() {
  const boot = useBootstrap();
  const patch = usePatchSettings();
  const [shown, setShown] = useState<Release[] | null>(null);
  const started = useRef(false);
  const request = useStore(whatsNewRequest);
  const handled = useRef(request); // a request from before this mount is not ours to answer

  const data = boot.data && !boot.data.fromCache ? boot.data : undefined;
  const seenRaw = data?.settings[WHATS_NEW_SEEN];
  const seen = typeof seenRaw === "string" ? seenRaw : "";
  const due = data ? whatsNewDue(seen, BUNDLE.version) : false;
  const markSeen = () => patch.mutate({ [WHATS_NEW_SEEN]: BUNDLE.version });

  useEffect(() => {
    if (started.current || !due) return;
    started.current = true;
    void loadReleases().then(
      (all) => {
        const list = releasesToShow(all, seen, BUNDLE.version);
        if (list.length > 0) setShown(list);
        else patch.mutate({ [WHATS_NEW_SEEN]: BUNDLE.version });
      },
      () => undefined, // the chunk could not load (offline): try again on the next visit, nothing is marked
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per mount, from the first network bootstrap
  }, [due]);

  const [manual, setManual] = useState<Release[] | null>(null);
  useEffect(() => {
    if (request === handled.current) return;
    handled.current = request;
    void loadReleases().then(
      (all) => setManual(all.slice(0, MANUAL_RELEASES)),
      () => setManual([]),
    );
  }, [request]);

  if (shown) {
    return (
      <WhatsNewDialog
        releases={shown}
        onClose={() => {
          setShown(null);
          markSeen();
        }}
      />
    );
  }
  if (manual) return <WhatsNewDialog releases={manual} onClose={() => setManual(null)} />;
  return null;
}
