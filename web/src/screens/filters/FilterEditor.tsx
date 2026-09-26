import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useBootstrap } from "@/api/queries";
import { ApiError, errorMessage } from "@/api/client";
import {
  ACTIONS,
  FIELDS,
  LIMITS,
  applyFilter,
  badFilter,
  bodyOf,
  clipName,
  createFilter,
  draftOf,
  fieldGroup,
  invalidateFilterData,
  previewFilter,
  updateFilter,
  useFilters,
  type ApplyRun,
  type FilterDraft,
  type FilterField,
  type Preview,
} from "@/api/filters";
import { relativeTime } from "@/lib/format";
import { closeFilterEditor, type EditorRequest, type SimilarSeed } from "@/lib/similar";
import { announce, toast } from "@/shell/toasts";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";

export const PREVIEW_DEBOUNCE_MS = 600;

type Group = ReturnType<typeof fieldGroup>;
type Issue = { field: string; message: string };

const runeCount = (s: string): number => [...s].length;
const byteCount = (s: string): number => new TextEncoder().encode(s).length;

/** Why a term cannot be added, or null. The server checks again; this saves a round trip. */
export function termProblem(term: string, d: Pick<FilterDraft, "kind" | "terms">): string | null {
  if (d.kind === "regex") {
    if (byteCount(term) > LIMITS.regexBytes) return `A pattern can be at most ${LIMITS.regexBytes} bytes.`;
    if (d.terms.length >= LIMITS.regexPatterns) return `A filter can have at most ${LIMITS.regexPatterns} patterns.`;
  } else {
    if (runeCount(term) > LIMITS.termRunes) return `A word or phrase can be at most ${LIMITS.termRunes} characters.`;
    if (d.terms.length >= LIMITS.termsPerRule) return `A filter can have at most ${LIMITS.termsPerRule} words or phrases.`;
  }
  return null;
}

/** A name for a filter saved without one. */
export function autoName(d: Pick<FilterDraft, "action" | "terms">): string {
  const verb = ACTIONS.find((a) => a.id === d.action)?.label ?? "Filter";
  const list = d.terms.slice(0, 3).join(", ");
  const name = `${verb}: ${list}${d.terms.length > 3 ? "…" : ""}`;
  const runes = [...name];
  // 80 characters for the eye, and never more than the server's 200 bytes (CJK is three bytes a character).
  return clipName(runes.length > 80 ? `${runes.slice(0, 79).join("")}…` : name);
}

const verbFor: Record<FilterDraft["action"], string> = {
  mute: "would be muted",
  mark_read: "would be marked read",
  star: "would be starred",
  highlight: "match",
};

/** The live count: "12 articles would be muted". */
export function previewSummary(p: Preview, action: FilterDraft["action"]): string {
  const n = p.matches;
  if (n === 0) return action === "highlight" ? "No stored articles match." : "No stored articles would change.";
  return `${n.toLocaleString()} article${n === 1 ? "" : "s"} ${action === "highlight" ? (n === 1 ? "matches" : "match") : verbFor[action]}${p.truncated ? " or more" : ""}`;
}

type PreviewState = { status: "idle" | "loading" | "error"; data?: Preview; issue?: Issue };

