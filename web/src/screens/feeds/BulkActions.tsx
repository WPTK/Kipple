import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { deleteFeeds, deleteFolder, invalidateFeeds, patchFeed, reorder as reorderApi, useDeleteArticleCount } from "@/api/admin";
import { errorMessage } from "@/api/client";
import type { Feed, Folder } from "@/api/types";
import { Button } from "@/ui/button";
import { Checkbox, Field, Modal, Notice, RadioRow, inputCls } from "@/ui/kit";
import { FolderSelect } from "@/ui/FolderSelect";
import { folderPath, folderTree } from "@/lib/folderTree";
import { announce, toast } from "@/shell/toasts";

/** What a bulk delete tells the user before it starts: the count and the starred articles at stake. */
function deleteSummary(feeds: readonly Feed[]): { count: number; starred: number } {
  return { count: feeds.length, starred: feeds.reduce((n, f) => n + (f.starred_count ?? 0), 0) };
}

const articlesOf = (n: number) => `${n} article${n === 1 ? "" : "s"}`;

/**
 * The delete dialogs' wording, right for one feed and for several, and right for the "Delete starred articles too"
 * box: the one place the singular, plural and checked forms live (the Feeds screen, the folder dialog and the feed
 * editor all read it). `articles` is how many articles the feeds hold in all, from the server's count (null while
 * counting, or if it could not be counted); the starred ones stay in the Archive unless `alsoStarred`. The button
 * names the outcome: "Delete feed and 312 articles".
 */
export function deleteCopy(count: number, starred: number, alsoStarred = false, articles: number | null = null) {
  const one = count === 1;
  const keptNote = starred > 0 ? (alsoStarred ? ", starred ones too" : ", apart from starred ones kept in the Archive") : "";
  const removed = articles === null ? null : Math.max(0, articles - (alsoStarred ? 0 : starred));
  return {
    feeds: one ? "feed" : "feeds",
    consequence: `${removed === null ? `Removes ${one ? "its" : "their"} articles${keptNote}` : removed === 0 ? "Removes no articles" : `Removes ${articlesOf(removed)}`}. This can't be undone.`,
    /** What the folder dialog's warning says follows the feeds. */
    removed,
    button: `Delete ${one ? "feed" : `${count} feeds`}${removed ? ` and ${articlesOf(removed)}` : ""}`,
    starredWarning: `${one ? "This feed holds" : "These feeds hold"} ${starred} starred article${starred === 1 ? "" : "s"}${one ? "" : " in total"}. ${
      alsoStarred ? `${starred === 1 ? "It is" : "They are"} deleted too.` : `By default ${starred === 1 ? "it is" : "they are"} kept in the Archive.`
    }`,
    /** The line under the box beside the warning. */
    starredHelp: "Removes them from the Archive too.",
    noStarred: one ? "This feed has no starred articles." : "None of these feeds has starred articles.",
  };
}

/** The starred notice and the box that decides their fate, shared by every delete dialog. */
export function StarredChoice({
  count,
  starred,
  alsoStarred,
  onAlsoStarred,
  disabled,
}: {
  count: number;
  starred: number;
  alsoStarred: boolean;
  onAlsoStarred: (v: boolean) => void;
  disabled?: boolean;
}) {
  const copy = deleteCopy(count, starred, alsoStarred);
  return (
    <>
      <Notice tone="warn">{copy.starredWarning}</Notice>
      <Checkbox label="Delete starred articles too" checked={alsoStarred} onChange={onAlsoStarred} disabled={disabled} help={copy.starredHelp} />
    </>
  );
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
  // No folder is chosen at first: one tap on Move must not file the feeds somewhere the person did not pick.
  const [dest, setDest] = useState("");
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
      <Field label="Move to folder">{(a) => <FolderSelect {...a} value={dest} onChange={setDest} none="Choose a folder" />}</Field>
    </Modal>
  );
}

