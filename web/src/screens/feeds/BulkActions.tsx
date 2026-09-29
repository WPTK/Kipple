import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { deleteFeed, invalidateFeeds, patchFeed, reorder as reorderApi } from "@/api/admin";
import { errorMessage } from "@/api/client";
import type { Feed, Folder } from "@/api/types";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";

/** What a bulk delete tells the user before it starts: the count and the starred articles at stake. */
export function deleteSummary(feeds: readonly Feed[]): { count: number; starred: number } {
  return { count: feeds.length, starred: feeds.reduce((n, f) => n + (f.starred_count ?? 0), 0) };
}

/** Move the selected feeds to the end of another folder: one atomic POST /api/reorder. */
export function MoveDialog({
  feeds,
  folders,
  allFeeds,
  onClose,
  onDone,
}: {
  feeds: Feed[];
  folders: Folder[];
  allFeeds: Feed[];
  onClose: () => void;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [dest, setDest] = useState(folders.find((f) => !feeds.every((x) => x.folder_id === f.id))?.id ?? folders[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    const target = folders.find((f) => f.id === dest);
    if (!target) return;
    setBusy(true);
    setError(null);
    try {
      const moved = new Set(feeds.map((f) => f.id));
      // The destination's own feeds keep their order; the moved ones go to the end in the order they are listed.
      const keep = allFeeds.filter((f) => f.folder_id === dest && !f.is_archive && !moved.has(f.id)).map((f) => f.id);
      await reorderApi({ feeds: [{ folder_id: dest, ids: [...keep, ...allFeeds.filter((f) => moved.has(f.id)).map((f) => f.id)] }] });
      invalidateFeeds(qc);
      toast(`Moved ${feeds.length} feed${feeds.length === 1 ? "" : "s"} to ${target.name}`);
      onDone();
      onClose();
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={`Move ${feeds.length} feed${feeds.length === 1 ? "" : "s"}`}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="solid" disabled={busy || !dest} onClick={() => void run()}>
            {busy ? "Moving" : "Move"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <Field label="Move to folder">
        {(a) => (
          <select {...a} value={dest} onChange={(e) => setDest(e.target.value)} className={inputCls}>
            {folders.map((f) => (
              <option key={f.id} value={f.id}>
                {f.name}
              </option>
            ))}
          </select>
        )}
      </Field>
    </Modal>
  );
}

/** How a one-feed-at-a-time bulk action ended when some feeds failed. */
export interface BulkReport {
  done: number;
  failed: { title: string; message: string }[];
}

/**
 * The state and loop the bulk dialogs share: run `op` on each feed in turn (a feed has no bulk endpoint of its
 * own), tracking progress; one bad feed does not stop the rest. Returns the ids that succeeded and the failures.
 */
function useBulkRun(feeds: readonly Feed[]) {
  const [progress, setProgress] = useState<number | null>(null);
  const [report, setReport] = useState<BulkReport | null>(null);
  const busy = progress !== null && report === null;
  const runEach = async (op: (f: Feed) => Promise<void>) => {
    const failed: BulkReport["failed"] = [];
    const succeeded: string[] = [];
    setProgress(0);
    for (const [i, f] of feeds.entries()) {
      try {
        await op(f);
        succeeded.push(f.id);
      } catch (e) {
        failed.push({ title: f.title, message: errorMessage(e) });
      }
      setProgress(i + 1);
    }
    return { succeeded, failed };
  };
  return { progress, report, setReport, busy, runEach };
}

/**
 * Turn the selected feeds on or off, one at a time (a feed has no bulk endpoint of its own): progress and a
 * per-feed error list, same shape as the delete dialog. One bad feed does not stop the rest.
 */
export function ToggleDialog({ feeds, enable, onClose, onDone }: { feeds: Feed[]; enable: boolean; onClose: () => void; onDone: () => void }) {
  const qc = useQueryClient();
  const count = feeds.length;
  const { progress, report, setReport, busy, runEach } = useBulkRun(feeds);
  const verb = enable ? "Turn on" : "Turn off";

  const run = async () => {
    const { succeeded, failed } = await runEach((f) => patchFeed(f.id, { enabled: enable }).then(() => undefined));
    const done = succeeded.length;
    invalidateFeeds(qc);
    onDone();
    if (failed.length === 0) {
      toast(`${enable ? "Turned on" : "Turned off"} ${done} feed${done === 1 ? "" : "s"}`);
      onClose();
    } else {
      announce(`${enable ? "Turned on" : "Turned off"} ${done}, ${failed.length} failed`);
      setReport({ done, failed });
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={report ? "Some feeds were not changed" : `${verb} ${count} feed${count === 1 ? "" : "s"}?`}
      description={report ? `${report.done} changed, ${report.failed.length} failed.` : undefined}
      footer={
        report ? (
          <Button variant="solid" onClick={onClose}>
            Close
          </Button>
        ) : (
          <>
            <Button onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="solid" disabled={busy} onClick={() => void run()}>
              {busy ? `Working ${progress} of ${count}` : `${verb} ${count} feed${count === 1 ? "" : "s"}`}
            </Button>
          </>
        )
      }
    >
      {report ? (
        <ul className="flex flex-col gap-2 text-sm" aria-label="Feeds that could not be changed">
          {report.failed.map((f, i) => (
            <li key={i}>
              <span className="font-semibold">{f.title}</span>: {f.message}
            </li>
          ))}
        </ul>
      ) : (
        <>
          <ul className="max-h-40 overflow-y-auto text-sm text-fg2" aria-label={`Feeds to ${enable ? "turn on" : "turn off"}`}>
            {feeds.map((f) => (
              <li key={f.id} className="truncate">
                {f.title}
              </li>
            ))}
          </ul>
          {busy ? (
            <p role="status" className="text-sm">
              Working {progress} of {count}
            </p>
          ) : null}
        </>
      )}
    </Modal>
  );
}

/**
 * Delete the selected feeds one at a time (DELETE /api/feeds/{id}), with progress and a per-feed error list:
 * one bad feed does not stop the rest. Starred articles move to the Archive unless the switch says otherwise.
 */
export function DeleteDialog({ feeds, onClose, onDone }: { feeds: Feed[]; onClose: () => void; onDone: (deletedIds: string[]) => void }) {
  const qc = useQueryClient();
  const { count, starred } = deleteSummary(feeds);
  const [alsoStarred, setAlsoStarred] = useState(false);
  const { progress, report, setReport, busy, runEach } = useBulkRun(feeds);

  const run = async () => {
    const { succeeded: deleted, failed } = await runEach((f) => deleteFeed(f.id, alsoStarred).then(() => undefined));
    invalidateFeeds(qc);
    void qc.invalidateQueries({ queryKey: ["items"] });
    onDone(deleted);
    if (failed.length === 0) {
      toast(`Deleted ${deleted.length} feed${deleted.length === 1 ? "" : "s"}`);
      onClose();
    } else {
      announce(`Deleted ${deleted.length}, ${failed.length} failed`);
      setReport({ done: deleted.length, failed });
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={report ? "Some feeds were not deleted" : `Delete ${count} feed${count === 1 ? "" : "s"}?`}
      description={report ? `${report.done} deleted, ${report.failed.length} failed.` : "Their articles are removed from Kipple. Your sync apps stop seeing these feeds."}
      footer={
        report ? (
          <Button variant="solid" onClick={onClose}>
            Close
          </Button>
        ) : (
          <>
            <Button onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="solid" disabled={busy} onClick={() => void run()}>
              {busy ? `Deleting ${progress} of ${count}` : `Delete ${count} feed${count === 1 ? "" : "s"}`}
            </Button>
          </>
        )
      }
    >
      {report ? (
        <ul className="flex flex-col gap-2 text-sm" aria-label="Feeds that could not be deleted">
          {report.failed.map((f, i) => (
            <li key={i}>
              <span className="font-semibold">{f.title}</span>: {f.message}
            </li>
          ))}
        </ul>
      ) : (
        <>
          {starred > 0 ? (
            <>
              <Notice tone="warn">
                These feeds hold {starred} starred article{starred === 1 ? "" : "s"} in total. By default they are kept in the Archive.
              </Notice>
              <Switch label="Delete starred articles too" checked={alsoStarred} onChange={setAlsoStarred} disabled={busy} help="This can't be undone." />
            </>
          ) : (
            <p className="text-sm text-fg2">None of these feeds has starred articles.</p>
          )}
          <ul className="max-h-40 overflow-y-auto text-sm text-fg2" aria-label="Feeds to delete">
            {feeds.map((f) => (
              <li key={f.id} className="truncate">
                {f.title}
              </li>
            ))}
          </ul>
          {busy ? (
            <p role="status" className="text-sm">
              Deleting {progress} of {count}
            </p>
          ) : null}
        </>
      )}
    </Modal>
  );
}
