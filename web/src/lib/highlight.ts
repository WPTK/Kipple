import type { Highlight } from "@/api/types";
import { createStore } from "./store";

// Keyword highlighting. The server hands the client each enabled, non-inverted text highlight rule (bootstrap
// `highlights`); the client draws them in titles, excerpts and the article. The matching follows the engine's
// (internal/filter): a term is literal, a phrase is its words in order with any whitespace between them, terms
// are lower-cased unless the rule is case sensitive, accents are dropped unless the rule keeps them (NFKD, then
// combining marks removed), and with "whole words" the characters on each side must not be letters, digits or
// combining marks (skipped on a side where the term itself starts or ends with a non-word character; scripts
// written without spaces are never word characters, so a CJK term matches inside a run of text).
//
// Nothing here builds HTML. Lists render the pieces as React text and <mark> elements; the article body is
// changed by splitting DOM text nodes, after it was sanitized, so a term can never become markup.

/** Most marks drawn in one article, so a common word in a very long piece does not stall the page. */
export const MAX_MARKS = 300;
/** Most characters looked at in one text node and in one article body. */
const NODE_SCAN_CAP = 50_000;
const BODY_SCAN_CAP = 600_000;

export type HighlightField = "title" | "author" | "content" | "url" | "category" | "feed";

export interface Group {
  id: string;
  scope: Highlight["scope"];
  folderId: string | null;
  feedId: string | null;
  fields: ReadonlySet<string>;
  caseSensitive: boolean;
  fold: boolean;
  wholeWord: boolean;
  re: RegExp | null;
}

export type Range = readonly [number, number];

const NO_SPACE_SCRIPT = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Thai}\p{Script=Lao}\p{Script=Khmer}\p{Script=Myanmar}]/u;
const WORD = /[\p{L}\p{N}\p{M}]/u;
/** A word character for the whole-word check: a letter, digit or mark, except in scripts written without spaces. */
export const isWordChar = (ch: string): boolean => WORD.test(ch) && !NO_SPACE_SCRIPT.test(ch);

const MARKS = /\p{M}/gu;
function foldChar(ch: string, caseSensitive: boolean, fold: boolean): string {
  let f = ch;
  if (!caseSensitive) f = f.toLowerCase();
  if (fold) f = f.normalize("NFKD").replace(MARKS, "");
  return f;
}
const foldTerm = (t: string, g: Pick<Group, "caseSensitive" | "fold">): string => [...t].map((c) => foldChar(c, g.caseSensitive, g.fold)).join("");

const escapeRe = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** One alternation for a rule's terms, longest first; whitespace inside a term matches any run of whitespace. */
function buildRegex(terms: string[], g: Pick<Group, "caseSensitive" | "fold">): RegExp | null {
  const parts = terms
    .map((t) => foldTerm(t.trim(), g))
    .filter((t) => t.length > 0)
    .sort((a, b) => b.length - a.length)
    .map((t) => t.split(/\s+/).map(escapeRe).join("\\s+"));
  if (parts.length === 0) return null;
  try {
    return new RegExp(parts.join("|"), "gu");
  } catch {
    return null;
  }
}

export function compileHighlights(rules: readonly Highlight[] | undefined): Group[] {
  const out: Group[] = [];
  for (const r of rules ?? []) {
    const g = { caseSensitive: r.case_sensitive, fold: r.fold_diacritics };
    const re = buildRegex(r.terms, g);
    if (!re) continue;
    out.push({
      id: r.id,
      scope: r.scope,
      folderId: r.folder_id,
      feedId: r.feed_id,
      fields: new Set(r.fields),
      caseSensitive: r.case_sensitive,
      fold: r.fold_diacritics,
      wholeWord: r.whole_word,
      re,
    });
  }
  return out;
}

/** The rules that apply to a field of an article of this feed: global, its folder's, or its own. */
export function groupsFor(all: readonly Group[], field: HighlightField, feed: { id: string; folder_id: string } | undefined): Group[] {
  return all.filter((g) => {
    if (!g.fields.has(field)) return false;
    if (g.scope === "global") return true;
    if (!feed) return false;
    return g.scope === "folder" ? g.folderId === feed.folder_id : g.feedId === feed.id;
  });
}

interface Folded {
  s: string;
  /** For each UTF-16 unit of `s`: where its source character starts and ends in the original text. */
  from: number[];
  to: number[];
}

function fold(text: string, g: Pick<Group, "caseSensitive" | "fold">): Folded {
  let s = "";
  const from: number[] = [];
  const to: number[] = [];
  let i = 0;
  for (const ch of text) {
    const f = foldChar(ch, g.caseSensitive, g.fold);
    for (let k = 0; k < f.length; k++) {
      from.push(i);
      to.push(i + ch.length);
    }
    s += f;
    i += ch.length;
  }
  return { s, from, to };
}

function codePointBefore(s: string, i: number): string {
  if (i <= 0) return "";
  const lo = s.charCodeAt(i - 1);
  if (lo >= 0xdc00 && lo <= 0xdfff && i >= 2) return s.slice(i - 2, i);
  return s.slice(i - 1, i);
}
const codePointAt = (s: string, i: number): string => (i < s.length ? String.fromCodePoint(s.codePointAt(i) as number) : "");