/** How a one-feed-at-a-time bulk action ended when some feeds failed. */
interface BulkReport {
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
 * Pause or resume the selected feeds, one at a time (a feed has no bulk endpoint of its own): progress and a
 * per-feed error list, same shape as the delete dialog. One bad feed does not stop the rest.
 */
export function ToggleDialog({ feeds: selected, enable, onClose, onDone }: { feeds: Feed[]; enable: boolean; onClose: () => void; onDone: () => void }) {
  const [feeds] = useState(selected); // fixed when the dialog opens: the live selection shrinks as feeds change
  const qc = useQueryClient();
  const count = feeds.length;
  const { progress, report, setReport, busy, runEach } = useBulkRun(feeds);
  const verb = enable ? "Resume" : "Pause";

  const run = async () => {
    const { succeeded, failed } = await runEach((f) => patchFeed(f.id, { enabled: enable }).then(() => undefined));
    const done = succeeded.length;
    invalidateFeeds(qc);
    onDone();
    if (failed.length === 0) {
      toast(`${enable ? "Resumed" : "Paused"} ${done} feed${done === 1 ? "" : "s"}`);
      onClose();
    } else {
      announce(`${enable ? "Resumed" : "Paused"} ${done}, ${failed.length} failed`);
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
          <ul className="max-h-40 overflow-y-auto text-sm text-fg2" aria-label={`Feeds to ${enable ? "resume" : "pause"}`}>
            {feeds.map((f) => (
              <li key={f.id} className="truncate">
                {f.title}
              </li>
            ))}
          </ul>
          {busy ? <BulkProgress label={`${enable ? "Resuming" : "Pausing"} feeds`} done={progress} total={count} /> : null}
        </>
      )}
    </Modal>
  );
}

/** The feeds about to be deleted: the starred warning and checkbox, and the list. */
function FeedsToDelete({
  feeds,
  starred,
  alsoStarred,
  onAlsoStarred,
  disabled,
  noneNote,
}: {
  feeds: readonly Feed[];
  starred: number;
  alsoStarred: boolean;
  onAlsoStarred: (v: boolean) => void;
  disabled: boolean;
  noneNote?: boolean;
}) {
  const copy = deleteCopy(feeds.length, starred);
  return (
    <>
      {starred > 0 ? (
        <StarredChoice count={feeds.length} starred={starred} alsoStarred={alsoStarred} onAlsoStarred={onAlsoStarred} disabled={disabled} />
      ) : noneNote ? (
        <p className="text-sm text-fg2">{copy.noStarred}</p>
      ) : null}
      <ul className="max-h-40 overflow-y-auto text-sm text-fg2" aria-label="Feeds to delete">
        {feeds.map((f) => (
          <li key={f.id} className="truncate">
            {f.title}
            {f.address_hint ? <span className="text-xs"> ({f.address_hint})</span> : null}
          </li>
        ))}
      </ul>
    </>
  );
}

/** A delete is one server call, so there is no count to show: a moving bar and what is being done. */
function DeleteWorking({ count }: { count: number }) {
  return (
    <div role="status" className="flex flex-col gap-1 text-sm">
      <span>
        Deleting {count} feed{count === 1 ? "" : "s"}. This keeps going on the server if you close this page.
      </span>
      <progress className="h-2 w-full accent-[var(--kp-accent)]" aria-label="Deleting feeds" />
    </div>
  );
}

/**
 * Delete the selected feeds in one call (POST /api/feeds/delete): the server marks them all at once and finishes
 * the job even if this page is closed, so a half-deleted library cannot happen. Starred articles move to the
 * Archive unless the checkbox says otherwise.
 */
export function DeleteDialog({
  feeds: selected,
  onClose,
  onDone,
}: {
  feeds: Feed[];
  onClose: () => void;
  /** Called when the run ends: the ids deleted, and whether every selected feed was. */
  onDone: (deletedIds: string[], all: boolean) => void;
}) {
  const [feeds] = useState(selected); // fixed when the dialog opens: the live selection shrinks as feeds are deleted
  const qc = useQueryClient();
  const { count, starred } = deleteSummary(feeds);
  const articles = useDeleteArticleCount(feeds.map((f) => f.id)) ?? null;
  const [alsoStarred, setAlsoStarred] = useState(false);
  const copy = deleteCopy(count, starred, alsoStarred, articles);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      const { deleted } = await deleteFeeds(
        feeds.map((f) => f.id),
        alsoStarred,
      );
      invalidateFeeds(qc);
      void qc.invalidateQueries({ queryKey: ["items"] });
      onDone(
        feeds.map((f) => f.id),
        true,
      );
      // 0 is a retry that found the feeds already gone.
      const n = deleted || count;
      toast(`Deleted ${n} feed${n === 1 ? "" : "s"}`);
      onClose();
    } catch (e) {
      // The server may have finished anyway (a dropped connection): look again, and say so.
      invalidateFeeds(qc);
      setError(`${errorMessage(e)} Some of the feeds may be gone already; the list shows what is left.`);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={`Delete ${count} ${copy.feeds}?`}
      description={copy.consequence}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {error ? "Close" : "Cancel"}
          </Button>
          <Button variant="danger" disabled={busy} onClick={() => void run()}>
            {busy ? "Deleting…" : error ? "Try again" : copy.button}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <FeedsToDelete feeds={feeds} starred={starred} alsoStarred={alsoStarred} onAlsoStarred={setAlsoStarred} disabled={busy} noneNote />
      {busy ? <DeleteWorking count={count} /> : null}
    </Modal>
  );
}

