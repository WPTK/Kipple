import type { Card } from "@/api/types";
import { LIMITS, emptyDraft, type FilterDraft } from "@/api/filters";
import { createStore } from "./store";
import { STOP_WORDS } from "./stopwords";

// "Mute similar...": a filter rule started from an article. The feed becomes the scope, the author and the
// distinctive words of the title become suggestions the user taps to add. The rule itself starts with no terms.

/** Scripts written without spaces between words: a whole headline is one "word" there, so it is cut into short pieces. */
const UNSPACED = "\\p{Script=Han}\\p{Script=Hiragana}\\p{Script=Katakana}\\p{Script=Thai}\\p{Script=Lao}\\p{Script=Khmer}\\p{Script=Myanmar}";
const UNSPACED_RUN = new RegExp(`[${UNSPACED}]+|[^${UNSPACED}]+`, "gu");
const IS_UNSPACED = new RegExp(`^[${UNSPACED}]`, "u");
/** A piece of an unspaced-script run, in characters, and how many pieces one run may give. */
const CHUNK = 3;
const CHUNKS_PER_RUN = 3;

/**
 * Distinctive words of a title: letters and digits only, at least 4 characters, no common words, in order, no repeats.
 * A run of Chinese, Japanese or Thai text (no spaces) gives its first few 3-character pieces instead of one long word.
 * No keyword is longer than the server's limit for a term.
 */
export function titleKeywords(title: string, max = 8): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const add = (w: string): boolean => {
    const key = w.toLowerCase();
    if (seen.has(key)) return false;
    seen.add(key);
    out.push(w);
    return out.length >= max;
  };
  for (const raw of title.split(/[^\p{L}\p{N}']+/u)) {
    for (const piece of raw.match(UNSPACED_RUN) ?? []) {
      if (IS_UNSPACED.test(piece)) {
        const runes = [...piece];
        if (runes.length < 2) continue;
        const parts = runes.length <= CHUNK + 1 ? [piece] : Array.from({ length: Math.min(CHUNKS_PER_RUN, Math.ceil(runes.length / CHUNK)) }, (_, i) => runes.slice(i * CHUNK, i * CHUNK + CHUNK).join(""));
        for (const part of parts) if ([...part].length >= 2 && add(part)) return out;
        continue;
      }
      const w = [...piece.replace(/^'+|'+$/g, "")].slice(0, LIMITS.termRunes).join("");
      if ([...w].length < 4 || STOP_WORDS.has(w.toLowerCase()) || /^\d+$/.test(w)) continue;
      if (add(w)) return out;
    }
  }
  return out;
}

export interface SimilarSeed {
  draft: FilterDraft;
  /** Suggestions the editor offers as one-click additions. */
  keywords: string[];
  author: string | null;
  feedTitle: string;
}

/** The rule "Mute similar..." starts with: this article's feed, no terms (the user taps the suggestions), action Mute. */
export function similarSeed(item: Pick<Card, "title" | "author" | "feed_id" | "source">, feedTitle?: string): SimilarSeed {
  const author = item.author.trim() ? item.author.trim() : null;
  return {
    draft: emptyDraft({ scope: "feed", feed_id: item.feed_id, terms: [], fields: ["title"], action: "mute", name: "" }),
    keywords: titleKeywords(item.title),
    author,
    feedTitle: feedTitle ?? item.source,
  };
}

/** What the global editor should show: a rule to create (with suggestions) or an existing one to edit. */
export type EditorRequest = { mode: "create"; seed: SimilarSeed | { draft: FilterDraft } } | { mode: "edit"; id: string } | null;

export const filterEditorStore = createStore<EditorRequest>(null);
export const openFilterEditor = (r: NonNullable<EditorRequest>): void => filterEditorStore.set(r);
export const closeFilterEditor = (): void => filterEditorStore.set(null);
