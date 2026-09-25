import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { DropdownMenu } from "radix-ui";
import { ArrowDown, ArrowUp, CircleAlert, CircleCheck, CirclePause, FolderPlus, HeartPulse, MoreVertical, Pencil, Plus, Upload, Download } from "lucide-react";
import { createFolder, deleteFolder, invalidateFeeds, patchFeed, patchFolder } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { useBootstrap } from "@/api/queries";
import type { Feed, Folder } from "@/api/types";
import { LAYOUT_IDS, LAYOUT_LABELS, setLayoutOverride, useDevicePrefs, type LayoutId } from "@/lib/devicePrefs";
import { statusInfo } from "@/lib/feedStatus";
import { listTo } from "@/lib/routes";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { toast } from "@/shell/toasts";
import { AddFeedDialog } from "./feeds/AddFeedDialog";
import { FeedEditor } from "./feeds/FeedEditor";
import { OpmlImportDialog } from "./feeds/OpmlDialog";

const row = "flex min-h-11 items-center gap-2 rounded-lg px-3 hover:bg-selection";
const menuItem = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";

function Badge({ n }: { n: number }) {
  if (n <= 0) return null;
  return (
    <span className="ml-auto shrink-0 rounded-full bg-surface px-2 py-0.5 text-xs font-semibold text-fg2 tabular-nums">
      <span className="sr-only-live">Unread </span>
      {n > 9999 ? "9999+" : n}
    </span>
  );
}

/** Icon and text together: status is never color alone. */
export function StatusChip({ status }: { status: string }) {
  const s = statusInfo(status);
  const Icon = s.tone === "ok" ? CircleCheck : s.tone === "muted" ? CirclePause : CircleAlert;
  return (
    <span className={`inline-flex items-center gap-1 text-xs ${s.tone === "bad" ? "font-semibold text-danger" : "text-fg2"}`}>
      <Icon aria-hidden="true" className="size-4" />
      {s.label}
    </span>
  );
}

type FolderDialog = { kind: "new" } | { kind: "rename"; folder: Folder } | { kind: "delete"; folder: Folder };

function FolderDialogs({ dialog, onClose }: { dialog: FolderDialog; onClose: () => void }) {
  const qc = useQueryClient();
  const dp = useDevicePrefs();
  const [name, setName] = useState(dialog.kind === "rename" ? dialog.folder.name : "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const fail = (e: unknown) => {
    const code = (e as { code?: string }).code;
    setError(code === "folder_exists" ? "A folder with that name already exists." : errorMessage(e));
  };
  const run = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      invalidateFeeds(qc);
      toast(done);
      onClose();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };

  if (dialog.kind === "delete") {
    const f = dialog.folder;
    return (
      <Modal
        open
        onOpenChange={(o) => !o && onClose()}
        title={`Delete ${f.name}?`}
        description="Its feeds are not deleted. They move to the default folder."
        footer={
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="solid" disabled={busy} onClick={() => void run(() => deleteFolder(f.id), `Deleted folder ${f.name}`)}>
              Delete folder
            </Button>
          </>
        }
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
      </Modal>
    );
  }
  const isNew = dialog.kind === "new";
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={isNew ? "New folder" : "Rename folder"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="solid"
            disabled={busy || !name.trim()}
            onClick={() => void run(() => (isNew ? createFolder(name.trim()) : patchFolder(dialog.folder.id, { name: name.trim() })), isNew ? "Folder created" : "Folder renamed")}
          >
            {isNew ? "Create" : "Save"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <form
        onSubmit={(e) => {
          e.preventDefault();
        }}
        className="flex flex-col gap-4"
      >
        <Field label="Name">
          {(a) => <input {...a} type="text" maxLength={100} autoFocus value={name} onChange={(e) => setName(e.target.value)} className={inputCls} />}
        </Field>
        {!isNew ? (
          <Field label="Layout on this device" help="Overrides the device default for every feed in this folder that has no layout of its own.">
            {(a) => (
              <select
                {...a}
                value={dp.overrides.folder[dialog.folder.id] ?? "default"}
                onChange={(e) => setLayoutOverride("folder", dialog.folder.id, e.target.value === "default" ? null : (e.target.value as LayoutId))}
                className={inputCls}
              >
                <option value="default">Use device default</option>
                {LAYOUT_IDS.map((l) => (
                  <option key={l} value={l}>
                    {LAYOUT_LABELS[l]}
                  </option>
                ))}
              </select>
            )}
          </Field>
        ) : null}
      </form>
    </Modal>
  );
}

