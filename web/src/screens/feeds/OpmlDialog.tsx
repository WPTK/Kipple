import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { importOpml, invalidateFeeds, patchFeed, type OpmlResult, type PrivateFeed } from "@/api/admin";
import { liveStore, seedRun } from "@/api/events";
import { useStoreSelector } from "@/lib/store";
import { Button } from "@/ui/button";
import { Checkbox, Field, Modal, Notice, inputCls } from "@/ui/kit";
import { announce } from "@/shell/toasts";
import { PATH_SEP } from "@/lib/folderTree";

/** The most the server accepts for an OPML upload (maxOPMLBody in internal/api/opml.go). */
export const OPML_MAX_BYTES = 8 << 20;

export function opmlError(e: unknown): string {
  if (e instanceof ApiError) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    if (e.status === 413) return "That file is too large to import.";
    if ((e.code === "empty_opml" || e.code === "opml_entity") && msg) return `Kipple can't import that file: ${msg}.`;
    if (e.code === "not_opml") return "That is not an OPML file. Choose the .opml or .xml file exported from your other reader.";
    if (e.code === "bad_opml") return "That OPML file is damaged or cut short, so Kipple couldn't read it. Export it again from your other reader.";
    if (e.status === 400 || e.status === 422) return msg ? `Kipple couldn't read that file: ${msg}` : "Kipple couldn't read that file. Choose an OPML file exported from another reader.";
  }
  return errorMessage(e);
}

/**
 * iOS greys out files whose type it cannot map from `accept` (an `.opml` export often has no registered
 * type), so the picker accepts everything and the file is checked here instead: by extension, else by
 * whether it opens like XML. Returns an error message, or null when it looks like OPML.
 */
export async function opmlFileProblem(file: File): Promise<string | null> {
  // The server takes at most 8 MiB (internal/api/opml.go): say so now rather than after a long upload.
  if (file.size > OPML_MAX_BYTES) return "That file is too large to import (the limit is 8 MB). Check that it is the OPML export and not something else.";
  if (/\.(opml|xml)$/i.test(file.name)) return null;
  let head = "";
  try {
    head = await file.slice(0, 512).text();
  } catch {
    /* unreadable: fall through to the message */
  }
  if (/^[\s\uFEFF]*<(\?xml|opml)/i.test(head)) return null;
  return "That doesn't look like an OPML file. Choose the .opml or .xml file exported from your other reader.";
}

/**
 * A file with more feeds than this brings thousands of articles into Unread at once (a feed's first fetch is 5 to 50), so
 * the form offers the 7-day setting. The article count is not known before the fetch; the feeds in the file stand for it.
 */
export const FRESH_MIN_FEEDS = 20;
/** The days "Start fresh" fills in. The field itself stays empty until the person presses it. */
export const FRESH_DAYS = 7;

const isSpace = (c: string | undefined): boolean => c === " " || c === "\t" || c === "\n" || c === "\r";

/**
 * Reads one `<outline` start tag from just after its name to its closing `>` (quoted values may hold `>`), and says whether
 * it has a non-empty `xmlUrl` attribute. Returns where reading stopped. Each character is looked at a bounded number of times.
 */
function readOutlineTag(text: string, from: number): { next: number; feed: boolean } {
  const n = text.length;
  let i = from;
  let feed = false;
  while (i < n) {
    const c = text[i];
    if (isSpace(c) || c === "/") {
      i++;
      continue;
    }
    if (c === ">") return { next: i + 1, feed };
    const start = i;
    while (i < n && !isSpace(text[i]) && !"=>/\"'".includes(text[i] ?? "")) i++;
    const name = text.slice(start, i);
    while (i < n && isSpace(text[i])) i++;
    if (text[i] !== "=") {
      if (i === start) i++; // a stray quote: step over it
      continue;
    }
    i++;
    while (i < n && isSpace(text[i])) i++;
    const q = text[i];
    if (q === '"' || q === "'") {
      const close = text.indexOf(q, i + 1);
      if (close < 0) return { next: n, feed };
      if (name === "xmlUrl" && text.slice(i + 1, close).trim() !== "") feed = true;
      i = close + 1;
    } else {
      while (i < n && !isSpace(text[i]) && text[i] !== ">") i++; // an unquoted value is not valid XML: skip it
    }
  }
  return { next: n, feed };
}

