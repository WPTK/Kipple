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
const runes = (s: string): number => [...s].length;
/** Go's unicode.IsSpace: ASCII space controls, U+0085 and the Z (which include U+00A0) categories (not U+FEFF, which JS's \s includes). */
const SPACE = /^[\t\n\v\f\r \u0085\p{Z}]$/u;
const isSpace = (c: string | undefined): boolean => c !== undefined && SPACE.test(c);
const WORD_RUNE = /[\p{L}\p{Nd}]/u;
const hasWordRune = (s: string): boolean => WORD_RUNE.test(s);

// The server's limits (internal/store/searchquery.go), copied so the words drawn are the words searched.
const MAX_TERMS = 12;
const MAX_TOKEN_RUNES = 64;
const MAX_INPUT_BYTES = 512;
const MIN_PREFIX = 3;
const MIN_PREFIX_CJK = 2;
const MAX_PREFIXES = 3;

/** Long enough to be searched as a prefix: 3 letters, 2 for Han, Kana and Hangul. */
const prefixOK = (w: string): boolean => runes(w) >= MIN_PREFIX || (runes(w) >= MIN_PREFIX_CJK && CJK.test(w));
const cleanWord = (w: string): string => [...w].slice(0, MAX_TOKEN_RUNES).join("");

/** Trim a common English ending from a longer word, so the stem matches the forms the server's stemmer joins. */
export function approxStem(w: string): string {
  if (runes(w) < 5) return w;
  for (const suf of ["ing", "ed", "es", "s", "e"]) {
    if (w.endsWith(suf) && runes(w) - suf.length >= 3) return w.slice(0, w.length - suf.length);
  }
  return w;
}

/** One parsed unit of search text, as the server's `searchTerm`. */
export interface ParsedTerm {
  col: "" | "title" | "author";
  neg: boolean;
  words: string[];
  phrase: boolean;
  /** A bare word searched as a prefix too (an explicit `word*`, or the typed last word). */
  prefix: boolean;
}

/** "title:" or "author:" followed by something to filter, and how many characters it takes. */
function columnPrefix(s: string[], i: number): { col: "title" | "author"; n: number } | null {
  for (const c of ["title", "author"] as const) {
    const n = c.length;
    if (s.length - i > n + 1 && s[i + n] === ":" && !isSpace(s[i + n + 1]) && s.slice(i, i + n).join("").toLowerCase() === c) return { col: c, n: n + 1 };
  }
  return null;
}

/**
 * Parse search text exactly as the server does (`ParseSearch` in internal/store/searchquery.go): a phrase ends at the
 * next `"` (no escapes), trailing stars are stripped and make a prefix only for a word of 3 letters (2 for CJK) and at
 * most 3 per query, tokens with no letter or digit are dropped, at most 12 terms, and with `typing` the unfinished last
 * word is a prefix only if it is a positive bare word, long enough, and within the prefix cap.
 */
export function parseSearch(raw: string, typing = false): ParsedTerm[] {
  let text = raw;
  if (new TextEncoder().encode(text).length > MAX_INPUT_BYTES) text = new TextDecoder().decode(new TextEncoder().encode(text).slice(0, MAX_INPUT_BYTES));
  const src = [...text].map((c) => (c === "�" || /^\p{Cc}$/u.test(c) ? " " : c));
  const endsOpen = src.length > 0 && !isSpace(src[src.length - 1]);
  const terms: ParsedTerm[] = [];
  let pendingNot = false;
  let prefixes = 0;
  let lastWasBare = false;
  for (let i = 0; i < src.length && terms.length < MAX_TERMS; ) {
    if (isSpace(src[i])) {
      i++;
      continue;
    }
    const t: ParsedTerm = { col: "", neg: false, words: [], phrase: false, prefix: false };
    if (src[i] === "-" && i + 1 < src.length && !isSpace(src[i + 1])) {
      t.neg = true;
      i++;
    }
    const cp = columnPrefix(src, i);
    if (cp) {
      t.col = cp.col;
      i += cp.n;
    }
    if (i < src.length && src[i] === '"') {
      let j = i + 1;
      while (j < src.length && src[j] !== '"') j++;
      for (const w of src.slice(i + 1, j).join("").split(/[\t\n\v\f\r \u0085\p{Z}]+/u)) {
        if (w && hasWordRune(w) && t.words.length < MAX_TERMS) t.words.push(cleanWord(w));
      }
      t.phrase = true;
      i = j + 1;
    } else {
      let j = i;
      while (j < src.length && !isSpace(src[j])) j++;
      let tok = src.slice(i, j).join("");
      i = j;
      const trimmed = tok.replace(/\*+$/, "");
      const stars = tok.length - trimmed.length;
      tok = trimmed;
      if (hasWordRune(tok)) {
        t.words = [cleanWord(tok)];
        if (stars > 0 && prefixOK(t.words[0] as string) && prefixes < MAX_PREFIXES) {
          t.prefix = true; // a shorter or surplus "w*" is just the word
          prefixes++;
        }
      }
    }
    if (t.words.length === 0) {
      lastWasBare = false;
      continue; // nothing searchable (punctuation only, empty quotes)
    }
    if (!t.neg && !t.phrase && t.words.length === 1 && t.col === "" && t.words[0] === "NOT" && !t.prefix) {
      pendingNot = true;
      lastWasBare = false;
      continue;
    }
    if (pendingNot) {
      t.neg = true;
      pendingNot = false;
    }
    lastWasBare = !t.neg && !t.phrase;
    terms.push(t);
  }
  if (typing && lastWasBare && endsOpen && terms.length > 0 && prefixes < MAX_PREFIXES) {
    const last = terms[terms.length - 1] as ParsedTerm;
    if (prefixOK(last.words[0] as string)) last.prefix = true;
  }
  return terms;
}

/**
 * The positive terms to draw. `typing` is the server's search-as-you-type prefix rule (the unfinished last bare word).
 * `fallback` draws what the partial-match fallback searched: the positive words, phrases split, words too short to be
 * a prefix dropped, at most 12.
 */
export function searchTerms(q: string, opts: SearchOpts = {}): SearchTerm[] {
  const terms = parseSearch(q, opts.typing === true);
  const out: SearchTerm[] = [];
  const seen = new Set<string>();
  const add = (text: string, field?: "title" | "author") => {
    const key = `${field ?? ""}|${text}`;
    if (seen.has(key)) return;
    seen.add(key);
    out.push({ text, field });
  };
  let words = 0;
  for (const t of terms) {
    if (t.neg) continue;
    const field = t.col === "" ? undefined : t.col;
    if (opts.fallback) {
      for (const w of t.words) {
        if (words >= MAX_TERMS) break;
        if (!prefixOK(w)) continue;
        words++;
        add(w.toLowerCase(), field);
      }
      continue;
    }
    if (t.phrase) {
      add(t.words.join(" ").toLowerCase(), field);
      continue;
    }
    const w = (t.words[0] as string).toLowerCase();
    add(t.prefix ? w : approxStem(w), field);
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