/** Renumber positions 0..n-1 in the given order (the server stores absolute positions). */
async function renumber(kind: "folder" | "feed", ids: string[]): Promise<void> {
  for (const [i, id] of ids.entries()) await (kind === "folder" ? patchFolder(id, { position: i }) : patchFeed(id, { position: i }));
}

function move<T>(list: T[], i: number, d: -1 | 1): T[] {
  const out = [...list];
  const [x] = out.splice(i, 1);
  out.splice(i + d, 0, x as T);
  return out;
}

export function FirstRun({ onAdd, onImport }: { onAdd: () => void; onImport: () => void }) {
  return (
    <div role="status" className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center">
      <h2 className="text-lg font-semibold">No feeds yet</h2>
      <p className="text-sm text-fg2">Add a feed by its address, or import an OPML file from another reader.</p>
      <div className="flex flex-wrap justify-center gap-2">
        <Button variant="solid" onClick={onAdd}>
          <Plus aria-hidden="true" />
          Add your first feed
        </Button>
        <Button onClick={onImport}>
          <Upload aria-hidden="true" />
          Import OPML
        </Button>
      </div>
    </div>
  );
}

/** Feeds: where you open a folder or feed, and where feeds and folders are added, edited, ordered and removed. */
export function FeedsScreen() {
  const boot = useBootstrap();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const c = boot.data?.counts;
  const [adding, setAdding] = useState(false);
  const [importing, setImporting] = useState(false);
  const [editing, setEditing] = useState<Feed | null>(null);
  const [folderDialog, setFolderDialog] = useState<FolderDialog | null>(null);
  const [reorder, setReorder] = useState(false);

  const reorderTo = async (kind: "folder" | "feed", ids: string[]) => {
    try {
      await renumber(kind, ids);
      invalidateFeeds(qc);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const feeds = boot.data?.feeds ?? [];
  const folders = boot.data?.folders ?? [];
  const realFeeds = feeds.filter((f) => !f.is_archive);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <div className="flex items-center gap-1 pt-2">
          <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
            Feeds
          </h1>
          <Button variant="ghost" onClick={() => setAdding(true)} aria-label="Add feed">
            <Plus aria-hidden="true" />
            <span className="hidden min-[400px]:inline">Add feed</span>
          </Button>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button variant="ghost" size="icon" aria-label="Feed actions">
                <MoreVertical aria-hidden="true" />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
                <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "new" })}>
                  <FolderPlus className="size-5" aria-hidden="true" />
                  New folder
                </DropdownMenu.Item>
                <DropdownMenu.Item className={menuItem} onSelect={() => setImporting(true)}>
                  <Upload className="size-5" aria-hidden="true" />
                  Import OPML
                </DropdownMenu.Item>
                <DropdownMenu.Item asChild className={menuItem}>
                  <a href="/api/opml" download>
                    <Download className="size-5" aria-hidden="true" />
                    Export OPML
                  </a>
                </DropdownMenu.Item>
                <DropdownMenu.Item className={menuItem} onSelect={() => setReorder((r) => !r)}>
                  <ArrowUp className="size-5" aria-hidden="true" />
                  {reorder ? "Done reordering" : "Reorder folders and feeds"}
                </DropdownMenu.Item>
                <DropdownMenu.Item className={menuItem} onSelect={() => navigate("/health")}>
                  <HeartPulse className="size-5" aria-hidden="true" />
                  Feed health
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {boot.isPending ? <Skeleton rows={5} label="Loading feeds" /> : null}
        {boot.isError ? <Notice tone="error">Couldn't load your feeds. Check your connection and try again.</Notice> : null}
        {boot.data && realFeeds.length === 0 ? (
          <FirstRun onAdd={() => setAdding(true)} onImport={() => setImporting(true)} />
        ) : boot.data ? (
          <>
            <ul className="mb-2 flex flex-col gap-1">
              <li>
                <Link to={listTo({ view: "unread" })} className={row}>
                  Unread
                  <Badge n={c?.unread ?? 0} />
                </Link>
              </li>
              <li>
                <Link to={listTo({ view: "all" })} className={row}>
                  All articles
                </Link>
              </li>
              <li>
                <Link to={listTo({ view: "starred" })} className={row}>
                  Starred
                  <Badge n={c?.starred ?? 0} />
                </Link>
              </li>
            </ul>
            <ul className="flex flex-col gap-1">
              {folders.map((fo, fi) => {
                const inFolder = feeds.filter((f) => f.folder_id === fo.id && !f.is_archive);
                return (
                  <li key={fo.id}>
                    <div className="flex items-center">
                      <Link to={listTo({ view: "unread", folder: fo.id })} className={`${row} min-w-0 flex-1 text-sm font-semibold`}>
                        <span className="truncate">{fo.name}</span>
                        <Badge n={fo.unread} />
                      </Link>
                      {reorder ? (
                        <>
                          <Button variant="ghost" size="icon" aria-label={`Move folder ${fo.name} up`} disabled={fi === 0} onClick={() => void reorderTo("folder", move(folders, fi, -1).map((x) => x.id))}>
                            <ArrowUp aria-hidden="true" />
                          </Button>
                          <Button variant="ghost" size="icon" aria-label={`Move folder ${fo.name} down`} disabled={fi === folders.length - 1} onClick={() => void reorderTo("folder", move(folders, fi, 1).map((x) => x.id))}>
                            <ArrowDown aria-hidden="true" />
                          </Button>
                        </>
                      ) : (
                        <DropdownMenu.Root>
                          <DropdownMenu.Trigger asChild>
                            <Button variant="ghost" size="icon" aria-label={`Folder actions for ${fo.name}`}>
                              <MoreVertical aria-hidden="true" />
                            </Button>
                          </DropdownMenu.Trigger>
                          <DropdownMenu.Portal>
                            <DropdownMenu.Content align="end" sideOffset={4} className="z-50 min-w-48 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
                              <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "rename", folder: fo })}>
                                <Pencil className="size-5" aria-hidden="true" />
                                Rename or set layout
                              </DropdownMenu.Item>
                              {!fo.is_default ? (
                                <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "delete", folder: fo })}>
                                  Delete folder
                                </DropdownMenu.Item>
                              ) : null}
                            </DropdownMenu.Content>
                          </DropdownMenu.Portal>
                        </DropdownMenu.Root>
                      )}
                    </div>
                    <ul>
                      {inFolder.map((f, i) => (
                        <li key={f.id} className="flex items-center">
                          <Link to={listTo({ view: "unread", feed: f.id })} className={`${row} min-w-0 flex-1 pl-6 text-sm`}>
                            <span className="min-w-0">
                              <span className="block truncate">{f.title}</span>
                              {f.status !== "ok" ? <StatusChip status={f.status} /> : null}
                            </span>
                            <Badge n={f.unread} />
                          </Link>
                          {reorder ? (
                            <>
                              <Button variant="ghost" size="icon" aria-label={`Move ${f.title} up`} disabled={i === 0} onClick={() => void reorderTo("feed", move(inFolder, i, -1).map((x) => x.id))}>
                                <ArrowUp aria-hidden="true" />
                              </Button>
                              <Button variant="ghost" size="icon" aria-label={`Move ${f.title} down`} disabled={i === inFolder.length - 1} onClick={() => void reorderTo("feed", move(inFolder, i, 1).map((x) => x.id))}>
                                <ArrowDown aria-hidden="true" />
                              </Button>
                            </>
                          ) : (
                            <Button variant="ghost" size="icon" aria-label={`Edit ${f.title}`} onClick={() => setEditing(f)}>
                              <Pencil aria-hidden="true" />
                            </Button>
                          )}
                        </li>
                      ))}
                    </ul>
                  </li>
                );
              })}
            </ul>
            <Link to="/health" className={`${row} mt-2 text-sm`}>
              <HeartPulse aria-hidden="true" className="size-5" />
              Feed health
            </Link>
          </>
        ) : null}
      </div>
      {adding ? <AddFeedDialog onClose={() => setAdding(false)} onOpenFeed={(id) => { setAdding(false); navigate(listTo({ view: "unread", feed: id })); }} /> : null}
      {importing ? <OpmlImportDialog onClose={() => setImporting(false)} /> : null}
      {editing ? <FeedEditor feed={editing} onClose={() => setEditing(null)} /> : null}
      {folderDialog ? <FolderDialogs dialog={folderDialog} onClose={() => setFolderDialog(null)} /> : null}
    </div>
  );
}
