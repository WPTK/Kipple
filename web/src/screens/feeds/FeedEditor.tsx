import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { deleteFeed, invalidateFeeds, loadFeedDetail, patchFeed, refreshFeed, type FeedDetail } from "@/api/admin";
import { useBootstrap } from "@/api/queries";
import type { Feed } from "@/api/types";
import { LAYOUT_IDS, LAYOUT_LABELS, setLayoutOverride, useDevicePrefs, type LayoutId } from "@/lib/devicePrefs";
import { AUTO_READ_PRESETS, autoReadLabel } from "@/api/autoRead";
import { Button } from "@/ui/button";
import { AutoReadCatchUp } from "../AutoReadCatchUp";
import { Disclosure, Field, Modal, Notice, Skeleton, Switch, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";
import { intervalLabel } from "@/lib/interval";

const INTERVALS = [15, 30, 60, 120, 360, 720, 1440, 10080];
const RETENTIONS = [50, 100, 250, 500, 1000, 0];
const retentionLabel = (n: number) => (n === 0 ? "Unlimited" : `Newest ${n}`);

interface Form {
  title: string;
  url: string;
  folder: string;
  interval: string; // "" = default
  retention: string; // "" = default
  autoRead: string; // "" = follow the global setting, "0" = off, else days
  fulltext: boolean;
  enabled: boolean;
  dedup: FeedDetail["dedup_mode"];
  userAgent: string;
  auth: string;
  clearAuth: boolean;
  ignoreCache: boolean;
  noHttp2: boolean;
  insecureTls: boolean;
  privateNet: boolean;
  /** With a new address: send both unsafe options as shown, so the server keeps them even on another site. */
  keepGrants: boolean;
}

const fromDetail = (d: FeedDetail): Form => ({
  title: d.custom_title ?? "",
  url: d.url,
  folder: d.folder_id,
  interval: d.interval_minutes == null ? "" : String(d.interval_minutes),
  retention: d.retention == null ? "" : String(d.retention),
  autoRead: d.auto_read_days == null ? "" : String(d.auto_read_days),
  fulltext: d.fulltext,
  enabled: d.enabled,
  dedup: d.dedup_mode,
  userAgent: d.user_agent ?? "",
  auth: "",
  clearAuth: false,
  ignoreCache: d.ignore_http_cache,
  noHttp2: d.disable_http2,
  insecureTls: d.allow_insecure_tls,
  privateNet: d.allow_private_net,
  keepGrants: false,
});

/** Only what changed, in the shape PATCH /api/feeds/{id} takes. */
export function diffForm(d: FeedDetail, f: Form): Record<string, unknown> {
  const o = fromDetail(d);
  const out: Record<string, unknown> = {};
  if (f.title.trim() !== o.title) out.custom_title = f.title.trim() === "" ? null : f.title.trim();
  if (f.url.trim() !== o.url) out.url = f.url.trim();
  if (f.folder !== o.folder) out.folder_id = f.folder;
  if (f.interval !== o.interval) out.interval_minutes = f.interval === "" ? null : Number(f.interval);
  if (f.retention !== o.retention) out.retention = f.retention === "" ? null : Number(f.retention);
  if (f.autoRead !== o.autoRead) out.auto_read_days = f.autoRead === "" ? null : Number(f.autoRead);
  if (f.fulltext !== o.fulltext) out.fulltext = f.fulltext;
  if (f.enabled !== o.enabled) out.enabled = f.enabled;
  if (f.dedup !== o.dedup) out.dedup_mode = f.dedup;
  if (f.userAgent.trim() !== o.userAgent) out.user_agent = f.userAgent.trim() === "" ? null : f.userAgent.trim();
  if (f.clearAuth) out.http_auth = null;
  else if (f.auth.trim() !== "") out.http_auth = f.auth.trim();
  if (f.ignoreCache !== o.ignoreCache) out.ignore_http_cache = f.ignoreCache;
  if (f.noHttp2 !== o.noHttp2) out.disable_http2 = f.noHttp2;
  // The server keeps the unsafe options on a move within the feed's site and clears the ones it is not sent on
  // a move to another site. "Keep for the new address" sends both as shown, so they stay wherever it points.
  const resend = "url" in out && f.keepGrants;
  if (resend || f.insecureTls !== o.insecureTls) out.allow_insecure_tls = f.insecureTls;
  if (resend || f.privateNet !== o.privateNet) out.allow_private_net = f.privateNet;
  return out;
}

/** What the unsafe options on for this feed allow, for the new-address notice. */
export function grantNotice(f: Pick<Form, "privateNet" | "insecureTls">): string {
  const what = [f.privateNet ? "reach addresses on your own network" : "", f.insecureTls ? "accept an invalid security certificate" : ""].filter(Boolean).join(" and ");
  return `This feed can ${what}. If the new address is on another site, that is turned off unless you keep it.`;
}

/** Whether a save turned off an unsafe option the form still showed on (a new address on another site). */
export function grantsDropped(f: Pick<Form, "privateNet" | "insecureTls">, saved: Pick<FeedDetail, "allow_private_net" | "allow_insecure_tls">): boolean {
  return (f.privateNet && !saved.allow_private_net) || (f.insecureTls && !saved.allow_insecure_tls);
}

/** The confirmation after a new address, saying what the server dropped: the unsafe options (another site) or the saved login (another host). */
export function savedAddressMessage(
  before: Pick<FeedDetail, "has_http_auth">,
  held: Pick<Form, "privateNet" | "insecureTls">,
  saved: Pick<FeedDetail, "allow_private_net" | "allow_insecure_tls" | "has_http_auth">,
): string {
  const lost = [grantsDropped(held, saved) ? "its unsafe options were turned off" : "", before.has_http_auth && !saved.has_http_auth ? "its saved login was removed" : ""].filter(Boolean);
  return lost.length ? `Feed address updated. It is on another site or host, so ${lost.join(" and ")}. Kipple is fetching it now.` : "Feed address updated. Kipple is fetching it now.";
}

/** Turn a failed save into a message next to the field it belongs to. */
/** The preset numbers, plus the feed's own stored value when it is not one of them, in ascending order. */
export function withCustom(presets: readonly number[], custom: string): number[] {
  const n = Number(custom);
  return custom && !presets.includes(n) ? [...presets, n].sort((x, y) => x - y) : [...presets];
}

function saveError(e: unknown): { field: "url" | "auth" | "form"; message: string } {
  if (e instanceof ApiError) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    if (e.code === "invalid_url") return { field: "url", message: msg || "That address isn't a valid feed URL." };
    if (e.code === "url_exists") return { field: "url", message: "Another feed already uses that address." };
    if (e.code === "archive_feed") return { field: "form", message: "The archive feed can't be edited." };
    if (e.status === 400 && msg) return { field: msg.includes("http_auth") ? "auth" : "form", message: msg };
  }
  return { field: "form", message: errorMessage(e) };
}