/**
 * Delete a folder. By default its feeds are kept and move to the default folder (what the server does). The other
 * choice deletes the feeds of the whole subtree first, then the folder; it asks for the folder's name, and if any feed
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
  const articles = useDeleteArticleCount(feeds.map((f) => f.id), mode === "all") ?? null;
  const removed = deleteCopy(count, starred, alsoStarred, articles).removed;
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [working, setWorking] = useState(false);
  // The feeds are gone once the delete call has answered: a retry after the folder step failed must not run it again.
  const [feedsGone, setFeedsGone] = useState(false);
  const all = mode === "all";
  const ready = !all || typed.trim() === folder.name.trim();
  const plural = `${count} feed${count === 1 ? "" : "s"}`;

  const run = async () => {
    setWorking(true);
    setError(null);
    try {
      if (all && !feedsGone) {
        await deleteFeeds(
          feeds.map((f) => f.id),
          alsoStarred,
        );
        setFeedsGone(true);
      }
      await deleteFolder(folder.id);
      invalidateFeeds(qc);
      void qc.invalidateQueries({ queryKey: ["items"] });
      toast(all ? `Deleted folder ${folder.name} and ${plural}` : `Deleted folder ${folder.name}`);
      onClose(focusNext);
    } catch (e) {
      invalidateFeeds(qc);
      setError(errorMessage(e));
    } finally {
      setWorking(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !working && onClose()}
      title={title}
      description={mode === "keep" ? keepText : undefined}
      footer={
        (
          <>
            <Button onClick={() => onClose()} disabled={working}>
              Cancel
            </Button>
            <Button variant="danger" disabled={working || !ready} onClick={() => void run()}>
              {working ? "Deleting…" : all ? (feedsGone ? "Delete the folder" : `Delete folder and ${plural}${removed ? ` and ${articlesOf(removed)}` : ""}`) : "Delete folder"}
            </Button>
          </>
        )
      }
    >
      {
        <>
          {error ? <Notice tone="error">{error}</Notice> : null}
          {count > 0 ? (
            <fieldset className="flex flex-col gap-2 text-sm" disabled={working || feedsGone}>
              <legend className="sr-only">What happens to the feeds</legend>
              <RadioRow name="folder-feeds" disabled={working || feedsGone} checked={!all} onSelect={() => setMode("keep")}>
                Keep the feeds
              </RadioRow>
              <RadioRow name="folder-feeds" disabled={working || feedsGone} checked={all} onSelect={() => setMode("all")}>
                Delete the {plural} too
              </RadioRow>
            </fieldset>
          ) : null}
          {all ? (
            <>
              <Notice tone="warn">
                Deletes {plural}{removed === null ? " and their articles" : removed === 0 ? "" : ` and ${articlesOf(removed)}`}. This can't be undone.
              </Notice>
              <FeedsToDelete feeds={feeds} starred={starred} alsoStarred={alsoStarred} onAlsoStarred={setAlsoStarred} disabled={working || feedsGone} />
              <Field label={`Type ${folder.name} to confirm`}>
                {(a) => <input {...a} type="text" autoComplete="off" autoCapitalize="off" value={typed} onChange={(e) => setTyped(e.target.value)} className={inputCls} />}
              </Field>
              {working ? <DeleteWorking count={count} /> : null}
            </>
          ) : null}
        </>
      }
    </Modal>
  );
}