/** Where a comment, CDATA section, declaration or processing instruction at `lt` ends (the end of the text when unterminated); null for anything else. */
function skipMarkup(text: string, lt: number): number | null {
  const [open, close]: [number, string] = text.startsWith("<!--", lt) ? [4, "-->"] : text.startsWith("<![CDATA[", lt) ? [9, "]]>"] : text[lt + 1] === "?" || text[lt + 1] === "!" ? [2, ">"] : [0, ""];
  if (!open) return null;
  const end = text.indexOf(close, lt + open);
  return end < 0 ? text.length : end + close.length;
}

/**
 * How many feeds an OPML text lists: its outline start tags with a non-empty `xmlUrl` attribute (a repeat counts twice).
 * One pass over the text, without backtracking: comments, CDATA, declarations and processing instructions are jumped
 * over, and a tag's attributes are read one by one, so a quoted value that holds the words `xmlUrl=` is never taken
 * for the attribute. An unterminated comment or tag ends the count at what came before it.
 */
export function countOpmlFeeds(text: string): number {
  let n = 0;
  let i = 0;
  while (i < text.length) {
    const lt = text.indexOf("<", i);
    if (lt < 0) break;
    const past = skipMarkup(text, lt);
    if (past !== null) {
      i = past;
      continue;
    }
    const after = text[lt + 8];
    if (text.startsWith("<outline", lt) && (after === undefined || isSpace(after) || after === "/" || after === ">")) {
      const tag = readOutlineTag(text, lt + 8);
      if (tag.feed) n++;
      i = tag.next;
      continue;
    }
    i = lt + 1;
  }
  return n;
}

/** "<url>: kipple:allow_insecure_tls" from the import report: the address, and what was not applied, in plain words. */
export function attrNote(s: string): { url: string; note: string } {
  const at = s.lastIndexOf(": ");
  const url = at >= 0 ? s.slice(0, at) : s;
  const attr = at >= 0 ? s.slice(at + 2) : "";
  if (attr.endsWith("allow_private_net")) return { url, note: "the file asked to reach your own network; Kipple didn't allow it. Allow it in the feed's settings if you trust it" };
  if (attr === "private_address") return { url, note: "this address can reach services on the machine itself or its network, so Kipple won't fetch it unless you allow it in the feed's settings" };
  if (attr.endsWith("allow_insecure_tls")) return { url, note: "the file skipped certificate checks; Kipple checks them" };
  return { url, note: `${attr || "a setting"} was not applied` };
}

/**
 * How the fetch of the new feeds is going: a bar while the import run goes, then how it ended. The run is shown from
 * the import's answer at once, so this works while the live stream is down; without either it says the fetch started.
 */
function ImportProgress({ runId, total, eventId }: { runId: string; total: number; eventId?: string }) {
  useEffect(() => seedRun({ id: runId, kind: "import", done: 0, total, new_items: 0, errors: 0 }, eventId), [runId, total, eventId]);
  const run = useStoreSelector(liveStore, (s) => s.runs[runId]);
  const finished = useStoreSelector(liveStore, (s) => s.finished?.[runId]);
  // A run that was on screen and is gone without a result ended while the stream was down (the status poll dropped it).
  const [seen, setSeen] = useState(false);
  if (run && !seen) setSeen(true);
  if (run) {
    return (
      <div className="flex flex-col gap-1 text-sm text-fg2">
        <p>
          Fetching the new feeds: {run.done.toLocaleString()} of {run.total.toLocaleString()}
        </p>
        <progress className="h-2 w-full accent-[var(--kp-accent)]" value={run.done} max={Math.max(1, run.total)} aria-label="Fetching the new feeds" />
      </div>
    );
  }
  // A run a status poll stopped listing, with no result heard: it is over, but how it went is not known.
  if (finished && !finished.inferred) {
    return (
      <p className="text-sm text-fg2">
        {finished.errors > 0
          ? `Kipple fetched the new feeds. ${finished.errors.toLocaleString()} couldn't be fetched; each says why on its row in Feeds.`
          : "Kipple fetched the new feeds."}
      </p>
    );
  }
  if (finished || seen) return <p className="text-sm text-fg2">The fetch of the new feeds has ended. A feed that can't be fetched says why on its row in Feeds.</p>;
  return <p className="text-sm text-fg2">Kipple is fetching the new feeds now. A feed that can't be fetched says why on its row in Feeds.</p>;
}

