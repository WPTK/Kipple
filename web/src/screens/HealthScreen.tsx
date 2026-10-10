import { Suspense, useMemo, useState } from "react";
import { lazyScreen } from "@/lib/lazyScreen";
import { useQueryClient } from "@tanstack/react-query";
import { DropdownMenu } from "radix-ui";
import { ArrowDown, ArrowUp, Check, CheckSquare, MoreVertical } from "lucide-react";
import {
  invalidateFeeds,
  patchFeed,
  refreshFeed,
  useFeedLog,
  useHealth,
  type HealthFeed,
} from "@/api/admin";
import { ApiError, api, errorMessage } from "@/api/client";
import { invalidateLists, keys, useBootstrap } from "@/api/queries";
import type { Feed } from "@/api/types";
import { STATUS_RANK, statusInfo } from "@/lib/feedStatus";
import { groupState, toggleGroup, toggleIn } from "@/lib/selection";
import { bytesLabel, fullDate, whenLabel } from "@/lib/format";
import { useWide } from "@/lib/useMedia";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";
import { StatusChip } from "./StatusChip";
import { DeleteDialog, ToggleDialog } from "./feeds/BulkActions";

const FeedEditor = lazyScreen(() => import("./feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));

const menuItem = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";

type SortKey = "status" | "title" | "last_success_at" | "next_fetch_at";
type Filter = "all" | "attention" | "redirects" | "off";

const SORT_LABEL: Record<SortKey, string> = { status: "Status", title: "Feed", last_success_at: "Last success", next_fetch_at: "Next fetch" };

export function healthSort(feeds: HealthFeed[], key: SortKey, dir: 1 | -1): HealthFeed[] {
  const val = (f: HealthFeed): number | string => {
    if (key === "status") return STATUS_RANK[f.status] ?? 9;
    if (key === "title") return f.title.toLowerCase();
    return f[key] ?? 0;
  };
  return [...feeds].sort((a, b) => {
    const x = val(a);
    const y = val(b);
    const c = typeof x === "string" ? x.localeCompare(y as string) : (x as number) - (y as number);
    return (c || a.title.localeCompare(b.title)) * dir;
  });
}

export function healthFilter(feeds: HealthFeed[], filter: Filter, q: string): HealthFeed[] {
  const needle = q.trim().toLowerCase();
  return feeds.filter((f) => {
    if (f.status === "archive") return false;
    if (needle && !`${f.title} ${f.url}`.toLowerCase().includes(needle)) return false;
    switch (filter) {
      case "attention":
        return ["dead", "failing", "erroring", "throttled", "redirecting"].includes(f.status);
      case "redirects":
        return f.redirect_pending || f.notices.some((n) => n.includes("redirect"));
      case "off":
        return !f.enabled;
      default:
        return true;
    }
  });
}

/** The fetch log's outcome codes, in words. */
const OUTCOME_LABEL: Record<string, string> = {
  ok: "Fetched",
  not_modified: "Not modified",
  unchanged: "Nothing new",
  error: "Failed",
  skipped: "Skipped",
  trim_only: "Trimmed old articles",
};

/** Fetch log for one feed (14 days), with "mark this fetch read". */
function LogDialog({ feed, onClose }: { feed: HealthFeed; onClose: () => void }) {
  const q = useFeedLog(feed.id);
  const qc = useQueryClient();
  const markRead = async (rowId: string) => {
    try {
      const r = await api<{ changed: number | string[] }>(`/api/feeds/${feed.id}/mark-fetch-read`, { method: "POST", body: { fetch_log_id: rowId } });
      const n = Array.isArray(r.changed) ? r.changed.length : r.changed;
      toast(`${n} article${n === 1 ? "" : "s"} marked read`);
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      // The reply is only a count, and the items.state event that patches the rows in place can be missed (live
      // updates down): the loaded lists reload the next time they are shown instead of keeping these articles unread.
      invalidateLists(qc, () => true);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title={`Fetch log for ${feed.title}`} description="The last 14 days, newest first." footer={<Button onClick={onClose}>Close</Button>}>
      {q.isPending ? <Skeleton rows={3} label="Loading fetch log" /> : null}
      {q.isError ? <Notice tone="error">Couldn't load the fetch log.</Notice> : null}
      {q.data && q.data.length === 0 ? <p className="text-sm text-fg2">No fetches in the last 14 days.</p> : null}
      <ul className="flex flex-col gap-2">
        {q.data?.map((r) => (
          <li key={r.id} className="rounded-xl border border-line bg-surface p-3 text-sm">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="font-semibold">{OUTCOME_LABEL[r.outcome] ?? r.outcome}</span>
              <span className="text-xs text-fg2">{fullDate(r.started_at)}</span>
            </div>
            <p className="text-fg2">
              {[r.http_status ? `HTTP ${r.http_status}` : null, `${r.new_items} new`, r.updated_items ? `${r.updated_items} updated` : null, r.trimmed_items ? `${r.trimmed_items} trimmed` : null, `${r.duration_ms} ms`, r.bytes ? bytesLabel(r.bytes) : null]
                .filter(Boolean)
                .join(" · ")}
            </p>
            {r.error ? <p className="mt-1 break-words text-danger">{r.error}</p> : null}
            {r.note ? <p className="mt-1 break-words text-fg2">{r.note}</p> : null}
            {r.first_item_id && r.last_item_id && r.new_items > 0 ? (
              <Button className="mt-2" onClick={() => void markRead(r.id)}>
                Mark this fetch read
              </Button>
            ) : null}
          </li>
        ))}
      </ul>
    </Modal>
  );
}

function Actions({ f, onEdit, onLog }: { f: HealthFeed; onEdit: () => void; onLog: () => void }) {
  const qc = useQueryClient();
  const done = () => invalidateFeeds(qc);
  const run = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      done();
      announce(ok);
      toast(ok);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button variant="ghost" size="icon" aria-label={`Actions for ${f.title}`}>
          <MoreVertical aria-hidden="true" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
          <DropdownMenu.Item className={menuItem} onSelect={() => void run(async () => { const r = await refreshFeed(f.id); if (r.error) throw new Error(r.error); }, `Refreshed ${f.title}`)}>
            Refresh now
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={onLog}>
            Fetch log
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={onEdit}>
            Edit feed
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={() => void run(() => patchFeed(f.id, { enabled: !f.enabled }), f.enabled ? `Turned off ${f.title}` : `Turned on ${f.title}`)}>
            {f.enabled ? "Turn off" : "Turn on"}
          </DropdownMenu.Item>
          {f.trimmed_unread_count > 0 ? (
            <DropdownMenu.Item className={menuItem} onSelect={() => void run(() => api(`/api/feeds/${f.id}/trimmed-unread/reset`, { method: "POST" }), "Reset the trimmed-unread count")}>
              Reset trimmed-unread count
            </DropdownMenu.Item>
          ) : null}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

/** The one-tap fix for a permanent redirect. */
function RedirectNotice({ f }: { f: HealthFeed }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  if (!f.redirect_pending || !f.redirect_to) {
    return f.notices.length ? (
      <ul className="mt-1 text-xs text-fg2">
        {f.notices.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    ) : null;
  }
  const update = async () => {
    setBusy(true);
    try {
      await patchFeed(f.id, { url: f.redirect_to });
      invalidateFeeds(qc);
      toast("Feed address updated. Kipple is fetching it now.");
    } catch (e) {
      const code = e instanceof ApiError ? e.code : undefined;
      toast(code === "url_exists" ? "Another feed already uses the new address." : errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-2 rounded-lg border border-line bg-bg p-2 text-xs">
      <p className="break-all">
        This feed moved to <span className="font-semibold">{f.redirect_to}</span>.
      </p>
      <Button className="mt-1" disabled={busy} onClick={() => void update()}>
        Update to new URL
      </Button>
    </div>
  );
}

function ErrorLine({ f }: { f: HealthFeed }) {
  if (!f.last_error) return null;
  const resolved = f.status === "ok" || f.status === "silent";
  return (
    <p className={`mt-1 text-xs break-words ${resolved ? "text-fg2" : "text-danger"}`}>
      {resolved ? "Earlier error" : "Last error"} {whenLabel(f.last_error_at)}: {f.last_error}
    </p>
  );
}

/** Feed health: every feed, sortable and filterable, with the actions that fix common problems. */
export function HealthScreen() {
  const h = useHealth();
  const boot = useBootstrap();
  const wide = useWide();
  const qc = useQueryClient();
  const [sort, setSort] = useState<SortKey>("status");
  const [dir, setDir] = useState<1 | -1>(1);
  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");
  const [log, setLog] = useState<HealthFeed | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [selecting, setSelecting] = useState(false);
  const [sel, setSel] = useState<ReadonlySet<string>>(new Set());
  const [bulk, setBulk] = useState<null | "delete" | "enable" | "disable">(null);

  const feeds = useMemo(() => (h.data ? healthSort(healthFilter(h.data.feeds, filter, q), sort, dir) : []), [h.data, filter, q, sort, dir]);
  const editing = edit ? boot.data?.feeds.find((f) => f.id === edit) : undefined;
  const exitSelect = () => {
    setSelecting(false);
    setSel(new Set());
  };
  const check = (id: string) => setSel((s) => toggleIn(s, id));
  const shownIds = useMemo(() => feeds.map((f) => f.id), [feeds]);
  // A filter or search can hide feeds that are still ticked. Everything that counts or acts on the selection
  // uses only the ticked feeds still on screen, so a bulk Delete or Turn off never touches a feed you can't see.
  const shownSel = useMemo(() => new Set(shownIds.filter((id) => sel.has(id))), [shownIds, sel]);
  const allShownSelected = groupState(shownIds, sel) === "all";
  // Feed Health's own rows (HealthFeed) don't carry folder_id/starred_count; the bulk dialogs work on the
  // full Feed from the bootstrap, matched by id.
  const selectedFeeds: Feed[] = useMemo(() => (boot.data?.feeds ?? []).filter((f) => shownSel.has(f.id)), [boot.data?.feeds, shownSel]);
  const th = (k: SortKey) => (
    <th scope="col" aria-sort={sort === k ? (dir === 1 ? "ascending" : "descending") : "none"} className="px-3 py-2 text-left font-semibold">
      <button
        type="button"
        className="inline-flex min-h-11 items-center gap-1"
        onClick={() => {
          if (sort === k) setDir((d) => (d === 1 ? -1 : 1));
          else {
            setSort(k);
            setDir(1);
          }
        }}
      >
        {SORT_LABEL[k]}
        {sort === k ? dir === 1 ? <ArrowUp aria-hidden="true" className="size-4" /> : <ArrowDown aria-hidden="true" className="size-4" /> : null}
      </button>
    </th>
  );

  const d = h.data;
  const warnings = [...(boot.data?.warnings ?? []).map((w) => w.message)];
  if (d && d.clock.ahead_s > 60) warnings.push(`This server's clock is about ${Math.round(d.clock.ahead_s / 60)} minutes ahead of the time Kipple last heard from the internet. Fetch schedules may be off.`);
  if (d?.snapshot.last_error) warnings.push(`The last automatic database snapshot failed: ${d.snapshot.last_error}`);

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <div className="flex items-center gap-1 pt-2">
          <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
            Feed health
          </h1>
          <Button variant="ghost" onClick={() => (selecting ? exitSelect() : setSelecting(true))} aria-label={selecting ? "Done" : "Select"}>
            {selecting ? <Check aria-hidden="true" /> : <CheckSquare aria-hidden="true" />}
            <span className={selecting ? undefined : "hidden min-[400px]:inline"}>{selecting ? "Done" : "Select"}</span>
          </Button>
        </div>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {h.isPending ? <Skeleton rows={5} label="Loading feed health" /> : null}
        {h.isError ? (
          <Notice tone="error">
            Couldn't load feed health. <Button variant="link" onClick={() => void h.refetch()}>Try again</Button>
          </Notice>
        ) : null}
        {d ? (
          <>
            {warnings.length ? (
              <div className="mb-3 flex flex-col gap-2">
                {warnings.map((w) => (
                  <Notice key={w} tone="warn">
                    {w}
                  </Notice>
                ))}
              </div>
            ) : null}
            <div className="mb-3 flex flex-wrap items-end gap-3">
              <label className="flex min-w-40 flex-1 flex-col gap-1 text-sm font-semibold">
                Search feeds
                <input type="search" value={q} onChange={(e) => setQ(e.target.value)} className={inputCls} autoComplete="off" />
              </label>
              <label className="flex flex-col gap-1 text-sm font-semibold">
                Show
                <select value={filter} onChange={(e) => setFilter(e.target.value as Filter)} className={inputCls}>
                  <option value="all">All feeds</option>
                  <option value="attention">Needs attention</option>
                  <option value="redirects">Moved</option>
                  <option value="off">Turned off</option>
                </select>
              </label>
              {!wide ? (
                <label className="flex flex-col gap-1 text-sm font-semibold">
                  Sort by
                  <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)} className={inputCls}>
                    {(Object.keys(SORT_LABEL) as SortKey[]).map((k) => (
                      <option key={k} value={k}>
                        {SORT_LABEL[k]}
                      </option>
                    ))}
                  </select>
                </label>
              ) : null}
              {!wide ? (
                <Button onClick={() => setDir((x) => (x === 1 ? -1 : 1))} aria-label={dir === 1 ? "Sort ascending. Switch to descending" : "Sort descending. Switch to ascending"}>
                  {dir === 1 ? <ArrowUp aria-hidden="true" /> : <ArrowDown aria-hidden="true" />}
                </Button>
              ) : null}
              <Button onClick={() => { void qc.invalidateQueries({ queryKey: ["health"] }); announce("Feed health refreshed"); }}>Reload</Button>
            </div>
            <p role="status" className="mb-2 text-sm text-fg2">
              {feeds.length} of {d.feeds.filter((f) => f.status !== "archive").length} feeds
              {d.reader_last_seen_at ? ` · A sync app was last seen ${whenLabel(Math.min(d.reader_last_seen_at, Date.now() / 1000)).toLowerCase()}` : ""}
            </p>
            {feeds.length === 0 ? (
              <p className="py-8 text-center text-sm text-fg2">No feeds match.</p>
            ) : wide ? (
              <table className="w-full border-collapse text-sm">
                <caption className="sr-only-live">Feed health</caption>
                <thead>
                  <tr className="border-b border-line">
                    {selecting ? (
                      <th scope="col" className="px-3 py-2">
                        <input
                          type="checkbox"
                          aria-label="Select all feeds"
                          checked={allShownSelected}
                          onChange={() => setSel(toggleGroup(shownIds, sel))}
                          className="size-5 accent-[var(--kp-accent)]"
                        />
                      </th>
                    ) : null}
                    {th("title")}
                    {th("status")}
                    {th("last_success_at")}
                    {th("next_fetch_at")}
                    <th scope="col" className="px-3 py-2 text-left font-semibold">
                      <span className="sr-only-live">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {feeds.map((f) => (
                    <tr key={f.id} className="border-b border-line align-top">
                      {selecting ? (
                        <td className="px-3 py-2">
                          <input
                            type="checkbox"
                            aria-label={`Select ${f.title}`}
                            checked={sel.has(f.id)}
                            onChange={() => check(f.id)}
                            className="size-5 accent-[var(--kp-accent)]"
                          />
                        </td>
                      ) : null}
                      <th scope="row" className="max-w-[24rem] px-3 py-2 text-left font-medium">
                        <span className="block truncate">{f.title}</span>
                        <span className="block truncate text-xs font-normal text-fg2">{f.url}</span>
                        <RedirectNotice f={f} />
                        <ErrorLine f={f} />
                      </th>
                      <td className="px-3 py-2">
                        <StatusChip status={f.status} />
                        {f.consecutive_failures > 0 ? <span className="block text-xs text-fg2">{f.consecutive_failures} failed in a row</span> : null}
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">{whenLabel(f.last_success_at)}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{f.enabled ? whenLabel(f.next_fetch_at) : "Turned off"}</td>
                      <td className="px-3 py-1">
                        <Actions f={f} onEdit={() => setEdit(f.id)} onLog={() => setLog(f)} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <ul className="flex flex-col gap-2">
                {feeds.map((f) => (
                  <li key={f.id} className="rounded-xl border border-line bg-surface p-3">
                    <div className="flex items-start gap-2">
                      {selecting ? (
                        <label className="hit-row -my-1 -ml-2 inline-flex shrink-0 items-center justify-center">
                          <input
                            type="checkbox"
                            aria-label={`Select ${f.title}`}
                            checked={sel.has(f.id)}
                            onChange={() => check(f.id)}
                            className="size-5 accent-[var(--kp-accent)]"
                          />
                        </label>
                      ) : null}
                      <div className="min-w-0 flex-1">
                        <p className="truncate font-semibold">{f.title}</p>
                        <p className="truncate text-xs text-fg2">{f.url}</p>
                        <div className="mt-1">
                          <StatusChip status={f.status} />
                        </div>
                      </div>
                      <Actions f={f} onEdit={() => setEdit(f.id)} onLog={() => setLog(f)} />
                    </div>
                    <p className="mt-1 text-xs text-fg2">{statusInfo(f.status).hint}</p>
                    <dl className="mt-2 grid grid-cols-2 gap-x-3 text-xs">
                      <dt className="text-fg2">Last success</dt>
                      <dd>{whenLabel(f.last_success_at)}</dd>
                      <dt className="text-fg2">Next fetch</dt>
                      <dd>{f.enabled ? whenLabel(f.next_fetch_at) : "Turned off"}</dd>
                    </dl>
                    <RedirectNotice f={f} />
                    <ErrorLine f={f} />
                  </li>
                ))}
              </ul>
            )}
            <p className="mt-4 text-xs text-fg2">
              Database {bytesLabel(d.db.db_bytes + (d.db.wal_bytes ?? 0))}, backups {bytesLabel(d.db.backup_bytes)}
              {typeof d.db.imgcache_bytes === "number" ? `, image cache ${bytesLabel(d.db.imgcache_bytes)}` : ""}, {d.unread_total} unread.
            </p>
          </>
        ) : null}
      </div>
      {selecting ? (
        <div role="region" aria-label="Selected feeds" className="pb-safe flex shrink-0 flex-wrap items-center gap-2 border-t border-line bg-surface px-4 py-2">
          <span className="mr-auto text-sm font-semibold" aria-live="polite">
            {shownSel.size} selected
          </span>
          <Button onClick={() => setSel(new Set([...sel, ...shownIds]))} disabled={feeds.length === 0 || allShownSelected}>
            Select all
          </Button>
          <Button onClick={() => setBulk("enable")} disabled={shownSel.size === 0}>
            Turn on
          </Button>
          <Button onClick={() => setBulk("disable")} disabled={shownSel.size === 0}>
            Turn off
          </Button>
          <Button variant="solid" onClick={() => setBulk("delete")} disabled={shownSel.size === 0}>
            Delete
          </Button>
        </div>
      ) : null}
      {log ? <LogDialog feed={log} onClose={() => setLog(null)} /> : null}
      {editing ? (
        <Suspense fallback={null}>
          <FeedEditor feed={editing} onClose={() => setEdit(null)} />
        </Suspense>
      ) : null}
      {bulk === "delete" ? (
        <DeleteDialog
          feeds={selectedFeeds}
          onClose={() => setBulk(null)}
          onDone={exitSelect}
        />
      ) : null}
      {bulk === "enable" || bulk === "disable" ? (
        <ToggleDialog feeds={selectedFeeds} enable={bulk === "enable"} onClose={() => setBulk(null)} onDone={exitSelect} />
      ) : null}
    </div>
  );
}