/** POST /api/filters/preview, 600 ms after the last change to the rule; a newer edit cancels the older request. */
export function usePreview(d: FilterDraft, id: string | undefined, includeRead: boolean): PreviewState {
  const [res, setRes] = useState<{ key: string; data?: Preview; issue?: Issue; failed?: boolean } | null>(null);
  const key = JSON.stringify([bodyOf(d), includeRead, id ?? null]);
  const ready = d.terms.length > 0 && d.fields.length > 0 && (d.scope === "global" || (d.scope === "folder" ? !!d.folder_id : !!d.feed_id));
  useEffect(() => {
    if (!ready) return;
    const [body] = JSON.parse(key) as [Record<string, unknown>];
    const ac = new AbortController();
    const t = setTimeout(() => {
      previewFilter(body, { id, includeRead, signal: ac.signal }).then(
        (data) => !ac.signal.aborted && setRes({ key, data }),
        (e: unknown) => {
          if (ac.signal.aborted) return;
          const bf = badFilter(e);
          setRes((prev) => (bf ? { key, issue: bf } : { key, data: prev?.data, failed: true }));
        },
      );
    }, PREVIEW_DEBOUNCE_MS);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [key, ready, id, includeRead]);
  if (!ready) return { status: "idle" };
  if (!res || res.key !== key) return { status: "loading", data: res?.data };
  return { status: res.failed ? "error" : "idle", data: res.data, issue: res.issue };
}

function Group({ legend, help, error, children }: { legend: string; help?: ReactNode; error?: string | null; children: ReactNode }) {
  const id = useId();
  return (
    <fieldset className="flex min-w-0 flex-col gap-2" aria-describedby={[help ? `${id}-h` : "", error ? `${id}-e` : ""].filter(Boolean).join(" ") || undefined}>
      <legend className="text-sm font-semibold">{legend}</legend>
      {help ? (
        <p id={`${id}-h`} className="-mt-1 text-xs text-fg2">
          {help}
        </p>
      ) : null}
      {children}
      {error ? (
        <p id={`${id}-e`} role="alert" className="text-sm text-danger">
          {error}
        </p>
      ) : null}
    </fieldset>
  );
}

function Check({ label, help, checked, onChange, disabled, busy }: { label: string; help?: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; busy?: boolean }) {
  const id = useId();
  return (
    <label htmlFor={id} aria-busy={busy || undefined} className={cn("flex min-h-11 items-start gap-3 py-1 text-sm", disabled ? "opacity-60" : "cursor-pointer")}>
      <input id={id} type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-0.5 size-5 shrink-0 accent-[var(--kp-accent)]" />
      <span>
        {label}
        {help ? <span className="block text-xs text-fg2">{help}</span> : null}
      </span>
    </label>
  );
}

function Choice({ name, value, current, label, help, onPick, disabled }: { name: string; value: string; current: string; label: string; help?: string; onPick: (v: string) => void; disabled?: boolean }) {
  const on = value === current;
  return (
    <label
      className={cn(
        "relative flex min-h-11 items-start gap-3 rounded-xl border p-3 text-sm has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-accent",
        on ? "border-accent bg-selection" : "border-line bg-surface",
        disabled ? "opacity-60" : "cursor-pointer hover:bg-selection",
      )}
    >
      <input type="radio" name={name} value={value} checked={on} disabled={disabled} onChange={() => onPick(value)} className="mt-0.5 size-5 shrink-0 accent-[var(--kp-accent)]" />
      <span>
        <span className="font-semibold">{label}</span>
        {help ? <span className="block text-xs text-fg2">{help}</span> : null}
      </span>
    </label>
  );
}

function Chips({ terms, mono, onRemove }: { terms: string[]; mono: boolean; onRemove: (i: number) => void }) {
  if (terms.length === 0) return null;
  return (
    <ul aria-label="Terms in this filter" className="flex flex-wrap gap-2">
      {terms.map((t, i) => (
        <li key={`${i}:${t}`} className={cn("flex min-h-9 max-w-full items-center gap-1 rounded-full border border-line bg-surface py-0.5 pr-1 pl-3 text-sm", mono && "font-mono")}>
          <span className="truncate">{t}</span>
          <button type="button" aria-label={`Remove ${t}`} onClick={() => onRemove(i)} className="hit-row inline-flex size-8 shrink-0 items-center justify-center rounded-full hover:bg-selection">
            <X aria-hidden="true" className="size-4" />
          </button>
        </li>
      ))}
    </ul>
  );
}

function TermsInput({ d, set, error }: { d: FilterDraft; set: (patch: Partial<FilterDraft>) => void; error: string | null }) {
  const id = useId();
  const [text, setText] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const regex = d.kind === "regex";
  const max = regex ? LIMITS.regexPatterns : LIMITS.termsPerRule;
  const add = () => {
    const parts = text.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
    if (parts.length === 0) return;
    let terms = [...d.terms];
    for (const p of parts) {
      if (terms.includes(p)) continue;
      const bad = termProblem(p, { kind: d.kind, terms });
      if (bad) {
        setProblem(bad);
        set({ terms });
        return;
      }
      terms = [...terms, p];
    }
    setProblem(null);
    setText("");
    set({ terms });
  };
  const shown = problem ?? error;
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={id} className="text-sm font-semibold">
        {regex ? "Patterns" : "Words or phrases"}
      </label>
      <div className="flex gap-2">
        <input
          id={id}
          value={text}
          autoComplete="off"
          spellCheck={false}
          placeholder={regex ? "For example ^\\[Sponsored\\]" : "For example: giveaway"}
          aria-describedby={`${id}-h${shown ? ` ${id}-e` : ""}`}
          aria-invalid={shown ? true : undefined}
          onChange={(e) => {
            setText(e.target.value);
            setProblem(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
          className={cn(inputCls, regex && "font-mono")}
        />
        <Button onClick={add} disabled={text.trim() === ""}>
          Add
        </Button>
      </div>
      <p id={`${id}-h`} className="text-xs text-fg2">
        {regex
          ? `A regular expression (RE2 syntax, not case sensitive unless you turn that on). At most ${LIMITS.regexPatterns} patterns, ${LIMITS.regexBytes} bytes each. `
          : "A phrase matches its words in order. "}
        {d.terms.length} of {max} used. The rule fires when any one of them matches.
      </p>
      {shown ? (
        <p id={`${id}-e`} role="alert" className="text-sm text-danger">
          {shown}
        </p>
      ) : null}
      <Chips terms={d.terms} mono={regex} onRemove={(i) => set({ terms: d.terms.filter((_, j) => j !== i) })} />
    </div>
  );
}

function PreviewCards({ p }: { p: Preview }) {
  if (p.sample.length === 0) return null;
  return (
    <ul aria-label="Sample of matching articles" className="flex max-h-72 flex-col gap-2 overflow-y-auto">
      {p.sample.map((c) => (
        <li key={c.id} className="rounded-xl border border-line bg-surface px-3 py-2">
          <p className="text-xs text-fg2">
            {c.source}
            {c.published_at ? ` · ${relativeTime(c.published_at)}` : ""}
          </p>
          <p className="line-clamp-2 text-sm font-semibold">{c.title || "Untitled"}</p>
          {c.excerpt ? <p className="line-clamp-1 text-xs text-fg2">{c.excerpt}</p> : null}
        </li>
      ))}
    </ul>
  );
}

/** The live preview: the count (a polite live region), the warnings, the sample cards and the include-read switch. */
function PreviewPanel({ d, state, includeRead, setIncludeRead }: { d: FilterDraft; state: PreviewState; includeRead: boolean; setIncludeRead: (v: boolean) => void }) {
  const p = state.data;
  return (
    <section aria-label="Preview" className="flex flex-col gap-3 rounded-2xl border border-line bg-surface p-3">
      <h3 className="text-sm font-bold">Preview</h3>
      <div role="status" aria-live="polite" aria-atomic="true" className="min-h-6 text-sm">
        {d.terms.length === 0 ? (
          <span className="text-fg2">Add a word to see which stored articles it would affect.</span>
        ) : state.issue ? (
          <span className="text-fg2">{state.issue.message}</span>
        ) : state.status === "error" ? (
          <span className="text-danger">Couldn't run the preview. It will try again when you change the filter.</span>
        ) : p ? (
          <span className={cn(state.status === "loading" && "opacity-60")}>
            <strong>{previewSummary(p, d.action)}</strong>
            {p.truncated ? ` (checked the newest ${p.scanned.toLocaleString()} articles)` : ""}
          </span>
        ) : (
          <span className="text-fg2">Checking your articles…</span>
        )}
      </div>
      {p?.warnings.map((w) => (
        <Notice key={w.code} tone="warn" role="status">
          {w.message}
        </Notice>
      ))}
      <Switch label="Include already-read articles" help="Off looks only at unread articles that are not starred. On also counts, and later applies to, articles you have read." checked={includeRead} onChange={setIncludeRead} />
      {p ? <PreviewCards p={p} /> : null}
    </section>
  );
}

const inlineTarget = (g: Group): boolean => g !== "other";

export interface FilterEditorProps {
  request: NonNullable<EditorRequest>;
}

/** Create or edit a filter: every field of the rule with a live preview, and optionally apply it to stored articles. */
export function FilterEditor({ request }: FilterEditorProps) {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const filters = useFilters();
  const editing = request.mode === "edit" ? filters.data?.find((f) => f.id === request.id) : undefined;
  const seed: SimilarSeed | null = request.mode === "create" && "keywords" in request.seed ? request.seed : null;
  const initial = useMemo<FilterDraft | null>(() => {
    if (request.mode === "create") return request.seed.draft;
    return editing ? draftOf(editing) : null;
    // The draft is captured once, when the rule is loaded; later edits are the user's.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [request.mode, editing?.id]);
  if (request.mode === "edit" && filters.isPending) return null;
  if (!initial) {
    return (
      <Modal open onOpenChange={(o) => !o && closeFilterEditor()} title="Filter not found" description="It may have been deleted on another device." footer={<Button onClick={closeFilterEditor}>Close</Button>} />
    );
  }
  return <EditorForm key={editing?.id ?? "new"} initial={initial} id={editing?.id} seed={seed} folders={boot.data?.folders ?? []} feeds={(boot.data?.feeds ?? []).filter((f) => !f.is_archive)} qc={qc} />;
}

function EditorForm({
  initial,
  id,
  seed,
  folders,
  feeds,
  qc,
}: {
  initial: FilterDraft;
  id: string | undefined;
  seed: SimilarSeed | null;
  folders: { id: string; name: string }[];
  feeds: { id: string; title: string; folder_id: string }[];
  qc: ReturnType<typeof useQueryClient>;
}) {
  const [d, setD] = useState<FilterDraft>(initial);
  const [includeRead, setIncludeRead] = useState(false);
  const [apply, setApply] = useState(false);
  const [issue, setIssue] = useState<Issue | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  const set = (patch: Partial<FilterDraft>) => {
    setD((cur) => ({ ...cur, ...patch }));
    setIssue(null);
  };
  const preview = usePreview(d, id, includeRead);
  const scopeName = useId();
  const kindName = useId();
  const actionName = useId();

  // A problem the server names (from the preview or from Save) shows next to the control it belongs to.
  const shown = issue ?? preview.issue ?? null;
  const at = (g: Group): string | null => (shown && fieldGroup(shown.field) === g ? shown.message : null);
  const other = shown && !inlineTarget(fieldGroup(shown.field)) ? shown.message : null;

  const regex = d.kind === "regex";
  const canApply = d.action !== "highlight" && d.enabled;
  const scopeOk = d.scope === "global" || (d.scope === "folder" ? !!d.folder_id : !!d.feed_id);
  const nameTooLong = byteCount(d.name.trim()) > LIMITS.nameBytes;
  // The preview on screen (and the count in the Apply help) is for an older state of the rule until this is false.
  const previewStale = d.terms.length > 0 && preview.status === "loading";
  const ok = d.terms.length > 0 && d.fields.length > 0 && scopeOk && !nameTooLong;

  // Suggestions from the article "Mute similar..." started from: one click adds a word or the author.
  const addTerm = (t: string, field?: FilterField) => {
    if (d.terms.includes(t) || termProblem(t, d)) return;
    set({ terms: [...d.terms, t], ...(field && !d.fields.includes(field) ? { fields: [...d.fields, field] } : {}) });
  };

  const save = async () => {
    if (!ok) return;
    const draft: FilterDraft = { ...d, name: d.name.trim() || autoName(d) };
    setBusy(true);
    setFailed(null);
    setIssue(null);
    let saved = false;
    try {
      let run: ApplyRun | { error: "busy" } | null = null;
      if (id) {
        await updateFilter(id, draft);
        saved = true;
        if (apply && canApply) run = await applyFilter(id, includeRead);
      } else {
        const res = await createFilter(draft, apply && canApply ? { include_read: includeRead } : undefined);
        run = res.applied;
      }
      invalidateFilterData(qc);
      closeFilterEditor();
      if (run && "error" in run) toast("Filter saved. Another filter is still being applied, so this one was not applied to stored articles. Apply it from the Filters list in a moment.");
      else if (run) toast("Filter saved. Applying it to your articles…");
      else {
        toast("Filter saved");
        announce("Filter saved");
      }
    } catch (e) {
      const bf = badFilter(e);
      if (saved && e instanceof ApiError && e.status === 409) {
        // The edit is stored; only the apply was refused because another one is running.
        invalidateFilterData(qc);
        setApply(false);
        setFailed("Saved. Another apply is running; try Apply again in a moment.");
      } else if (saved) {
        invalidateFilterData(qc);
        setApply(false);
        setFailed(`Saved, but it was not applied to stored articles: ${errorMessage(e)}`);
      } else if (bf) setIssue(bf);
      else setFailed(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      size="lg"
      onOpenChange={(o) => !o && !busy && closeFilterEditor()}
      title={id ? "Edit filter" : "New filter"}
      description="A filter looks for words in new articles as they arrive and mutes, marks read, stars or highlights the ones that match."
      footer={
        <div className="flex w-full flex-wrap items-center gap-2">
          <p aria-hidden="true" data-busy={previewStale || undefined} className={cn("min-w-0 flex-1 text-xs text-fg2", previewStale && "opacity-60")}>
            {d.terms.length === 0 ? "" : preview.data ? previewSummary(preview.data, d.action) : preview.status === "loading" ? "Checking…" : ""}
          </p>
          <Button variant="ghost" onClick={closeFilterEditor} disabled={busy}>
            Cancel
          </Button>
          <Button variant="solid" onClick={() => void save()} disabled={busy || !ok || (apply && canApply && previewStale)}>
            {busy ? "Saving" : "Save filter"}
          </Button>
        </div>
      }
    >
      {seed ? (
        <section aria-label="Suggestions from this article" className="flex flex-col gap-2 rounded-2xl border border-line bg-surface p-3">
          <p className="text-sm font-semibold">Suggestions from this article</p>
          <p className="text-xs text-fg2">Tap a word to add it. The rule starts on {seed.feedTitle} only; change that below.</p>
          <div className="flex flex-wrap gap-2">
            {seed.keywords.map((k) => (
              <Button key={k} onClick={() => addTerm(k)} disabled={d.terms.includes(k)} aria-label={`Add ${k}`} className="min-h-9">
                {k}
              </Button>
            ))}
            {seed.author ? (
              <Button onClick={() => addTerm(seed.author as string, "author")} disabled={d.terms.includes(seed.author)} aria-label={`Add author ${seed.author}`} className="min-h-9">
                Author: {seed.author}
              </Button>
            ) : null}
          </div>
        </section>
      ) : null}

      <Field label="Name" help="Optional. Shown in Settings and on each muted article." error={nameTooLong ? `A name can be at most ${LIMITS.nameBytes} bytes (about ${Math.floor(LIMITS.nameBytes / 3)} Chinese, Japanese or Korean characters).` : null}>
        {(a) => <input {...a} value={d.name} onChange={(e) => set({ name: e.target.value })} className={inputCls} />}
      </Field>

      <Group legend="Where it applies" error={at("scope") ?? at("target")}>
        <div className="grid gap-2 min-[520px]:grid-cols-3">
          <Choice name={scopeName} value="global" current={d.scope} label="Everywhere" help="Every feed" onPick={() => set({ scope: "global", folder_id: null, feed_id: null })} />
          <Choice name={scopeName} value="folder" current={d.scope} label="A folder" help="Feeds in one folder" onPick={() => set({ scope: "folder", feed_id: null, folder_id: d.folder_id ?? folders[0]?.id ?? null })} />
          <Choice name={scopeName} value="feed" current={d.scope} label="A feed" help="One feed only" onPick={() => set({ scope: "feed", folder_id: null, feed_id: d.feed_id ?? feeds[0]?.id ?? null })} />
        </div>
        {d.scope === "folder" ? (
          <Field label="Folder">
            {(a) => (
              <select {...a} value={d.folder_id ?? ""} onChange={(e) => set({ folder_id: e.target.value || null })} className={inputCls}>
                {folders.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
        ) : null}
        {d.scope === "feed" ? (
          <Field label="Feed">
            {(a) => (
              <select {...a} value={d.feed_id ?? ""} onChange={(e) => set({ feed_id: e.target.value || null })} className={inputCls}>
                {feeds.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.title}
                  </option>
                ))}
              </select>
            )}
          </Field>
        ) : null}
      </Group>

      <Group legend="How to match" error={at("kind")}>
        <div className="grid gap-2 min-[520px]:grid-cols-2">
          <Choice
            name={kindName}
            value="text"
            current={d.kind}
            label="Words or phrases"
            help="The simple choice. Matches whole words."
            onPick={() => set({ kind: "text", terms: d.terms.slice(0, LIMITS.termsPerRule) })}
          />
          <Choice
            name={kindName}
            value="regex"
            current={d.kind}
            label="Regular expression"
            help="For patterns. Not available for Highlight."
            onPick={() => set({ kind: "regex", terms: d.terms.slice(0, LIMITS.regexPatterns) })}
          />
        </div>
      </Group>

      <TermsInput d={d} set={set} error={at("terms")} />

      <Group legend="Look in" help="The rule fires when a term is found in any of the parts you tick." error={at("fields")}>
        <div className="grid gap-x-4 min-[520px]:grid-cols-2">
          {FIELDS.map((f) => (
            <Check
              key={f.id}
              label={f.label}
              checked={d.fields.includes(f.id)}
              onChange={(v) => set({ fields: v ? [...d.fields, f.id] : d.fields.filter((x) => x !== f.id) })}
            />
          ))}
        </div>
        {d.fields.length === 0 ? <p className="text-sm text-danger">Tick at least one part to look in.</p> : null}
      </Group>

      <Group legend="Options" error={at("options")}>
        {regex ? null : <Check label="Match whole words only" help="On: cat matches “cat” but not “category”." checked={d.whole_word} onChange={(v) => set({ whole_word: v })} />}
        <Check label="Ignore capitalization" help="On: Apple matches apple and APPLE." checked={!d.case_sensitive} onChange={(v) => set({ case_sensitive: !v })} />
        {regex ? null : <Check label="Ignore accents" help="On: cafe matches café." checked={d.fold_diacritics} onChange={(v) => set({ fold_diacritics: v })} />}
        <Check
          label="Act when it does NOT match"
          help="Inverts the rule: it fires on articles that contain none of the terms. Articles with nothing in the parts you chose (say, no author) count as not matching."
          checked={d.invert}
          disabled={d.action === "highlight"}
          onChange={(v) => set({ invert: v })}
        />
      </Group>

      <Group legend="What it does" error={at("action")}>
        <div className="grid gap-2">
          {ACTIONS.map((a) => (
            <Choice key={a.id} name={actionName} value={a.id} current={d.action} label={a.label} help={a.help} onPick={(v) => set({ action: v as FilterDraft["action"], ...(v === "highlight" ? { invert: false } : {}) })} />
          ))}
        </div>
        {regex && d.action === "highlight" ? <p className="text-xs text-fg2">Highlight works with words or phrases only.</p> : null}
      </Group>

      <PreviewPanel d={d} state={preview} includeRead={includeRead} setIncludeRead={setIncludeRead} />

      {canApply ? (
        <Check
          label="Apply to existing articles"
          disabled={previewStale}
          busy={previewStale}
          help={
            preview.data && d.terms.length && !previewStale
              ? `Also ${d.action === "mute" ? "mute" : d.action === "star" ? "star" : "mark read"} the ${preview.data.matches.toLocaleString()}${preview.data.truncated ? "+" : ""} stored article${preview.data.matches === 1 ? "" : "s"} it matches now. New articles are always filtered.`
              : "Also runs the rule over the articles Kipple has already stored. New articles are always filtered. Progress shows at the top of the screen."
          }
          checked={apply}
          onChange={setApply}
        />
      ) : d.action !== "highlight" ? (
        <p className="text-xs text-fg2">A filter that is turned off does nothing, so it can't be applied to stored articles.</p>
      ) : null}
      <Switch label="Turn this filter on" checked={d.enabled} onChange={(v) => set({ enabled: v })} />
      {other ? <Notice tone="error">{other}</Notice> : null}
      {failed ? <Notice tone="error">{failed}</Notice> : null}
    </Modal>
  );
}