/**
 * The feeds on a private address or a name only a local network resolves. An import never grants the exception (a
 * file is not you), so they are listed with the one thing that turns it on for all of them. Each grant names the
 * address this list showed, so a feed whose address has changed since is left alone. A feed on a public name that
 * only the file asked about is not here: it gets its own line under "Some settings in the file were not applied".
 */
function PrivateFeeds({ feeds, where }: { feeds: PrivateFeed[]; where: string }) {
  const qc = useQueryClient();
  const [left, setLeft] = useState(feeds); // not tried yet, or stopped by a session or network error: worth another go
  const [allowed, setAllowed] = useState<PrivateFeed[]>([]);
  const [refused, setRefused] = useState<string[]>([]); // gone or changed: another go would say the same
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(0);
  const [error, setError] = useState("");
  const n = feeds.length;
  const run = async () => {
    setBusy(true);
    setError("");
    setDone(0);
    const rest: PrivateFeed[] = [];
    const ok: PrivateFeed[] = [];
    const no: string[] = [];
    for (const [i, f] of left.entries()) {
      try {
        await patchFeed(String(f.feed_id), { allow_private_net: true, expected_url: f.url });
        ok.push(f);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) no.push(`${f.url} is gone`);
        else if (e instanceof ApiError && e.status === 409) no.push(e.code === "url_changed" ? `${f.url} now has another address` : `${f.url} could not be changed`);
        else {
          // The session or the network would fail every feed after this one too, so the rest wait for another go.
          setError(errorMessage(e));
          rest.push(...left.slice(i));
          break;
        }
      }
      setDone((d) => d + 1);
    }
    setLeft(rest);
    setAllowed((a) => [...a, ...ok]);
    setRefused((r) => [...r, ...no]);
    setBusy(false);
    invalidateFeeds(qc);
    announce(rest.length || no.length ? "Some feeds could not be changed" : "Allowed addresses on your own network");
  };
  const off = allowed.filter((f) => !f.enabled).length;
  const fetching = allowed.length - off;
  return (
    <div className="text-sm" data-testid="private-feeds">
      <p className="font-semibold">
        {n} feed{n === 1 ? " is" : "s are"} on your own network
      </p>
      <p className="text-fg2">For safety, an import never lets a file reach addresses on your own network, so Kipple won't fetch {n === 1 ? "it" : "these"} until you allow it.</p>
      <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
        {feeds.map((f) => (
          <li key={f.feed_id} className="break-all">
            {f.url}
          </li>
        ))}
      </ul>
      <div className="mt-2 flex flex-col gap-1">
        {left.length === 0 ? (
          allowed.length ? (
            <p role="status">
              Allowed for {allowed.length === n ? (n === 1 ? "the feed" : `all ${n} feeds`) : `${allowed.length} of ${n} feeds`}.
              {fetching ? ` Kipple is fetching ${fetching === 1 ? "it" : fetching === allowed.length ? "them" : `${fetching} of them`} now.` : ""}
              {off ? ` ${off === 1 ? "One is" : `${off} are`} turned off, so Kipple fetches ${off === 1 ? "it" : "them"} once you turn ${off === 1 ? "it" : "them"} on.` : ""}
            </p>
          ) : null
        ) : (
          <Button onClick={() => void run()} disabled={busy} className="self-start">
            {busy ? `Allowing ${done.toLocaleString()} of ${left.length.toLocaleString()}` : left.length < n ? "Try the rest again" : `Allow my own network for ${n === 1 ? "this feed" : `these ${n} feeds`}`}
          </Button>
        )}
        {error ? <Notice tone="error">{error}</Notice> : null}
        {refused.length ? <Notice tone="error">Not changed: {refused.join("; ")}. {where}</Notice> : null}
      </div>
    </div>
  );
}

