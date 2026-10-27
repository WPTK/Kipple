import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { useBootstrap } from "@/api/queries";
import { addFeed, invalidateFeeds, type AddFeedResult, type Candidate, type FeedDetail, type FetchOutcome } from "@/api/admin";
import { Button } from "@/ui/button";
import { Checkbox, Disclosure, Field, Modal, Notice, RadioCard, inputCls } from "@/ui/kit";
import { failureReason } from "@/lib/feedStatus";
import { FolderSelect } from "@/ui/FolderSelect";
import { announce } from "@/shell/toasts";

type Step =
  | { kind: "form" }
  // from: the address entered, sent back with the pick (the server scopes the private-network option to its site).
  | { kind: "choose"; candidates: Candidate[]; from: string }
  | { kind: "done"; feed: FeedDetail; existed: boolean; fetch?: FetchOutcome };

/** Fallbacks for the add errors the server explains in its own message (it knows the cause). */
const addFallback: Record<string, string> = {
  invalid_url: "That isn't a web address. Enter a feed or site address such as https://example.com/feed.",
  no_feed: "Kipple found a web page at that address, but the page doesn't link to a feed.",
  not_feed: "That address doesn't answer with a feed or a web page.",
  unreachable: "Kipple couldn't reach that site. Check the address, or try again later.",
  timeout: "The site took too long to answer. Try again later.",
  feed_exists: "You already have this feed. The address you entered leads to it.",
  feed_gone: "That feed was removed while it was being added, most likely because you already have it, in which case a title or folder you chose (other than the default folder) was applied to the feed you have.",
  private_address: "That address is on your own network, which Kipple doesn't fetch from unless you allow it for the feed.",
};

/** A server reason as a sentence: capitalized, ending in a full stop. */
export const sentence = (s: string) => {
  const t = s.trim();
  return t ? t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? "" : ".") : t;
};

/** Plain-English message for a failed add. */
export function addError(e: unknown): string {
  const fallback = e instanceof ApiError ? addFallback[e.code] : undefined;
  if (e instanceof ApiError && fallback) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    return msg ? sentence(msg) : fallback;
  }
  return errorMessage(e);
}

/** The host of what was typed, with or without a scheme ("example.com/feed" is "example.com"); "" when it has none. */
export function hostOf(typed: string): string {
  const t = typed.trim();
  try {
    return new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(t) ? t : `https://${t}`).hostname;
  } catch {
    return "";
  }
}

/** Whether a typed address points at the reader's own network (a loopback, private-range or local name), which Kipple fetches only when allowed for the feed. */
export function looksPrivate(typed: string): boolean {
  const h = hostOf(typed).replace(/^\[|\]$/g, "");
  if (!h) return false;
  if (h === "localhost" || h === "::1" || /\.(local|lan|home\.arpa|internal)$/.test(h)) return true;
  const v4 = /^(\d{1,3})\.(\d{1,3})\.\d{1,3}\.\d{1,3}$/.exec(h);
  if (!v4) return /^f[cd][0-9a-f]{2}:/i.test(h) || /^fe80:/i.test(h);
  const [a, b] = [Number(v4[1]), Number(v4[2])];
  return a === 127 || a === 10 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31) || (a === 169 && b === 254);
}

/** The longest title the server accepts, in characters. */
const MAX_TITLE_CHARS = 200;

