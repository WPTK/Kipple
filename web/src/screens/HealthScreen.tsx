import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { Suspense, useMemo, useState } from "react";
import { lazyScreen } from "@/lib/lazyScreen";
import { useQueryClient } from "@tanstack/react-query";
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
import { STATUS_RANK, failureReason, retryLabel, statusInfo } from "@/lib/feedStatus";
import { groupState, toggleGroup, toggleIn } from "@/lib/selection";
import { bytesLabel, fullDate, whenLabel } from "@/lib/format";
import { useWide } from "@/lib/useMedia";
import { BackButton } from "@/ui/BackButton";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";
import { StatusChip } from "./StatusChip";
import { RepairButtons } from "./FeedRepair";
import { DeleteDialog, ToggleDialog } from "./feeds/BulkActions";
import { BottomBar } from "@/ui/BottomBar";
import { DropdownMenu } from "@/ui/menu";

const FeedEditor = lazyScreen(() => import("./feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));

const menuItem = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";

type SortKey = "status" | "title" | "last_success_at" | "next_fetch_at";
type Filter = "all" | "attention" | "off";

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
        <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-56">
          <DropdownMenu.Item className={menuItem} onSelect={() => void run(async () => { const r = await refreshFeed(f.id); if (r.error) throw new Error(r.error); }, `Refreshed ${f.title}`)}>
            Refresh now
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={onLog}>
            Fetch log
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={onEdit}>
            Edit feed
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={() => void run(() => patchFeed(f.id, { enabled: !f.enabled }), f.enabled ? `Paused ${f.title}` : `Resumed ${f.title}`)}>
            {f.enabled ? "Pause this feed" : "Resume this feed"}
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
function RedirectNotice({ f, onDelete }: { f: HealthFeed; onDelete: () => void }) {
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
      const msg = e instanceof ApiError && typeof e.body?.message === "string" ? e.body.message : "";
      toast(code === "url_exists" ? msg || "Another feed already uses the new address." : errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  };
  const keepBoth = async () => {
    setBusy(true);
    try {
      await api(`/api/feeds/${f.id}/redirect/keep`, { method: "POST" });
      invalidateFeeds(qc);
      toast("Keeping both feeds.");
    } catch (e) {
      if (e instanceof ApiError && e.code === "no_redirect") {
        invalidateFeeds(qc); // the card was stale: refresh it
        toast("That redirect is no longer pending.", "error");
      } else {
        toast(errorMessage(e), "error");
      }
    } finally {
      setBusy(false);
    }
  };
  if (f.redirect_owner) {
    return (
      <div className="mt-2 rounded-lg border border-line bg-bg p-2 text-xs">
        <p className="break-words">
          This address redirects to <span className="font-semibold">{f.redirect_owner}</span>, which you already have.
        </p>
        <div className="mt-1 flex flex-wrap gap-2">
          <Button disabled={busy} onClick={onDelete}>
            Delete this feed
          </Button>
          <Button disabled={busy} onClick={() => void keepBoth()}>
            Keep both
          </Button>
        </div>
      </div>
    );
  }
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

/**
 * Why a feed fails, in words, with when it last worked and when Kipple tries again, and the two ways to repair it. The
 * server's own text is behind Details, because it is for a person who has to look the cause up. A feed that works
 * again keeps its earlier error greyed (design 4.6), without the buttons.
 */
function ErrorLine({ f, onEdit }: { f: HealthFeed; onEdit: () => void }) {
  if (!f.last_error) return null;
  const resolved = f.status === "ok" || f.status === "silent";
  const reason = failureReason(f);
  const retry = f.enabled ? retryLabel(f.next_fetch_at) : "";
  return (
    <div className="mt-1 flex flex-col gap-2 text-xs font-normal">
      {resolved ? (
        <p className="break-words text-fg2">
          Earlier error {whenLabel(f.last_error_at)}: {reason}.
        </p>
      ) : (
        <>
          <p className="break-words text-danger">{reason}.</p>
          <p className="text-fg2">{[f.last_success_at ? `Last worked ${whenLabel(f.last_success_at).toLowerCase()}.` : "", retry ? `${retry}.` : ""].filter(Boolean).join(" ")}</p>
          <RepairButtons feedId={f.id} title={f.title} onEdit={onEdit} />
        </>
      )}
      <details>
        <summary className="hit-row inline-flex cursor-pointer items-center text-link underline underline-offset-2">Details</summary>
        <p className="break-words text-fg2">{f.last_error}</p>
      </details>
    </div>
  );
}

/** One count in the summary line: a button that shows only those feeds (a second press shows all). Nothing is drawn for zero. */
function SummaryPart({ n, label, on, onToggle, urgent }: { n: number; label: string; on: boolean; onToggle: () => void; urgent?: boolean }) {
  if (n === 0) return null;
  return (
    <span>
      <button
        type="button"
        aria-pressed={on}
        onClick={onToggle}
        className={`hit-row inline-flex items-center underline underline-offset-2 ${urgent ? "font-semibold text-danger" : "text-link"} ${on ? "decoration-2" : ""}`}
      >
        {n} {label}
      </button>
      .{" "}
    </span>
  );
}

/** Feed health: every feed, sortable and filterable, with the actions that fix common problems. */
export function HealthScreen() {
  useDocumentTitle("Feed health");
  const h = useHealth();
  const boot = useBootstrap();
  const wide = useWide();
  const qc = useQueryClient();
  const [sort, setSort] = useState<SortKey>("status");
  const [dir, setDir] = useState<1 | -1>(1);
  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");
  const [log, setLog] = useState<HealthFeed | null>(null);
  // The feed being edited; `address` opens Edit feed with its address field focused (the repair route).
  const [edit, setEdit] = useState<{ id: string; address: boolean } | null>(null);
  const [selecting, setSelecting] = useState(false);
  const [sel, setSel] = useState<ReadonlySet<string>>(new Set());
  const [bulk, setBulk] = useState<null | "delete" | "enable" | "disable">(null);
  // The feed a row's "Delete this feed" is deleting: the same confirmation as the bulk Delete, for one feed.
  const [deleting, setDeleting] = useState<Feed | null>(null);
  // The Feed is taken when the button is pressed, so the dialog keeps it as the person saw it even after a refetch drops the row.
  const askDelete = (id: string) => {
    const f = boot.data?.feeds.find((x) => x.id === id);
    if (f) setDeleting(f);
    else toast("Your feeds are still loading. Try again in a moment.", "error");
  };

  const feeds = useMemo(() => (h.data ? healthSort(healthFilter(h.data.feeds, filter, q), sort, dir) : []), [h.data, filter, q, sort, dir]);
  // The summary line: how many feeds need attention, are paused, or work. It is also the filter.
  const tally = useMemo(() => {
    const all = (h.data?.feeds ?? []).filter((f) => f.status !== "archive");
    const attention = healthFilter(all, "attention", "").length;
    const off = all.filter((f) => !f.enabled).length;
    return { total: all.length, attention, off, working: all.length - attention - off };
  }, [h.data]);
  const editing = edit ? boot.data?.feeds.find((f) => f.id === edit.id) : undefined;
  const exitSelect = () => {
    setSelecting(false);
    setSel(new Set());
  };
  const check = (id: string) => setSel((s) => toggleIn(s, id));
  const shownIds = useMemo(() => feeds.map((f) => f.id), [feeds]);
  // A filter or search can hide feeds that are still ticked. Everything that counts or acts on the selection
  // uses only the ticked feeds still on screen, so a bulk Delete or Pause never touches a feed you can't see.
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
          {!wide ? <BackButton to="/feeds" ariaLabel="Back to Feeds" /> : null}
          <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
            Feed health
          </h1>
          <Button
            variant="ghost"
            onClick={() => {
              void qc.invalidateQueries({ queryKey: ["health"] });
              announce("Feed health updated");
            }}
          >
            Update list
          </Button>
          <Button variant="ghost" onClick={() => (selecting ? exitSelect() : setSelecting(true))} aria-label={selecting ? "Done" : "Select"}>
            {selecting ? <Check aria-hidden="true" /> : <CheckSquare aria-hidden="true" />}
            <span className={selecting ? undefined : "hidden min-[560px]:inline"}>{selecting ? "Done" : "Select"}</span>
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
            <div className="mb-2 flex items-center gap-2">
              <input
                type="search"
                aria-label="Search feeds"
                placeholder="Search feeds"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                className={`${inputCls} min-w-0 flex-1`}
                autoComplete="off"
              />
              {!wide ? (
                <>
                  <select aria-label="Sort by" value={sort} onChange={(e) => setSort(e.target.value as SortKey)} className={`${inputCls} w-auto shrink-0`}>
                    {(Object.keys(SORT_LABEL) as SortKey[]).map((k) => (
                      <option key={k} value={k}>
                        {SORT_LABEL[k]}
                      </option>
                    ))}
                  </select>
                  <Button onClick={() => setDir((x) => (x === 1 ? -1 : 1))} aria-label={dir === 1 ? "Sort ascending. Switch to descending" : "Sort descending. Switch to ascending"}>
                    {dir === 1 ? <ArrowUp aria-hidden="true" /> : <ArrowDown aria-hidden="true" />}
                  </Button>
                </>
              ) : null}
            </div>
            <p className="mb-2 text-sm text-fg2" data-testid="health-summary">
              <SummaryPart n={tally.attention} label={tally.attention === 1 ? "feed needs attention" : "feeds need attention"} on={filter === "attention"} onToggle={() => setFilter(filter === "attention" ? "all" : "attention")} urgent />
              <SummaryPart n={tally.off} label="paused" on={filter === "off"} onToggle={() => setFilter(filter === "off" ? "all" : "off")} />
              <span>{tally.attention + tally.off === 0 ? `All ${tally.working} feeds working.` : `${tally.working} working.`} </span>
              {filter !== "all" ? (
                <button type="button" className="hit-row inline-flex items-center text-link underline underline-offset-2" onClick={() => setFilter("all")}>
                  Show all
                </button>
              ) : null}
            </p>
            <span role="status" className="sr-only-live">
              Showing {feeds.length} of {tally.total} feeds
            </span>
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
                        <RedirectNotice f={f} onDelete={() => askDelete(f.id)} />
                        <ErrorLine f={f} onEdit={() => setEdit({ id: f.id, address: true })} />
                      </th>
                      <td className="px-3 py-2">
                        <StatusChip status={f.status} />
                        {f.consecutive_failures > 0 ? <span className="block text-xs text-fg2">{f.consecutive_failures} failed in a row</span> : null}
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">{whenLabel(f.last_success_at)}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{f.enabled ? whenLabel(f.next_fetch_at) : "Paused"}</td>
                      <td className="px-3 py-1">
                        <Actions f={f} onEdit={() => setEdit({ id: f.id, address: false })} onLog={() => setLog(f)} />
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
                          {f.consecutive_failures > 0 ? <span className="block text-xs text-fg2">{f.consecutive_failures} failed in a row</span> : null}
                        </div>
                      </div>
                      <Actions f={f} onEdit={() => setEdit({ id: f.id, address: false })} onLog={() => setLog(f)} />
                    </div>
                    <p className="mt-1 text-xs text-fg2">{statusInfo(f.status).hint}</p>
                    <dl className="mt-2 grid grid-cols-2 gap-x-3 text-xs">
                      <dt className="text-fg2">Last success</dt>
                      <dd>{whenLabel(f.last_success_at)}</dd>
                      <dt className="text-fg2">Next fetch</dt>
                      <dd>{f.enabled ? whenLabel(f.next_fetch_at) : "Paused"}</dd>
                    </dl>
                    <RedirectNotice f={f} onDelete={() => askDelete(f.id)} />
                    <ErrorLine f={f} onEdit={() => setEdit({ id: f.id, address: true })} />
                  </li>
                ))}
              </ul>
            )}
            {d.reader_last_seen_at ? <p className="mt-4 text-xs text-fg2">A sync app was last seen {whenLabel(Math.min(d.reader_last_seen_at, Date.now() / 1000)).toLowerCase()}.</p> : null}
          </>
        ) : null}
      </div>
      {selecting ? (
        <BottomBar ariaLabel="Selected feeds">
          <span className="mr-auto text-sm font-semibold" aria-live="polite">
            {shownSel.size} selected
          </span>
          <Button onClick={() => setSel(new Set([...sel, ...shownIds]))} disabled={feeds.length === 0 || allShownSelected}>
            Select all
          </Button>
          <Button onClick={() => setBulk("enable")} disabled={shownSel.size === 0}>
            Resume
          </Button>
          <Button onClick={() => setBulk("disable")} disabled={shownSel.size === 0}>
            Pause
          </Button>
          <Button variant="solid" onClick={() => setBulk("delete")} disabled={shownSel.size === 0}>
            Delete
          </Button>
        </BottomBar>
      ) : null}
      {log ? <LogDialog feed={log} onClose={() => setLog(null)} /> : null}
      {editing ? (
        <Suspense fallback={null}>
          <FeedEditor feed={editing} focusAddress={edit?.address} onClose={() => setEdit(null)} />
        </Suspense>
      ) : null}
      {bulk === "delete" ? (
        <DeleteDialog
          feeds={selectedFeeds}
          onClose={() => setBulk(null)}
          onDone={exitSelect}
        />
      ) : null}
      {deleting ? <DeleteDialog feeds={[deleting]} onClose={() => setDeleting(null)} onDone={() => undefined} /> : null}
      {bulk === "enable" || bulk === "disable" ? (
        <ToggleDialog feeds={selectedFeeds} enable={bulk === "enable"} onClose={() => setBulk(null)} onDone={exitSelect} />
      ) : null}
    </div>
  );
}