/** What an import did, in plain words: the counts, and everything that was skipped, merged or not applied. */
export function OpmlResultSummary({ result, inWizard = false }: { result: OpmlResult; inWizard?: boolean }) {
  const existing = result.feeds_existing.length;
  const moved = result.feeds_moved?.length ?? 0;
  const emptied = result.folders_emptied ?? [];
  const refused = result.folders_refused ?? [];
  const refusedMore = result.folders_refused_more ?? 0;
  const mergedCaseMore = result.folders_merged_case_more ?? 0;
  const mergedPath = result.folders_merged_path ?? [];
  const dropped = result.memberships_dropped.length + (result.memberships_dropped_more ?? 0);
  const skipped = result.skipped ?? [];
  const invalid = result.invalid_attrs ?? [];
  const ignored = result.ignored_attrs ?? [];
  const priv = result.feeds_private ?? [];
  return (
    <>
    <ul className="flex list-disc flex-col gap-1 pl-5 text-sm">
      <li>
        {result.feeds_added} feed{result.feeds_added === 1 ? "" : "s"} added
      </li>
      <li>
        {result.folders_created} folder{result.folders_created === 1 ? "" : "s"} created
      </li>
      {result.feeds_repeated ? (
        <li>
          {result.feeds_repeated === 1
            ? "1 feed was listed again under the same address; the repeat was left out"
            : `${result.feeds_repeated} feeds were listed again under the same address; the repeats were left out`}
        </li>
      ) : null}
      {existing - moved > 0 ? (
        <li>
          {existing - moved === 1 ? "1 feed was already in Kipple and was left as it is" : `${existing - moved} feeds were already in Kipple and were left as they are`}
        </li>
      ) : null}
      {moved ? <li>{moved === 1 ? "1 feed you already had was moved into the file's folder" : `${moved} feeds you already had were moved into the file's folders`}</li> : null}
      {emptied.length ? <li>Folders with no feeds are kept: {emptied.join(", ")}</li> : null}
      {dropped ? (
        <li>
          {dropped === 1
            ? "1 feed was listed in more than one folder. It stays in the first."
            : `${dropped} feeds were listed in more than one folder. Each stays in the first.`}
        </li>
      ) : null}
      {result.folders_merged_case.length ? <li>Folders that differed only by capital letters were merged: {result.folders_merged_case.map((m) => `${m.merged} into ${m.kept}`).join(", ")}{mergedCaseMore ? ` and ${mergedCaseMore} more` : ""}</li> : null}
    </ul>
    {mergedPath.length ? (
      <div className="text-sm">
        <p className="font-semibold">Some folders of the file match a folder you have with the same full path</p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {mergedPath.map((m, i) => (
            <li key={i} className="break-all">
              The feeds of {m.merged.join(PATH_SEP)} were filed into {m.kept.join(PATH_SEP)}
            </li>
          ))}
        </ul>
      </div>
    ) : null}
    {refused.length ? (
      <div className="text-sm">
        <p className="font-semibold">
          {refused.length + refusedMore} folder{refused.length + refusedMore === 1 ? " was" : "s were"} not created
        </p>
        <p className="text-fg2">Their feeds went into the nearest folder above that could be made.</p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {refused.map((r, i) => (
            <li key={i} className="break-all">
              {r.path}: {r.reason}
            </li>
          ))}
          {refusedMore ? <li>and {refusedMore} more</li> : null}
        </ul>
      </div>
    ) : null}
    {skipped.length ? (
      <div className="text-sm">
        <p className="font-semibold">
          {skipped.length} feed{skipped.length === 1 ? " was" : "s were"} skipped
        </p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {skipped.map((s, i) => (
            <li key={i} className="break-words">
              <span className="break-all">{s.url || "(no address)"}</span>: {s.reason}
            </li>
          ))}
        </ul>
      </div>
    ) : null}
    {priv.length ? <PrivateFeeds feeds={priv} where={inWizard ? "You can allow a feed in its settings in Feeds once setup is finished." : "Open a feed in Feeds to change it there."} /> : null}
    {ignored.length ? (
      <div className="text-sm">
        <p className="font-semibold">Some settings in the file were not applied</p>
        <p className="text-fg2">For safety, an import never lets a file skip certificate checks or reach your own network.</p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {ignored.map((s, i) => {
            const { url, note } = attrNote(s);
            return (
              <li key={i} className="break-words">
                <span className="break-all">{url}</span>: {note}
              </li>
            );
          })}
        </ul>
      </div>
    ) : null}
    {invalid.length ? (
      <div className="text-sm">
        <p className="font-semibold">Some settings in the file had values Kipple could not use</p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {invalid.map((s, i) => (
            <li key={i} className="break-all">
              {s}
            </li>
          ))}
        </ul>
      </div>
    ) : null}
    {result.run_id ? <ImportProgress runId={result.run_id} total={result.feeds_added} eventId={result.event_id} /> : null}
    </>
  );
}