/** The ranges of `text` (original offsets) that one rule marks. */
function rangesOf(text: string, g: Group): Range[] {
  if (!g.re) return [];
  const f = fold(text.length > NODE_SCAN_CAP ? text.slice(0, NODE_SCAN_CAP) : text, g);
  const out: Range[] = [];
  g.re.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = g.re.exec(f.s))) {
    const start = m.index;
    const end = start + m[0].length;
    if (m[0].length === 0) {
      g.re.lastIndex++;
      continue;
    }
    if (g.wholeWord) {
      const first = codePointAt(f.s, start);
      const last = codePointBefore(f.s, end);
      if (isWordChar(first) && isWordChar(codePointBefore(f.s, start))) continue;
      if (isWordChar(last) && isWordChar(codePointAt(f.s, end))) continue;
    }
    out.push([f.from[start] as number, f.to[end - 1] as number]);
  }
  return out;
}

/** All ranges the groups mark in `text`, sorted, overlaps merged. */
export function highlightRanges(text: string, groups: readonly Group[]): Range[] {
  if (!text || groups.length === 0) return [];
  const all: Range[] = [];
  for (const g of groups) all.push(...rangesOf(text, g));
  if (all.length === 0) return [];
  all.sort((a, b) => a[0] - b[0] || b[1] - a[1]);
  const merged: [number, number][] = [];
  for (const [s, e] of all) {
    const last = merged[merged.length - 1];
    if (last && s <= last[1]) last[1] = Math.max(last[1], e);
    else merged.push([s, e]);
  }
  return merged;
}

export interface Segment {
  text: string;
  mark: boolean;
}

/** `text` cut at the ranges, in order, at most `cap` marked. */
export function segments(text: string, ranges: readonly Range[], cap = MAX_MARKS): Segment[] {
  const out: Segment[] = [];
  let at = 0;
  let n = 0;
  for (const [s, e] of ranges) {
    if (n >= cap) break;
    if (s > at) out.push({ text: text.slice(at, s), mark: false });
    out.push({ text: text.slice(s, e), mark: true });
    at = e;
    n++;
  }
  if (at < text.length) out.push({ text: text.slice(at), mark: false });
  return out;
}

// ---- the article body ---------------------------------------------------------------------------

const SKIP_TAGS = new Set(["A", "CODE", "PRE", "KBD", "SAMP", "SCRIPT", "STYLE", "NOSCRIPT", "BUTTON", "TEXTAREA", "SELECT", "OPTION", "MARK", "IFRAME", "SVG"]);

function skipped(node: Node, root: Element): boolean {
  for (let p = node.parentElement; p && p !== root; p = p.parentElement) {
    if (SKIP_TAGS.has(p.tagName.toUpperCase()) || p.classList.contains("kp-embed")) return true;
  }
  return false;
}

/**
 * Wrap the matches in the text of `root` in <mark class="kp-hl">, after the article was sanitized. Text inside
 * links, code, preformatted text, buttons, embeds and existing marks is left alone. Returns how many marks were
 * made (at most `cap`). A match is looked for inside one text node, so a phrase split by inline markup is not marked.
 */
export function wrapMarks(root: HTMLElement, groups: readonly Group[], cap = MAX_MARKS): number {
  if (groups.length === 0) return 0;
  const doc = root.ownerDocument;
  const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  let chars = 0;
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = n as Text;
    if (!t.data.trim() || skipped(t, root)) continue;
    chars += t.data.length;
    nodes.push(t);
    if (chars > BODY_SCAN_CAP) break;
  }
  let made = 0;
  for (const t of nodes) {
    if (made >= cap) break;
    const ranges = highlightRanges(t.data, groups);
    if (ranges.length === 0) continue;
    const segs = segments(t.data, ranges, cap - made);
    const frag = doc.createDocumentFragment();
    for (const s of segs) {
      if (s.mark) {
        const m = doc.createElement("mark");
        m.className = "kp-hl";
        m.textContent = s.text;
        frag.appendChild(m);
        made++;
      } else frag.appendChild(doc.createTextNode(s.text));
    }
    t.replaceWith(frag);
  }
  return made;
}

/** Take the marks made by `wrapMarks` back out, leaving the text as it was. */
export function clearMarks(root: HTMLElement): void {
  const parents = new Set<Node>();
  for (const m of Array.from(root.querySelectorAll("mark.kp-hl"))) {
    if (m.parentNode) parents.add(m.parentNode);
    m.replaceWith(root.ownerDocument.createTextNode(m.textContent ?? ""));
  }
  for (const p of parents) p.normalize();
}

// ---- what the screens draw ------------------------------------------------------------------------

export interface HighlightState {
  groups: Group[];
  feeds: ReadonlyMap<string, string>;
}

/** The compiled rules and the feed-to-folder map; kept by `useSyncHighlights` from the bootstrap. */
export const highlightStore = createStore<HighlightState>({ groups: [], feeds: new Map() });
