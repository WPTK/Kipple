import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { deleteFeed, deleteFolder, invalidateFeeds, patchFeed, reorder as reorderApi } from "@/api/admin";
import { errorMessage } from "@/api/client";
import type { Feed, Folder } from "@/api/types";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { FolderSelect } from "@/ui/FolderSelect";
import { emptiedFolders, folderPath, folderTree } from "@/lib/folderTree";
import { useBootstrap } from "@/api/queries";
import { visibleFeeds } from "@/lib/visibleFeeds";
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
      toast(`Moved ${feeds.length} feed${feeds.length === 1 ? "" : "s"} to ${folderPath(folderTree(folders), target.id)}`);
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
      <Field label="Move to folder">{(a) => <FolderSelect {...a} value={dest} onChange={setDest} />}</Field>
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

/** One progress bar for a running bulk action: the button stays put and only the bar moves. */
function BulkProgress({ label, done, total }: { label: string; done: number | null; total: number }) {
  if (done === null) return null;
  return (
    <div role="status" className="flex flex-col gap-1 text-sm">
      <span>{label}</span>
      <progress className="h-2 w-full accent-[var(--kp-accent)]" value={done} max={total} aria-label={label} />
    </div>
  );
}

/**
 * Turn the selected feeds on or off, one at a time (a feed has no bulk endpoint of its own): progress and a
 * per-feed error list, same shape as the delete dialog. One bad feed does not stop the rest.
 */
export function ToggleDialog({ feeds: selected, enable, onClose, onDone }: { feeds: Feed[]; enable: boolean; onClose: () => void; onDone: () => void }) {
  const [feeds] = useState(selected); // fixed when the dialog opens: the live selection shrinks as feeds change
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
              {busy ? "Working…" : `${verb} ${count} feed${count === 1 ? "" : "s"}`}
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
          {busy ? <BulkProgress label={`${enable ? "Turning on" : "Turning off"} feeds`} done={progress} total={count} /> : null}
        </>
      )}
    </Modal>
  );
}

/**
 * Delete the selected feeds one at a time (DELETE /api/feeds/{id}), with progress and a per-feed error list:
 * one bad feed does not stop the rest. Starred articles move to the Archive unless the switch says otherwise.
 */
export function DeleteDialog({ feeds: selected, onClose, onDone }: { feeds: Feed[]; onClose: () => void; onDone: (deletedIds: string[]) => void }) {
  const [feeds] = useState(selected); // fixed when the dialog opens: the live selection shrinks as feeds are deleted
  const qc = useQueryClient();
  const { count, starred } = deleteSummary(feeds);
  const [alsoStarred, setAlsoStarred] = useState(false);
  const { progress, report, setReport, busy, runEach } = useBulkRun(feeds);
  // Folders this leaves empty are deleted too. Read from the library as it was when the dialog opened: it shrinks
  // as the feeds go.
  const boot = useBootstrap();
  const [library] = useState(() => ({ folders: boot.data?.folders ?? [], feeds: visibleFeeds(boot.data?.feeds) }));
  const emptiedFor = (gone: ReadonlySet<string>) => emptiedFolders(library.folders, library.feeds, gone);
  const folderName = (id: string) => library.folders.find((f) => f.id === id)?.name ?? id;
  const leaving = emptiedFor(new Set(feeds.map((f) => f.id)));

  const run = async () => {
    const { succeeded: deleted, failed } = await runEach((f) => deleteFeed(f.id, alsoStarred).then(() => undefined));
    let folders = 0;
    for (const id of emptiedFor(new Set(deleted))) {
      try {
        await deleteFolder(id);
        folders++;
      } catch (e) {
        failed.push({ title: folderName(id), message: errorMessage(e) });
      }
    }
    invalidateFeeds(qc);
    void qc.invalidateQueries({ queryKey: ["items"] });
    onDone(deleted);
    if (failed.length === 0) {
      toast(`Deleted ${deleted.length} feed${deleted.length === 1 ? "" : "s"}${folders > 0 ? ` and ${folders} empty folder${folders === 1 ? "" : "s"}` : ""}`);
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
              {busy ? "Deleting…" : `Delete ${count} feed${count === 1 ? "" : "s"}`}
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
          {leaving.length > 0 ? (
            <p className="text-sm">
              {leaving.length === 1 ? "This folder is left empty, so it is deleted too" : "These folders are left empty, so they are deleted too"}:{" "}
              {leaving.map(folderName).join(", ")}.
            </p>
          ) : null}
          {busy ? <BulkProgress label="Deleting feeds" done={progress} total={count} /> : null}
        </>
      )}
    </Modal>
  );
}