/** Import an OPML file: file picker, the mark-older-as-read option, then the result summary. */
export function OpmlImportDialog({ onClose, inWizard = false }: { onClose: () => void; inWizard?: boolean }) {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const pick = useRef(0); // which selection an async step belongs to: a slower earlier file never overwrites a newer one
  const [file, setFile] = useState<File | null>(null);
  const [days, setDays] = useState("");
  const [bigFile, setBigFile] = useState(false);
  const [moveExisting, setMoveExisting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<OpmlResult | null>(null);

  const daysNum = days.trim() === "" ? undefined : Number(days);
  const daysBad = daysNum !== undefined && (!Number.isInteger(daysNum) || daysNum < 1 || daysNum > 365);

  const run = async () => {
    if (!file || daysBad) return;
    setBusy(true);
    setError(null);
    try {
      const r = await importOpml(file, { markReadOlderThanDays: daysNum, moveExisting });
      setResult(r);
      invalidateFeeds(qc);
      announce(`Imported ${r.feeds_added} feed${r.feeds_added === 1 ? "" : "s"}`);
    } catch (e) {
      setError(opmlError(e));
    } finally {
      setBusy(false);
    }
  };

  if (result) {
    return (
      <Modal open onOpenChange={(o) => !o && onClose()} title="Import finished" footer={<Button variant="solid" onClick={onClose}>Done</Button>}>
        <OpmlResultSummary result={result} inWizard={inWizard} />
      </Modal>
    );
  }

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Import feeds from a file (OPML)"
      description="Feeds you already have are left alone."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="solid" disabled={!file || busy || daysBad} onClick={() => void run()}>
            {busy ? "Importing" : "Import"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <Field label="OPML file">
        {(a) => (
          <input
            {...a}
            ref={input}
            type="file"
            accept=".opml,.xml,text/xml,application/xml,text/x-opml,*/*"
            onChange={(e) => {
              const f = e.target.files?.[0] ?? null;
              const mine = ++pick.current;
              setResult(null);
              setError(null);
              setFile(null);
              setBigFile(false);
              if (!f) return;
              void opmlFileProblem(f).then(async (problem) => {
                if (mine !== pick.current) return;
                if (problem) return setError(problem);
                setFile(f);
                try {
                  const count = countOpmlFeeds(await f.text());
                  if (mine === pick.current) setBigFile(count > FRESH_MIN_FEEDS);
                } catch {
                  /* unreadable: the import reports it; the suggestion is just not offered */
                }
              });
            }}
            className="block min-h-11 w-full text-sm text-fg file:mr-3 file:min-h-11 file:cursor-pointer file:rounded-lg file:border file:border-line file:bg-surface file:px-3 file:font-semibold file:text-fg hover:file:bg-selection"
          />
        )}
      </Field>
      {bigFile ? (
        <Button
          className="self-start"
          aria-disabled={busy}
          onClick={() => {
            setDays(String(FRESH_DAYS));
            announce(`Older than ${FRESH_DAYS} days will arrive read`);
          }}
        >
          Start fresh: mark everything older than {FRESH_DAYS} days read
        </Button>
      ) : null}
      <Field
        label="Mark older articles as read (optional)"
        help="Articles older than this many days arrive already read."
        error={daysBad ? "Enter a whole number from 1 to 365, or leave empty." : null}
      >
        {(a) => <input {...a} inputMode="numeric" type="text" value={days} onChange={(e) => setDays(e.target.value)} className={inputCls} />}
      </Field>
      <Checkbox
        label="Move feeds that already exist into the file's folders"
        help="Left unticked, they stay where they are. Folders with no feeds are kept."
        checked={moveExisting}
        onChange={setMoveExisting}
      />
    </Modal>
  );
}
