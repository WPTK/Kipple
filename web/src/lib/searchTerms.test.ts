import { beforeEach, describe, expect, it } from "vitest";
import { highlightRanges, segments } from "./highlight";
import { approxStem, parseSearch, searchGroups, searchHighlightStore, searchTerms, setSearchHighlight, type ParsedTerm } from "./searchTerms";

const marked = (text: string, q: string, opts: { fallback?: boolean; typing?: boolean } = {}, field: "title" | "author" | "content" = "title"): string[] =>
  segments(text, highlightRanges(text, searchGroups(q, opts).filter((g) => g.fields.has(field))))
    .filter((s) => s.mark)
    .map((s) => s.text);

beforeEach(() => setSearchHighlight(undefined));

// ---- parity with internal/store/search_test.go -----------------------------------------------------------------
// The same expressions the Go tests assert, rendered from parseSearch the way the server renders its terms. If the
// server's parser changes, these fixtures (and searchTerms.ts) change with it.

const q = (w: string): string => `"${w.replace(/"/g, '""')}"`;
const render = (t: ParsedTerm): string => {
  const e = t.phrase ? q(t.words.join(" ")) : q(t.words[0] as string) + (t.prefix ? "*" : "");
  return t.col ? `${t.col} : ${e}` : e;
};
const joinNeg = (base: string, neg: ParsedTerm[]): string => (neg.length ? `(${base}) NOT (${neg.map(render).join(" OR ")})` : base);
/** SearchQuery.Match: null when no positive term remains. */
const match = (raw: string, typing = false): string | null => {
  const terms = parseSearch(raw, typing);
  const pos = terms.filter((t) => !t.neg);
  return pos.length ? joinNeg(pos.map(render).join(" AND "), terms.filter((t) => t.neg)) : null;
};
/** SearchQuery.FallbackMatch: "" when nothing is left. */
const fallback = (raw: string): string => {
  const terms = parseSearch(raw, false);
  const parts: string[] = [];
  let prefixes = 0;
  for (const t of terms.filter((x) => !x.neg)) {
    for (const w of t.words) {
      if (parts.length === 12) break;
      if (![...w].length || !([...w].length >= 3 || ([...w].length >= 2 && /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u.test(w)))) continue;
      parts.push(render({ col: t.col, neg: false, words: [w], phrase: false, prefix: prefixes++ < 3 }));
    }
  }
  return parts.length ? joinNeg(parts.join(" OR "), terms.filter((x) => x.neg)) : "";
};

describe("parseSearch parity with the server's ParseSearch (TestBuildFTSQuery)", () => {
  const cases: [string, string | null][] = [
    ["hello world ", `"hello" AND "world"`],
    ["hello world", `"hello" AND "world"`],
    ["  hello   ", `"hello"`],
    ["foo* ", `"foo"*`],
    ["foo** ", `"foo"*`], // the surplus star is stripped, not kept literal
    ['say "hi there" ', `"say" AND "hi there"`],
    ['"exact phrase"', `"exact phrase"`],
    ['"unterminated phrase', `"unterminated phrase"`],
    ['"" ""', null],
    ["title:foo ", `title : "foo"`],
    ["TITLE:foo ", `title : "foo"`],
    ['author:"jane doe" cats ', `author : "jane doe" AND "cats"`],
    ["body:foo ", `"body:foo"`],
    ["content_text:foo ", `"content_text:foo"`],
    ["{title author}: x ", `"{title" AND "author}:" AND "x"`],
    ["title: ", `"title:"`],
    ["cats -dogs ", `("cats") NOT ("dogs")`],
    ["cats NOT dogs ", `("cats") NOT ("dogs")`],
    ["cats -dogs -title:mice ", `("cats") NOT ("dogs" OR title : "mice")`],
    ['cats -"big dogs" ', `("cats") NOT ("big dogs")`],
    ["-dogs ", null],
    ["NOT dogs ", null],
    ["cats NOT ", `"cats"`],
    ["NOT foo OR bar ", `("OR" AND "bar") NOT ("foo")`],
    ["NEAR(a b) ", `"NEAR(a" AND "b)"`],
    ["^start ", `"^start"`],
    ["a\x00b ", `"a" AND "b"`],
    ["- + ( ) * ", null], // tokens with no letter or digit are dropped
    ["", null],
    ["   ", null],
    ["naïve café ", `"naïve" AND "café"`],
    ["run", `"run"`],
    ["ru", `"ru"`],
    ["ru*", `"ru"`], // shorter than 3: the literal word, star gone
    ["ab*", `"ab"`],
    ["run**", `"run"*`],
    ["a* b* ", `"a" AND "b"`],
    ["aaa* bbb* ccc* ddd* ", `"aaa"* AND "bbb"* AND "ccc"* AND "ddd"`], // at most 3 prefixes
    ["机* 机器* ", `"机" AND "机器"*`],
    ["cats -dogs", `("cats") NOT ("dogs")`],
    ["机器学习", `"机器学习"`],
  ];
  it.each(cases)("%j", (input, want) => expect(match(input)).toBe(want));

  it("ends a phrase at the next quote: doubled quotes are not an escape", () => {
    // "say ""hi""" : the phrase is `say `, then `"hi"`, then `""`.
    expect(parseSearch('"say ""hi"""').map((t) => t.words)).toEqual([["say"], ["hi"]]);
  });

  it("caps a run of words at 12 terms and a token at 64 letters", () => {
    expect(parseSearch("word ".repeat(100))).toHaveLength(12);
    expect(parseSearch("x".repeat(500) + " ")[0]?.words[0]).toHaveLength(64);
  });
});