/** Add a feed by address: exists, choose-among-candidates and ok flows, then the first-fetch result. */
export function AddFeedDialog({ onClose, onOpenFeed }: { onClose: () => void; onOpenFeed?: (feedId: string) => void }) {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");
  const [folder, setFolder] = useState("");
  const [step, setStep] = useState<Step>({ kind: "form" });
  const [pick, setPick] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Offered as soon as the address looks like one on the reader's own network, or once the server refused one; sent only when turned on.
  const [refusedPrivate, setRefusedPrivate] = useState(false);
  const [privateNet, setPrivateNet] = useState(false);
  // Options opens on request. A title can only be typed inside it, so while the title is too long it is open, and a click to
  // close it is ignored: the reason Add is off is never behind a closed section, and shortening the title later does not fold
  // the section away under the focused field.
  const [optionsOpen, setOptionsOpen] = useState(false);
  const privateOffered = refusedPrivate || looksPrivate(url);
  // The server counts characters (code points), so an emoji is one, not the two UTF-16 units maxLength would count.
  const titleChars = [...title.trim()].length;
  const titleTooLong = titleChars > MAX_TITLE_CHARS;

  const address = useRef<HTMLInputElement>(null);
  const choices = useRef<HTMLFieldSetElement>(null);
  const run = async (target: string, discoveredFrom?: string) => {
    setBusy(true);
    setError(null);
    try {
      const r: AddFeedResult = await addFeed({
        url: target.trim(),
        ...(title.trim() ? { title: title.trim() } : {}),
        ...(folder ? { folder_id: folder } : {}),
        ...(privateNet ? { allow_private_net: true } : {}),
        ...(discoveredFrom ? { discovered_from: discoveredFrom } : {}),
      });
      invalidateFeeds(qc);
      if (r.status === "choose") {
        // A pick that is itself a page with several feeds keeps the address first entered: the option is for
        // that site, never for one a page chose. The server cannot verify discovered_from (it keeps no state
        // between requests), so this is the client's part of the rule.
        setStep({ kind: "choose", candidates: r.candidates, from: discoveredFrom ?? target.trim() });
        setPick(r.candidates[0]?.url ?? "");
      } else {
        setStep({ kind: "done", feed: r.feed, existed: r.status === "exists", fetch: r.status === "ok" ? r.fetch : undefined });
        announce(r.status === "exists" ? "You already have this feed" : `Added ${r.feed.title}`);
      }
    } catch (e) {
      setError(addError(e));
      if (e instanceof ApiError && e.code === "private_address") setRefusedPrivate(true);
      // Focus goes back where the keyboard was (the busy button let it go): the address field, whose error it is, or the
      // feed that was picked.
      (address.current ?? choices.current?.querySelector<HTMLInputElement>("input:checked"))?.focus();
    } finally {
      setBusy(false);
    }
  };

  if (step.kind === "done") {
    const { feed, existed, fetch } = step;
    // `title` is the server's display name (custom title, else the feed's own, blanks ignored, else the URL), never
    // empty, so the dialog applies no rule of its own. The feed list's copy is the live one: a first fetch that
    // finishes after this answer names the feed there.
    const name = boot.data?.feeds.find((f) => f.id === feed.id)?.title ?? feed.title;
    return (
      <Modal
        open
        onOpenChange={(o) => !o && onClose()}
        title={existed ? "You already have this feed" : "Feed added"}
        footer={
          <>
            {onOpenFeed ? (
              <Button variant="solid" onClick={() => onOpenFeed(feed.id)}>
                Open feed
              </Button>
            ) : null}
            <Button onClick={onClose}>Done</Button>
          </>
        }
      >
        <p className="text-sm">{name}</p>
        {existed ? <p className="text-sm text-fg2">It is already in your feed list, so nothing was added.</p> : null}
        {!existed && fetch?.error ? (
          <Notice tone="error">
            Kipple can't fetch this feed yet: {failureReason({ last_error: fetch.error, last_error_class: fetch.error_class }).toLowerCase()}. It will try again later. You can fix the address in Feed health.
          </Notice>
        ) : null}
        {!existed && fetch && !fetch.error && !fetch.pending && fetch.outcome ? (
          <Notice>
            First fetch finished: {fetch.new_items ?? 0} article{fetch.new_items === 1 ? "" : "s"}.
          </Notice>
        ) : null}
        {!existed && (!fetch || fetch.pending) ? <Notice>Kipple is still fetching this feed. Articles appear in a moment.</Notice> : null}
      </Modal>
    );
  }

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Add feed"
      description="Paste a feed or website address."
      footer={
        step.kind === "choose" ? (
          <>
            <Button
              onClick={() => {
                setError(null);
                setStep({ kind: "form" });
              }}
            >
              Back
            </Button>
            <Button variant="solid" disabled={busy || !pick} onClick={() => void run(pick, step.from)}>
              Add selected feed
            </Button>
          </>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            {/* The form's submit button, so Enter in the address field adds the feed too. */}
            <Button variant="solid" type="submit" form="add-feed-form" disabled={busy || !url.trim() || titleTooLong}>
              {busy ? "Adding" : "Add feed"}
            </Button>
          </>
        )
      }
    >
      {error && step.kind === "choose" ? <Notice tone="error">{error}</Notice> : null}
      {step.kind === "choose" ? (
        <fieldset ref={choices} className="flex flex-col gap-2">
          <legend className="mb-1 text-sm font-semibold">This site has more than one feed. Choose one.</legend>
          {step.candidates.map((c) => (
            <RadioCard key={c.url} name="candidate" checked={pick === c.url} onSelect={() => setPick(c.url)} title={c.title || <span className="break-all">{c.url}</span>} help={c.title ? <span className="break-all">{c.url}</span> : undefined} />
          ))}
        </fieldset>
      ) : (
        <form
          id="add-feed-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (url.trim() && !busy && !titleTooLong) void run(url);
          }}
          className="flex flex-col gap-4"
        >
          <Field label="Feed or website address" error={error}>
            {/* type="text": the browser's URL check would refuse example.com, which Kipple accepts. */}
            {(a) => (
              <input {...a} ref={address} type="text" inputMode="url" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus placeholder="https://example.com/feed.xml" value={url} readOnly={busy} onChange={(e) => setUrl(e.target.value)} className={inputCls} />
            )}
          </Field>
          {busy ? (
            <p role="status" className="text-sm text-fg2">
              Looking for a feed at {hostOf(url) || "this address"}...
            </p>
          ) : null}
          {privateOffered ? (
            <Checkbox
              label="Allow addresses on my own network"
              help="This address looks like one on your own network, which Kipple fetches only if you allow it for this feed. Tick it only for a feed you trust."
              checked={privateNet}
              onChange={setPrivateNet}
            />
          ) : null}
          <Disclosure label="Options" open={optionsOpen} onOpenChange={(o) => setOptionsOpen(o || titleTooLong)} tone={titleTooLong ? "warn" : undefined}>
            <Field label="Title (optional)" error={titleTooLong ? `A title can be at most ${MAX_TITLE_CHARS} characters; this one has ${titleChars}.` : null} help={title.trim() ? "Replaces the feed's own title." : "Empty uses the feed's own title."}>
              {(a) => <input {...a} type="text" placeholder="Filled in from the feed" value={title} readOnly={busy} onChange={(e) => setTitle(e.target.value)} className={inputCls} />}
            </Field>
            <Field label="Folder">{(a) => <FolderSelect {...a} value={folder} onChange={setFolder} none="Default folder" disabled={busy} />}</Field>
          </Disclosure>
        </form>
      )}
    </Modal>
  );
}
