import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import { addFeed, invalidateFeeds, type AddFeedResult, type Candidate, type FeedDetail, type FetchOutcome } from "@/api/admin";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { FolderSelect } from "@/ui/FolderSelect";
import { announce } from "@/shell/toasts";

type Step =
  | { kind: "form" }
  | { kind: "choose"; candidates: Candidate[] }
  | { kind: "done"; feed: FeedDetail; existed: boolean; fetch?: FetchOutcome };

/** Fallbacks for the add errors the server explains in its own message (it knows the cause). */
const addFallback: Record<string, string> = {
  invalid_url: "That isn't a web address. Enter a feed or site address such as https://example.com/feed.",
  no_feed: "Kipple found a web page at that address, but the page doesn't link to a feed.",
  not_feed: "That address doesn't answer with a feed or a web page.",
  unreachable: "Kipple couldn't reach that site. Check the address, or try again later.",
  timeout: "The site took too long to answer. Try again later.",
  private_address: "That address is on your own network, which Kipple doesn't fetch from unless you allow it for the feed.",
};

/** A server reason as a sentence: capitalized, ending in a full stop. */
const sentence = (s: string) => {
  const t = s.trim();
  return t ? t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? "" : ".") : t;
};

/** Plain-English message for a failed add. */
export function addError(e: unknown): string {
  if (e instanceof ApiError && e.code && e.code in addFallback) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    return msg ? sentence(msg) : addFallback[e.code];
  }
  return errorMessage(e);
}

/** Add a feed by address: exists, choose-among-candidates and ok flows, then the first-fetch result. */
export function AddFeedDialog({ onClose, onOpenFeed }: { onClose: () => void; onOpenFeed?: (feedId: string) => void }) {
  const qc = useQueryClient();
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");
  const [folder, setFolder] = useState("");
  const [step, setStep] = useState<Step>({ kind: "form" });
  const [pick, setPick] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Offered once the server refused a private address; sent only when turned on.
  const [privateOffered, setPrivateOffered] = useState(false);
  const [privateNet, setPrivateNet] = useState(false);

  const run = async (target: string) => {
    setBusy(true);
    setError(null);
    try {
      const r: AddFeedResult = await addFeed({
        url: target.trim(),
        ...(title.trim() ? { title: title.trim() } : {}),
        ...(folder ? { folder_id: folder } : {}),
        ...(privateNet ? { allow_private_net: true } : {}),
      });
      invalidateFeeds(qc);
      if (r.status === "choose") {
        setStep({ kind: "choose", candidates: r.candidates });
        setPick(r.candidates[0]?.url ?? "");
      } else {
        setStep({ kind: "done", feed: r.feed, existed: r.status === "exists", fetch: r.status === "ok" ? r.fetch : undefined });
        announce(r.status === "exists" ? "You already have this feed" : `Added ${r.feed.title}`);
      }
    } catch (e) {
      setError(addError(e));
      if (e instanceof ApiError && e.code === "private_address") setPrivateOffered(true);
    } finally {
      setBusy(false);
    }
  };

  if (step.kind === "done") {
    const { feed, existed, fetch } = step;
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
        <p className="text-sm">{feed.custom_title || feed.title || feed.url}</p>
        {existed ? <p className="text-sm text-fg2">It is already in your folder list, so nothing was added.</p> : null}
        {!existed && fetch?.error ? (
          <Notice tone="error">
            First fetch failed: {fetch.error}. Kipple will try again later. Check the address on the feed health page.
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
      description="Paste the address of a feed, or of a website and Kipple will look for its feed."
      footer={
        step.kind === "choose" ? (
          <>
            <Button onClick={() => setStep({ kind: "form" })}>Back</Button>
            <Button variant="solid" disabled={busy || !pick} onClick={() => void run(pick)}>
              Add selected feed
            </Button>
          </>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            {/* The form's submit button, so Enter in the address field adds the feed too. */}
            <Button variant="solid" type="submit" form="add-feed-form" disabled={busy || !url.trim()}>
              {busy ? "Adding" : "Add feed"}
            </Button>
          </>
        )
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {step.kind === "choose" ? (
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-sm font-semibold">This site has more than one feed. Choose one.</legend>
          {step.candidates.map((c) => (
            <label key={c.url} className="flex min-h-11 cursor-pointer items-start gap-3 rounded-xl border border-line bg-surface p-3 has-[:checked]:border-accent has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-accent">
              <input type="radio" name="candidate" checked={pick === c.url} onChange={() => setPick(c.url)} className="mt-1 size-5 accent-[var(--kp-accent)]" />
              <span className="min-w-0">
                <span className="block text-sm font-medium">{c.title || c.url}</span>
                <span className="block text-xs break-all text-fg2">{c.url}</span>
              </span>
            </label>
          ))}
        </fieldset>
      ) : (
        <form
          id="add-feed-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (url.trim() && !busy) void run(url);
          }}
          className="flex flex-col gap-4"
        >
          <Field label="Feed or website address">
            {/* type="text": the browser's URL check would refuse example.com, which Kipple accepts. */}
            {(a) => <input {...a} type="text" inputMode="url" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus placeholder="example.com or https://example.com/feed.xml" value={url} onChange={(e) => setUrl(e.target.value)} className={inputCls} />}
          </Field>
          {privateOffered ? (
            <Switch
              label="Allow addresses on my own network"
              help="Lets this feed point at a private or local address, such as a server in your home. Turn it on only for a feed you trust."
              checked={privateNet}
              onChange={setPrivateNet}
            />
          ) : null}
          <Field label="Title (optional)" help="Leave empty to use the feed's own title.">
            {(a) => <input {...a} type="text" value={title} onChange={(e) => setTitle(e.target.value)} className={inputCls} />}
          </Field>
          <Field label="Folder">
            {(a) => (
              <FolderSelect {...a} value={folder} onChange={setFolder} none="Default folder" />
            )}
          </Field>
        </form>
      )}
    </Modal>
  );
}