describe("parseSearch typing parity (TestBuildFTSQueryTyping)", () => {
  const cases: [string, string][] = [
    ["hello world", `"hello" AND "world"*`],
    ["hello world ", `"hello" AND "world"`],
    ["run", `"run"*`],
    ["ru", `"ru"`],
    ["cats -dogs", `("cats") NOT ("dogs")`], // the last term is an exclusion: foo is not the typed prefix
    ["机器", `"机器"*`],
    ["机", `"机"`],
    ["aaa* bbb* ccc* ddd", `"aaa"* AND "bbb"* AND "ccc"* AND "ddd"`],
  ];
  it.each(cases)("%j", (input, want) => expect(match(input, true)).toBe(want));

  it("draws the typed word as a prefix only when the server would search it as one", () => {
    expect(searchTerms("foo -bar", { typing: true }).map((t) => t.text)).toEqual(["foo"]);
    expect(searchTerms("big ru", { typing: true }).map((t) => t.text)).toEqual(["big", "ru"]);
    expect(searchTerms("big runnin", { typing: true }).map((t) => t.text)).toEqual(["big", "runnin"]);
  });
});

describe("parseSearch fallback parity (TestFallbackMatch)", () => {
  const cases: [string, string][] = [
    ["apple pie ", `"apple"* OR "pie"*`],
    ['"big dogs" cats ', `"big"* OR "dogs"* OR "cats"*`],
    ["title:apple pie ", `title : "apple"* OR "pie"*`],
    ["apple -mice ", `("apple"*) NOT ("mice")`],
    ["a apple e ", `"apple"*`],
    ["a b c ", ""],
    ["aaa bbb ccc ddd ", `"aaa"* OR "bbb"* OR "ccc"* OR "ddd"`],
    ["机 机器 ", `"机器"*`],
    ["-mice ", ""],
    ["", ""],
  ];
  it.each(cases)("%j", (input, want) => expect(fallback(input)).toBe(want));
});

describe("searchTerms", () => {
  it("never draws excluded words", () => {
    expect(searchTerms("cat -dog NOT bird").map((t) => t.text)).toEqual(["cat"]);
  });

  it("trims a common ending so the stem matches what the server's stemmer joins", () => {
    expect(approxStem("running")).toBe("runn");
    expect(approxStem("apples")).toBe("appl");
    expect(approxStem("cat")).toBe("cat");
    expect(searchTerms("policies").map((t) => t.text)).toEqual(["polici"]);
  });

  it("with typing, the unfinished last word is a prefix as typed; a trailing space finishes it", () => {
    expect(searchTerms("big runnin", { typing: true }).map((t) => t.text)).toEqual(["big", "runnin"]);
    expect(searchTerms("big running", { typing: true }).map((t) => t.text)).toEqual(["big", "running"]);
    expect(searchTerms("big running ", { typing: true }).map((t) => t.text)).toEqual(["big", "runn"]);
  });

  it("fallback draws the words the fallback used: phrases split, short words dropped", () => {
    expect(searchTerms('"the quick fox" to', { fallback: true }).map((t) => t.text)).toEqual(["the", "quick", "fox"]);
    expect(searchTerms("北京 a", { fallback: true }).map((t) => t.text)).toEqual(["北京"]);
  });
});

describe("marking", () => {
  it("marks from the start of a word and lets it run on", () => {
    expect(marked("Running and runners", "run")).toEqual(["Running", "runners"]);
    expect(marked("Prune the tree", "run")).toEqual([]); // not the start of a word
  });

  it("marks accented text for an unaccented query and is case-blind", () => {
    expect(marked("Café Society", "cafe")).toEqual(["Café"]);
  });

  it("title: marks only the title, author: only the author", () => {
    expect(marked("Ada wins", "title:ada", {}, "title")).toEqual(["Ada"]);
    expect(marked("Ada wins", "title:ada", {}, "author")).toEqual([]);
    expect(marked("Ada Lovelace", "author:lovelace", {}, "author")).toEqual(["Lovelace"]);
    expect(marked("Ada Lovelace", "author:lovelace", {}, "content")).toEqual([]);
  });

  it("marks a phrase across any whitespace", () => {
    expect(marked("the big   red dog", '"big red"')).toEqual(["big   red"]);
  });

  it("in fallback mode marks each word the fallback used, not the phrase", () => {
    expect(marked("a big dog and a red dog", '"big red"', { fallback: true })).toEqual(["big", "red"]);
  });
});

describe("the store", () => {
  it("is empty without a query and only changes when the terms do", () => {
    expect(searchHighlightStore.get().groups).toEqual([]);
    setSearchHighlight("cat");
    const a = searchHighlightStore.get();
    expect(a.groups.length).toBeGreaterThan(0);
    setSearchHighlight("cat");
    expect(searchHighlightStore.get()).toBe(a);
    setSearchHighlight("cat", { fallback: true });
    expect(searchHighlightStore.get()).not.toBe(a);
    setSearchHighlight(undefined);
    expect(searchHighlightStore.get().groups).toEqual([]);
  });
});
