import { beforeEach, describe, expect, it } from "vitest";
import { highlightRanges, segments } from "./highlight";
import { approxStem, searchGroups, searchHighlightStore, searchTerms, setSearchHighlight, tokenize } from "./searchTerms";

const marked = (text: string, q: string, opts: { fallback?: boolean; typing?: boolean } = {}, field: "title" | "author" | "content" = "title"): string[] =>
  segments(text, highlightRanges(text, searchGroups(q, opts).filter((g) => g.fields.has(field))))
    .filter((s) => s.mark)
    .map((s) => s.text);

beforeEach(() => setSearchHighlight(undefined));

describe("tokenize", () => {
  it("follows the server's syntax: phrases, exclusions, fields, prefixes", () => {
    expect(tokenize('"exact phrase" -skip NOT nope title:word author:"jane doe" run*')).toEqual([
      { text: "exact phrase", quoted: true, field: undefined, exclude: false, prefix: false },
      { text: "skip", quoted: false, field: undefined, exclude: true, prefix: false },
      { text: "nope", quoted: false, field: undefined, exclude: true, prefix: false },
      { text: "word", quoted: false, field: "title", exclude: false, prefix: false },
      { text: "jane doe", quoted: true, field: "author", exclude: false, prefix: false },
      { text: "run", quoted: false, field: undefined, exclude: false, prefix: true },
    ]);
  });

  it("keeps a short star as a literal word, like the server (under 3 letters, 2 for CJK)", () => {
    expect(tokenize("ab*")[0]).toMatchObject({ text: "ab*", prefix: false });
    expect(tokenize("日本*")[0]).toMatchObject({ text: "日本", prefix: true });
  });

  it("runs an unterminated quote to the end and doubles embedded quotes", () => {
    expect(tokenize('"open end')[0]).toMatchObject({ text: "open end", quoted: true });
    expect(tokenize('"say ""hi"""')[0]).toMatchObject({ text: 'say "hi"' });
  });

  it("treats an unknown column as one literal word", () => {
    expect(tokenize("foo:bar")[0]).toMatchObject({ text: "foo:bar", field: undefined });
  });
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