export function FeedEditor({ feed, onClose }: { feed: Feed; onClose: () => void }) {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const q = useQuery({ queryKey: ["feed", feed.id], queryFn: () => loadFeedDetail(feed), staleTime: 0, gcTime: 0, retry: false });
  const [edits, setEdits] = useState<Partial<Form>>({});
  const [err, setErr] = useState<{ field: "url" | "auth" | "form"; message: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [alsoStarred, setAlsoStarred] = useState(false);
  const f: Form | null = q.data ? { ...fromDetail(q.data), ...edits } : null;

  const folders = boot.data?.folders ?? [];
  const patch = useMemo(() => (q.data ? diffForm(q.data, { ...fromDetail(q.data), ...edits }) : {}), [q.data, edits]);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setEdits((cur) => ({ ...cur, [k]: v }));
  const changedUrl = q.data && f && f.url.trim() !== q.data.url;
  // The unsafe options the feed has now and the form still keeps on: what a new address could lose. One
  // switched on in this edit is sent anyway (it changed), so it needs no notice.
  const heldGrants = { privateNet: !!(q.data?.allow_private_net && f?.privateNet), insecureTls: !!(q.data?.allow_insecure_tls && f?.insecureTls) };

  const save = async () => {
    if (!q.data || Object.keys(patch).length === 0) return onClose();
    setBusy(true);
    setErr(null);
    try {
      const saved = await patchFeed(feed.id, patch);
      invalidateFeeds(qc);
      // A login this edit replaced or removed itself is not news.
      const loginBefore = { has_http_auth: q.data.has_http_auth && !("http_auth" in patch) };
      toast(changedUrl ? savedAddressMessage(loginBefore, heldGrants, saved) : "Feed saved");
      onClose();
    } catch (e) {
      setErr(saveError(e));
    } finally {
      setBusy(false);
    }
  };

  const refresh = async () => {
    setBusy(true);
    try {
      const r = await refreshFeed(feed.id);
      invalidateFeeds(qc);
      if (r.pending) announce("Still fetching. Check the health page in a moment.");
      else if (r.error) toast(`Couldn't fetch this feed: ${r.error}`, "error");
      else toast(r.new_items ? `${r.new_items} new article${r.new_items === 1 ? "" : "s"}` : "No new articles");
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await deleteFeed(feed.id, alsoStarred);
      invalidateFeeds(qc);
      void qc.invalidateQueries({ queryKey: ["items"] });
      toast(`Deleted ${feed.title}`);
      setConfirmDelete(false);
      onClose();
    } catch (e) {
      toast(errorMessage(e), "error");
      setBusy(false);
    }
  };

  const layoutValue = dp.overrides.feed[feed.id] ?? "default";
  // The global switch overrides every feed's own one: say so instead of showing a switch that does nothing.
  const fulltextAll = boot.data?.settings?.["fetch.fulltext_all"] === true;

  if (confirmDelete) {
    return (
      <Modal
        open
        onOpenChange={(o) => !o && setConfirmDelete(false)}
        title={`Delete ${feed.title}?`}
        description="Its articles are removed from Kipple. Your sync apps stop seeing this feed."
        footer={
          <>
            <Button onClick={() => setConfirmDelete(false)}>Cancel</Button>
            <Button variant="solid" disabled={busy} onClick={() => void remove()}>
              Delete feed
            </Button>
          </>
        }
      >
        {feed.starred_count > 0 ? (
          <>
            <Notice tone="warn">
              This feed has {feed.starred_count} starred article{feed.starred_count === 1 ? "" : "s"}. By default they are kept in the Archive.
            </Notice>
            <Switch label="Delete starred articles too" checked={alsoStarred} onChange={setAlsoStarred} help="This can't be undone." />
          </>
        ) : (
          <p className="text-sm text-fg2">This feed has no starred articles.</p>
        )}
      </Modal>
    );
  }

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Edit feed"
      description={feed.title}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="solid" disabled={busy || !f} onClick={() => void save()}>
            {Object.keys(patch).length ? "Save" : "Done"}
          </Button>
        </>
      }
    >
      {q.isPending ? <Skeleton rows={4} label="Loading feed settings" /> : null}
      {q.isError ? <Notice tone="error">Couldn't load this feed's settings. {errorMessage(q.error)}</Notice> : null}
      {f && q.data ? (
        <>
          {err?.field === "form" ? <Notice tone="error">{err.message}</Notice> : null}
          <Field label="Title" help="Leave empty to use the title the feed gives itself.">
            {(a) => <input {...a} type="text" value={f.title} placeholder={q.data.title} onChange={(e) => set("title", e.target.value)} className={inputCls} />}
          </Field>
          <Field
            label="Feed address"
            help={q.data.url_original && q.data.url_original !== q.data.url ? `Originally ${q.data.url_original}. Change this to point at a new address; articles you already have are kept.` : "Change this to point at a new address; articles you already have are kept."}
            error={err?.field === "url" ? err.message : null}
          >
            {(a) => <input {...a} type="url" inputMode="url" autoCapitalize="none" spellCheck={false} value={f.url} onChange={(e) => set("url", e.target.value)} className={inputCls} />}
          </Field>
          {changedUrl && (heldGrants.privateNet || heldGrants.insecureTls) ? (
            <>
              <Notice tone="warn">{grantNotice(heldGrants)}</Notice>
              <Switch
                label="Keep for the new address"
                help="Only for an address you trust, such as another server in your home."
                checked={f.keepGrants}
                onChange={(v) => set("keepGrants", v)}
              />
            </>
          ) : null}
          <Field label="Folder">
            {(a) => (
              <select {...a} value={f.folder} onChange={(e) => set("folder", e.target.value)} className={inputCls}>
                {folders.map((fo) => (
                  <option key={fo.id} value={fo.id}>
                    {fo.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <Field label="Layout on this device" help="Overrides the device default for this feed only.">
            {(a) => (
              <select
                {...a}
                value={layoutValue}
                onChange={(e) => setLayoutOverride("feed", feed.id, e.target.value === "default" ? null : (e.target.value as LayoutId))}
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
          <Field label="Check for new articles">
            {(a) => (
              <select {...a} value={f.interval} onChange={(e) => set("interval", e.target.value)} className={inputCls}>
                <option value="">Use the default in Settings</option>
                {withCustom(INTERVALS, f.interval).map((m) => (
                  <option key={m} value={m}>
                    Every {intervalLabel(m)}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <Field label="Articles to keep" help="Starred articles are always kept.">
            {(a) => (
              <select {...a} value={f.retention} onChange={(e) => set("retention", e.target.value)} className={inputCls}>
                <option value="">Use the default in Settings</option>
                {RETENTIONS.map((n) => (
                  <option key={n} value={n}>
                    {retentionLabel(n)}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <Field
            label="Mark as read after"
            help="Unread articles are marked read once they are this old. Starred and muted articles are never touched. Changing this never marks anything at once."
          >
            {(a) => (
              <select {...a} value={f.autoRead} onChange={(e) => set("autoRead", e.target.value)} className={inputCls}>
                <option value="">Use the global setting</option>
                {withCustom(AUTO_READ_PRESETS, f.autoRead).map((n) => (
                  <option key={n} value={n}>
                    {n === 0 ? "Off for this feed" : autoReadLabel(n)}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <AutoReadCatchUp
            feedId={feed.id}
            days={f.autoRead !== fromDetail(q.data).autoRead && f.autoRead !== "" ? Number(f.autoRead) : undefined}
            blockedReason={f.autoRead === "" && q.data.auto_read_days != null ? "Save this change to preview it." : undefined}
            runBlocked={f.autoRead !== fromDetail(q.data).autoRead ? "Save the feed, then open it again to mark these now." : undefined}
          />
          {fulltextAll ? (
            <Switch
              label="Fetch full article text"
              help="On for all feeds. Settings > Sync & Feeds > Library fetches the full article for every feed, so this switch has no effect until that is turned off."
              checked
              disabled
              onChange={() => undefined}
            />
          ) : (
            <Switch label="Fetch full article text" help="Loads the whole article from the website when the feed only has a summary." checked={f.fulltext} onChange={(v) => set("fulltext", v)} />
          )}
          <Switch label="Enabled" help="Turn off to stop checking this feed without deleting it." checked={f.enabled} onChange={(v) => set("enabled", v)} />

          <Disclosure label="Advanced">
            <Field label="Duplicate detection" help="How Kipple decides that two entries are the same article.">
              {(a) => (
                <select {...a} value={f.dedup} onChange={(e) => set("dedup", e.target.value as Form["dedup"])} className={inputCls}>
                  <option value="auto">Automatic</option>
                  <option value="link">Same link</option>
                  <option value="link_title">Same link and title</option>
                </select>
              )}
            </Field>
            {q.data.rekey_pending ? <Notice>Duplicate detection is being re-applied to this feed.</Notice> : null}
            <Field label="User agent" help="Only for feeds that refuse to load. Leave empty for the default.">
              {(a) => <input {...a} type="text" value={f.userAgent} onChange={(e) => set("userAgent", e.target.value)} className={inputCls} />}
            </Field>
            <Field
              label="Login for this feed"
              help={q.data.has_http_auth ? "A login is saved. Type a new one as user:password to replace it." : "Only for private feeds. Type it as user:password."}
              error={err?.field === "auth" ? err.message : null}
            >
              {(a) => <input {...a} type="password" autoComplete="off" value={f.auth} disabled={f.clearAuth} placeholder={q.data?.has_http_auth ? "Saved" : ""} onChange={(e) => set("auth", e.target.value)} className={inputCls} />}
            </Field>
            {q.data.has_http_auth ? <Switch label="Remove the saved login" checked={f.clearAuth} onChange={(v) => set("clearAuth", v)} /> : null}
            <Switch label="Ignore HTTP caching" help="Download the whole feed every time, even when the site says nothing changed." checked={f.ignoreCache} onChange={(v) => set("ignoreCache", v)} />
            <Switch label="Don't use HTTP/2" help="Try this if a feed fails with connection or stream errors." checked={f.noHttp2} onChange={(v) => set("noHttp2", v)} />
          </Disclosure>

          <Disclosure label="Unsafe options" tone="warn">
            <Notice tone="warn">These weaken protections that keep Kipple safe. Turn them on only for a feed you trust.</Notice>
            <Switch
              label="Allow an invalid security certificate"
              help="The connection is still encrypted, but Kipple can't tell whether it is talking to the real site."
              checked={f.insecureTls}
              onChange={(v) => set("insecureTls", v)}
            />
            <Switch
              label="Allow addresses on my own network"
              help="Lets this feed point at a private or local address, such as a server in your home. Other feeds can't."
              checked={f.privateNet}
              onChange={(v) => set("privateNet", v)}
            />
          </Disclosure>

          <div className="flex flex-wrap gap-2">
            <Button disabled={busy} onClick={() => void refresh()}>
              Refresh now
            </Button>
            <Button disabled={busy} onClick={() => setConfirmDelete(true)}>
              Delete feed
            </Button>
          </div>
        </>
      ) : null}
    </Modal>
  );
}
