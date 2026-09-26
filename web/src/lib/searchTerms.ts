import type { Group } from "./highlight";
import { buildRegexes } from "./highlight";
import { createStore } from "./store";

// The words a search looks for, so results can show them. It follows the server's query parser
// (docs/design.md 2.4, internal/store/searchquery.go): phrases in double quotes, -word and NOT word
// (excluded, never drawn), title:word and author:word (drawn only in that field), word* (a prefix). The server
// stems both the query and the text (porter), so a match is drawn from the start of a word and may run on
// ("run" marks "running"); a trailing s, es, ed, ing or e is trimmed from a longer word so "running" still marks
// "run". That is an approximation of the stemmer, never a claim that a marked word is what matched.
//
// Fallback mode (the response's `fallback: true`) draws the words the fallback used: the positive words, phrases
// split into words, words under 3 letters (2 for Han, Kana and Hangul) dropped.

export interface SearchTerm {
  text: string;
  /** Only this field, for `title:` and `author:`; absent means title, author and content. */
  field?: "title" | "author";
}

export interface SearchOpts {
  fallback?: boolean;
  typing?: boolean;
}

const CJK = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u;
const minLen = (w: string): number => (CJK.test(w) ? 2 : 3);
const runes = (s: string): number => [...s].length;
const space = (c: string | undefined): boolean => c === undefined || /\s/.test(c);

/** Trim a common English ending from a longer word, so the stem matches the forms the server's stemmer joins. */
export function approxStem(w: string): string {
  if (runes(w) < 5) return w;
  for (const suf of ["ing", "ed", "es", "s", "e"]) {
    if (w.endsWith(suf) && runes(w) - suf.length >= 3) return w.slice(0, w.length - suf.length);
  }
  return w;
}

interface Token {
  text: string;
  quoted: boolean;
  field?: "title" | "author";
  exclude: boolean;
  prefix: boolean;
}

/** Split search text the way the server does, keeping only what matters for drawing. */
export function tokenize(q: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  const n = q.length;
  let pendingNot = false;
  while (i < n) {
    while (i < n && space(q[i])) i++;
    if (i >= n) break;
    let exclude = pendingNot;
    pendingNot = false;
    if (q[i] === "-" && i + 1 < n && !space(q[i + 1])) {
      exclude = true;
      i++;
    }
    let field: "title" | "author" | undefined;
    const fm = /^(title|author):/i.exec(q.slice(i));
    if (fm && !space(q[i + fm[0].length])) {
      field = (fm[1] as string).toLowerCase() as "title" | "author";
      i += fm[0].length;
    }
    if (q[i] === '"') {
      i++;
      let text = "";
      while (i < n) {
        if (q[i] === '"') {
          if (q[i + 1] === '"') {
            text += '"';
            i += 2;
            continue;
          }
          i++;
          break;
        }
        text += q[i];
        i++;
      }
      if (text.trim()) out.push({ text: text.trim(), quoted: true, field, exclude, prefix: false });
      continue;
    }
    let j = i;
    while (j < n && !space(q[j])) j++;
    let word = q.slice(i, j);
    i = j;
    if (word === "NOT" && !field && !exclude) {
      pendingNot = true;
      continue;
    }
    let prefix = false;
    if (word.endsWith("*") && runes(word) - 1 >= minLen(word)) {
      prefix = true;
      word = word.slice(0, -1);
    }
    if (word) out.push({ text: word, quoted: false, field, exclude, prefix });
  }
  return out;
}

/** The positive terms to draw. `typing` treats the unfinished last bare word as a prefix (search-as-you-type). */
export function searchTerms(q: string, opts: SearchOpts = {}): SearchTerm[] {
  const toks = tokenize(q);
  const out: SearchTerm[] = [];
  const seen = new Set<string>();
  const add = (text: string, field?: "title" | "author") => {
    const key = `${field ?? ""}|${text}`;
    if (seen.has(key)) return;
    seen.add(key);
    out.push({ text, field });
  };
  const unfinished = !space(q[q.length - 1]);
  const positives = toks.filter((t) => !t.exclude);
  const last = positives[positives.length - 1];
  for (const t of toks) {
    if (t.exclude) continue;
    if (opts.fallback) {
      for (const w of t.text.split(/\s+/)) if (w && runes(w) >= minLen(w)) add(w.toLowerCase(), t.field);
      continue;
    }
    if (t.quoted) {
      add(t.text.replace(/\s+/g, " ").toLowerCase(), t.field);
      continue;
    }
    const asTyped = t.prefix || (opts.typing === true && unfinished && t === last);
    add(asTyped ? t.text.toLowerCase() : approxStem(t.text.toLowerCase()), t.field);
  }
  return out.filter((t) => runes(t.text) >= 2);
}

/** Highlight groups for search terms: one per field set, drawn from the start of a word. */
export function searchGroups(q: string, opts: SearchOpts = {}): Group[] {
  const terms = searchTerms(q, opts);
  const groups: Group[] = [];
  const make = (id: string, field: "title" | "author" | undefined, texts: string[]) => {
    const res = buildRegexes(texts, { caseSensitive: false, fold: true });
    if (res.length === 0) return;
    groups.push({
      id,
      scope: "global",
      folderId: null,
      feedId: null,
      fields: new Set(field ? [field] : ["title", "author", "content"]),
      caseSensitive: false,
      fold: true,
      wholeWord: false,
      prefix: true,
      res,
    });
  };
  make("search:any", undefined, terms.filter((t) => !t.field).map((t) => t.text));
  make("search:title", "title", terms.filter((t) => t.field === "title").map((t) => t.text));
  make("search:author", "author", terms.filter((t) => t.field === "author").map((t) => t.text));
  return groups;
}

/** The search groups the screens draw right now; set by `useSearchHighlight`. */
export const searchHighlightStore = createStore<{ key: string; groups: Group[] }>({ key: "", groups: [] });

/** Set the drawn search terms (or clear them with no query). Cheap when nothing changed. */
export function setSearchHighlight(q: string | undefined, opts: SearchOpts = {}): void {
  const key = q ? `${q}|${opts.fallback ? "f" : ""}|${opts.typing ? "t" : ""}` : "";
  if (searchHighlightStore.get().key === key) return;
  searchHighlightStore.set({ key, groups: q ? searchGroups(q, opts) : [] });
}