/**
 * Delete a folder. By default its feeds are kept and move to `fallback` (what the server does). The other choice
 * deletes the feeds of the whole subtree first, then the folder; it asks for the folder's name, and if any feed
 * fails the folder is left alone so no surviving feed is moved by accident.
 */
export function DeleteFolderDialog({
  folder,
  title,
  keepText,
  feeds: inside,
  focusNext,
  onClose,
}: {
  folder: Folder;
  title: string;
  /** What deleting the folder alone does, in a sentence. */
  keepText: string;
  /** The feeds in the folder and all its subfolders. */
  feeds: Feed[];
  focusNext?: string;
  onClose: (focusFolder?: string) => void;
}) {
  const qc = useQueryClient();
  const [feeds] = useState(inside);
  const { count, starred } = deleteSummary(feeds);
  const [mode, setMode] = useState<"keep" | "all">("keep");
  const [alsoStarred, setAlsoStarred] = useState(false);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [working, setWorking] = useState(false);
  const { progress, report, setReport, busy, runEach } = useBulkRun(feeds);
  const all = mode === "all";
  const ready = !all || typed.trim() === folder.name;
  const plural = `${count} feed${count === 1 ? "" : "s"}`;

  const run = async () => {
    setWorking(true);
    setError(null);
    try {
      if (all) {
        const { succeeded, failed } = await runEach((f) => deleteFeed(f.id, alsoStarred).then(() => undefined));
        if (failed.length > 0) {
          invalidateFeeds(qc);
          announce(`Deleted ${succeeded.length}, ${failed.length} failed. The folder was kept.`);
          setReport({ done: succeeded.length, failed });
          return;
        }
      }
      await deleteFolder(folder.id);
      invalidateFeeds(qc);
      void qc.invalidateQueries({ queryKey: ["items"] });
      toast(all ? `Deleted folder ${folder.name} and ${plural}` : `Deleted folder ${folder.name}`);
      onClose(focusNext);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setWorking(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !working && onClose()}
      title={report ? "Some feeds were not deleted" : title}
      description={report ? `${report.done} deleted, ${report.failed.length} failed. The folder was kept.` : mode === "keep" ? keepText : undefined}
      footer={
        report ? (
          <Button variant="solid" onClick={() => onClose()}>
            Close
          </Button>
        ) : (
          <>
            <Button onClick={() => onClose()} disabled={working}>
              Cancel
            </Button>
            <Button variant="solid" disabled={working || !ready} onClick={() => void run()}>
              {working ? "Deleting…" : all ? `Delete folder and ${plural}` : "Delete folder"}
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
          {error ? <Notice tone="error">{error}</Notice> : null}
          {count > 0 ? (
            <fieldset className="flex flex-col gap-2 text-sm" disabled={working}>
              <legend className="sr-only">What happens to the feeds</legend>
              <label className="flex min-h-11 items-center gap-3">
                <input type="radio" name="folder-feeds" checked={!all} onChange={() => setMode("keep")} className="size-5 accent-[var(--kp-accent)]" />
                Keep the feeds
              </label>
              <label className="flex min-h-11 items-center gap-3">
                <input type="radio" name="folder-feeds" checked={all} onChange={() => setMode("all")} className="size-5 accent-[var(--kp-accent)]" />
                Delete the {plural} too
              </label>
            </fieldset>
          ) : null}
          {all ? (
            <>
              <Notice tone="warn">
                This permanently deletes {plural} and their articles, and your sync apps stop seeing them.
                {starred > 0 ? ` They hold ${starred} starred article${starred === 1 ? "" : "s"}, kept in the Archive unless you choose below.` : ""}
              </Notice>
              {starred > 0 ? <Switch label="Delete starred articles too" checked={alsoStarred} onChange={setAlsoStarred} disabled={working} help="This can't be undone." /> : null}
              <ul className="max-h-40 overflow-y-auto text-sm text-fg2" aria-label="Feeds to delete">
                {feeds.map((f) => (
                  <li key={f.id} className="truncate">
                    {f.title}
                  </li>
                ))}
              </ul>
              <Field label={`Type ${folder.name} to confirm`}>
                {(a) => <input {...a} type="text" autoComplete="off" autoCapitalize="off" value={typed} onChange={(e) => setTyped(e.target.value)} className={inputCls} />}
              </Field>
              {busy ? <BulkProgress label="Deleting feeds" done={progress} total={count} /> : null}
            </>
          ) : null}
        </>
      )}
    </Modal>
  );
}
