import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { importOpml, invalidateFeeds, type OpmlResult } from "@/api/admin";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { announce } from "@/shell/toasts";
import { PATH_SEP } from "@/lib/folderTree";

/** The most the server accepts for an OPML upload (maxOPMLBody in internal/api/opml.go). */
export const OPML_MAX_BYTES = 8 << 20;

export function opmlError(e: unknown): string {
  if (e instanceof ApiError) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    if (e.status === 413) return "That file is too large to import.";
    if (e.code === "empty_opml") return "That OPML file has no feeds in it.";
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

/** "<url>: kipple:allow_private_net" or "<url>: private_address" from the import report, in plain words. */
export function attrNote(s: string): string {
  const at = s.lastIndexOf(": ");
  const url = at >= 0 ? s.slice(0, at) : s;
  const attr = at >= 0 ? s.slice(at + 2) : "";
  if (attr === "private_address") return `${url}: this feed is on a private-network address, so it was imported with private-network access off. Turn it on for the feed to fetch it`;
  const what = attr.endsWith("allow_private_net") ? "allowing private-network addresses" : attr.endsWith("allow_insecure_tls") ? "skipping certificate checks" : attr || "a setting";
  return `${url}: ${what} was ignored`;
}

/** What an import did, in plain words: the counts, and everything that was skipped, merged or not applied. */
export function OpmlResultSummary({ result }: { result: OpmlResult }) {
  const existing = result.feeds_existing.length;
  const moved = result.feeds_moved?.length ?? 0;
  const emptied = result.folders_emptied ?? [];
  const refused = result.folders_refused ?? [];
  const mergedPath = result.folders_merged_path ?? [];
  const dropped = result.memberships_dropped.length;
  const skipped = result.skipped ?? [];
  const invalid = result.invalid_attrs ?? [];
  const ignored = result.ignored_attrs ?? [];
  return (
    <>
    <ul className="flex list-disc flex-col gap-1 pl-5 text-sm">
      <li>
        {result.feeds_added} feed{result.feeds_added === 1 ? "" : "s"} added
      </li>
      <li>
        {result.folders_created} folder{result.folders_created === 1 ? "" : "s"} created
      </li>
      {existing - moved > 0 ? (
        <li>
          {existing - moved === 1 ? "1 feed was already in Kipple and was left as it is" : `${existing - moved} feeds were already in Kipple and were left as they are`}
        </li>
      ) : null}
      {moved ? <li>{moved === 1 ? "1 feed you already had was moved into the file's folder" : `${moved} feeds you already had were moved into the file's folders`}</li> : null}
      {emptied.length ? <li>Folders now empty, kept: {emptied.join(", ")}</li> : null}
      {dropped ? (
        <li>
          {dropped === 1
            ? "1 feed was listed in more than one folder. It stays in the first."
            : `${dropped} feeds were listed in more than one folder. Each stays in the first.`}
        </li>
      ) : null}
      {result.folders_merged_case.length ? <li>Folders that differed only by capital letters were merged: {result.folders_merged_case.map((m) => `${m.merged} into ${m.kept}`).join(", ")}</li> : null}
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
          {refused.length} folder{refused.length === 1 ? " was" : "s were"} not created
        </p>
        <p className="text-fg2">Their feeds went into the nearest folder above that could be made.</p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {refused.map((r, i) => (
            <li key={i} className="break-all">
              {r.path}: {r.reason}
            </li>
          ))}
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
            <li key={i} className="break-all">
              {s.url || "(no address)"}: {s.reason}
            </li>
          ))}
        </ul>
      </div>
    ) : null}
    {ignored.length ? (
      <div className="text-sm">
        <p className="font-semibold">Some settings in the file were not applied</p>
        <p className="text-fg2">For safety, an import never lets a file allow private-network addresses or skip certificate checks. Set those on the feed itself if you need them.</p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-fg2">
          {ignored.map((s, i) => (
            <li key={i} className="break-all">
              {attrNote(s)}
            </li>
          ))}
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
    {result.run_id ? <p className="text-sm text-fg2">Kipple is fetching the new feeds now.</p> : null}
    </>
  );
}

/** Import an OPML file: file picker, the mark-older-as-read option, then the result summary. */
export function OpmlImportDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [days, setDays] = useState("");
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
        <OpmlResultSummary result={result} />
      </Modal>
    );
  }

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Import OPML"
      description="OPML is the file most feed readers export. Feeds you already have are left alone."
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
              setResult(null);
              setError(null);
              setFile(null);
              if (!f) return;
              void opmlFileProblem(f).then((problem) => {
                if (problem) setError(problem);
                else setFile(f);
              });
            }}
            className={`${inputCls} py-2`}
          />
        )}
      </Field>
      <Field
        label="Mark older articles as read (optional)"
        help="Enter a number of days. Articles older than that arrive already read, so a big import doesn't flood Unread."
        error={daysBad ? "Enter a whole number from 1 to 365, or leave empty." : null}
      >
        {(a) => <input {...a} inputMode="numeric" type="text" value={days} onChange={(e) => setDays(e.target.value)} placeholder="For example 7" className={inputCls} />}
      </Field>
      <Switch
        label="Move feeds that already exist into the file's folders"
        help="Off: feeds you already have stay where they are. On: they move to the folder the file puts them in. Folders left empty are kept."
        checked={moveExisting}
        onChange={setMoveExisting}
      />
    </Modal>
  );
}
