import { describe, expect, it } from "vitest";
import type { Highlight } from "@/api/types";
import { MAX_MARKS, clearMarks, compileHighlights, groupsFor, highlightRanges, segments, wrapMarks, type Group } from "./highlight";

const rule = (terms: string[], over: Partial<Highlight> = {}): Highlight => ({
  id: "1",
  scope: "global",
  folder_id: null,
  feed_id: null,
  terms,
  fields: ["title", "content"],
  case_sensitive: false,
  whole_word: true,
  fold_diacritics: true,
  ...over,
});

const marked = (text: string, terms: string[], over: Partial<Highlight> = {}): string[] =>
  segments(text, highlightRanges(text, compileHighlights([rule(terms, over)])))
    .filter((s) => s.mark)
    .map((s) => s.text);

describe("matching (the engine's rules)", () => {
  it("whole words by default: cat is not category", () => {
    expect(marked("The cat sat in the category of cats", ["cat"])).toEqual(["cat"]);
    expect(marked("The cat sat in the category of cats", ["cat"], { whole_word: false })).toEqual(["cat", "cat", "cat"]);
  });

  it("ignores case unless the rule is case sensitive", () => {
    expect(marked("Apple and APPLE and apple", ["apple"])).toEqual(["Apple", "APPLE", "apple"]);
    expect(marked("Apple and APPLE and apple", ["apple"], { case_sensitive: true })).toEqual(["apple"]);
  });

  it("drops accents unless the rule keeps them", () => {
    expect(marked("Un café à Paris", ["cafe"])).toEqual(["café"]);
    expect(marked("Un café à Paris", ["cafe"], { fold_diacritics: false })).toEqual([]);
    expect(marked("Un cafe à Paris", ["café"])).toEqual(["cafe"]);
  });

  it("a phrase is its words in order with any whitespace between", () => {
    expect(marked("big   data\nand big data", ["big data"])).toEqual(["big   data", "big data"]);
    expect(marked("data big", ["big data"])).toEqual([]);
  });

  it("checks word edges only on the side where the term has a word character", () => {
    expect(marked("Call C++ now, c++11 later", ["c++"])).toEqual(["C++", "c++"]);
    expect(marked("a.net b.netty", [".net"])).toEqual([".net"]); // the left side is not checked, the right one is
  });

  it("scripts written without spaces match as substrings", () => {
    expect(marked("新iPhone发布", ["iphone"])).toEqual(["iPhone"]);
    expect(marked("最新のニュース", ["ニュース"])).toEqual(["ニュース"]);
  });

  it("marks the longest of overlapping terms, merges overlaps, and maps back to the original text", () => {
    expect(marked("Ünited Nations", ["united nations", "united"])).toEqual(["Ünited Nations"]);
    const text = "ﬁne ﬁne";
    expect(marked(text, ["fine"])).toEqual(["ﬁne", "ﬁne"]); // a ligature folds to two letters; the mark covers the original
  });

  it("treats a term literally: no regex, no markup", () => {
    expect(marked("1+1=2 and (a|b) and [x]", ["1+1", "(a|b)", "[x]"], { whole_word: false })).toEqual(["1+1", "(a|b)", "[x]"]);
    expect(marked("Use <b> for bold", ["<b>"])).toEqual(["<b>"]);
  });

  it("ignores empty and unusable terms", () => {
    expect(compileHighlights([rule([""]), rule(["   "]), rule(["́"])])).toEqual([]);
  });
});

describe("scope and fields", () => {
  const all = compileHighlights([
    rule(["a"], { id: "g", fields: ["title"] }),
    rule(["b"], { id: "f", scope: "folder", folder_id: "10", fields: ["title", "content"] }),
    rule(["c"], { id: "d", scope: "feed", feed_id: "7", fields: ["author"] }),
  ]);
  const ids = (gs: Group[]) => gs.map((g) => g.id);

  it("applies global, folder and feed rules to the right feeds", () => {
    expect(ids(groupsFor(all, "title", { id: "1", folder_id: "10" }))).toEqual(["g", "f"]);
    expect(ids(groupsFor(all, "title", { id: "1", folder_id: "11" }))).toEqual(["g"]);
    expect(ids(groupsFor(all, "author", { id: "7", folder_id: "11" }))).toEqual(["d"]);
    expect(ids(groupsFor(all, "author", { id: "8", folder_id: "11" }))).toEqual([]);
  });

  it("only draws a field the rule looks at, and knows nothing of an unknown feed's scope", () => {
    expect(ids(groupsFor(all, "content", { id: "1", folder_id: "10" }))).toEqual(["f"]);
    expect(ids(groupsFor(all, "content", { id: "1", folder_id: "11" }))).toEqual([]);
    expect(ids(groupsFor(all, "title", undefined))).toEqual(["g"]);
  });
});

describe("the article body", () => {
  const body = (html: string) => {
    const el = document.createElement("div");
    el.innerHTML = html;
    return el;
  };
  const groups = (terms: string[], over: Partial<Highlight> = {}) => compileHighlights([rule(terms, over)]);

  it("wraps matches in <mark class=kp-hl> and leaves the rest of the markup alone", () => {
    const el = body('<p>The <b>quick</b> fox and a quick <i>brown</i> dog</p>');
    expect(wrapMarks(el, groups(["quick"]))).toBe(2);
    expect(el.innerHTML).toBe('<p>The <b><mark class="kp-hl">quick</mark></b> fox and a <mark class="kp-hl">quick</mark> <i>brown</i> dog</p>');
  });

  it("skips text inside links, code, preformatted text, buttons, embeds and existing marks", () => {
    const el = body(
      '<p>fox <a href="https://x">fox link</a> <code>fox()</code></p><pre>fox</pre><button>fox</button><figure class="kp-embed">fox</figure><mark>fox</mark><p>fox</p>',
    );
    expect(wrapMarks(el, groups(["fox"]))).toBe(2);
    expect(el.querySelectorAll("a mark, code mark, pre mark, button mark, figure mark").length).toBe(0);
    expect(el.querySelectorAll("mark.kp-hl").length).toBe(2);
  });

  it("never turns a term into markup", () => {
    const el = body("<p>Try &lt;img src=x onerror=alert(1)&gt; today</p>");
    wrapMarks(el, groups(["<img src=x onerror=alert(1)>"], { whole_word: false }));
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector("mark")?.textContent).toBe("<img src=x onerror=alert(1)>");
    const script = body("<p>hello</p>");
    wrapMarks(script, groups(["hello"]));
    expect(script.querySelector("mark")?.innerHTML).toBe("hello");
  });

  it("caps the number of marks", () => {
    const el = body(`<p>${"word ".repeat(MAX_MARKS + 200)}</p>`);
    expect(wrapMarks(el, groups(["word"]))).toBe(MAX_MARKS);
    expect(el.querySelectorAll("mark").length).toBe(MAX_MARKS);
    const two = body(`<p>${"word ".repeat(50)}</p><p>${"word ".repeat(50)}</p>`);
    expect(wrapMarks(two, groups(["word"]), 60)).toBe(60);
  });

  it("clearMarks puts the text back exactly", () => {
    const html = "<p>The quick fox <em>and</em> the quick dog</p>";
    const el = body(html);
    wrapMarks(el, groups(["quick", "dog"]));
    expect(el.innerHTML).not.toBe(html);
    clearMarks(el);
    expect(el.innerHTML).toBe(html);
    expect(el.querySelector("p")?.childNodes.length).toBe(3); // text nodes merged again
  });

  it("does nothing without rules", () => {
    const el = body("<p>hello</p>");
    expect(wrapMarks(el, [])).toBe(0);
    expect(el.innerHTML).toBe("<p>hello</p>");
  });
});

describe("segments", () => {
  it("cuts the text at the ranges and caps the marks", () => {
    expect(segments("abcdef", [[1, 2], [4, 6]])).toEqual([
      { text: "a", mark: false },
      { text: "b", mark: true },
      { text: "cd", mark: false },
      { text: "ef", mark: true },
    ]);
    expect(segments("abcdef", [[0, 1], [2, 3], [4, 5]], 2).filter((s) => s.mark)).toHaveLength(2);
    expect(segments("abc", [])).toEqual([{ text: "abc", mark: false }]);
  });
});

// Parity with the Go engine (internal/filter/text.go, eval.go): the same text must be marked that the rule muted.
describe("parity with the rule engine (review finding 10)", () => {
  it("after a whole-word rejection it retries at the next character, like containsTerm", () => {
    // 'new york' is rejected (glued to the 'a'), but 'york' on its own is a whole word.
    expect(marked("anew york", ["new york", "york"])).toEqual(["york"]);
    // The longer term is rejected on its right edge; the shorter one at the same start still counts.
    expect(marked("new yorker", ["new york", "new"])).toEqual(["new"]);
    // A rejected first occurrence does not hide a later good one.
    expect(marked("catalog cat", ["cat"])).toEqual(["cat"]);
  });

  it("folds only non-spacing marks (Mn), as the engine does: a spacing mark keeps its letter a whole word", () => {
    // U+0915 + U+093E (Mc): the 'ka' letter followed by a vowel sign is one word; the bare letter must not match inside it.
    expect(marked("का", ["क"])).toEqual([]);
    expect(marked("का", ["क"], { whole_word: false })).toEqual(["क"]);
    // Mn accents still fold.
    expect(marked("Un café", ["cafe"])).toEqual(["café"]);
  });

  it("scans the first 32 KiB of an article body, like the engine does for text rules", () => {
    const g = compileHighlights([rule(["needle"])]);
    const body = (words: number) => {
      const p = document.createElement("div");
      p.innerHTML = `<p>${"x ".repeat(words)}needle</p>`;
      return p;
    };
    const past = body(16384); // 32768 bytes of filler, then the word
    expect(wrapMarks(past, g)).toBe(0);
    const inside = body(16384 - 10);
    expect(wrapMarks(inside, g)).toBe(1);
  });

  it("counts the 32 KiB in bytes across text nodes, so CJK filler uses it up three times as fast", () => {
    const g = compileHighlights([rule(["needle"])]);
    const div = document.createElement("div");
    div.innerHTML = `<p>${"中".repeat(11000)}</p><p>needle</p>`; // 33000 bytes before the word
    expect(wrapMarks(div, g)).toBe(0);
  });

  it("scans only 4 KiB of a title-like field", () => {
    const text = "x ".repeat(2100) + "needle"; // 4200 bytes before the word
    expect(marked(text, ["needle"])).toEqual([]);
    expect(marked("x ".repeat(2000) + "needle", ["needle"])).toEqual(["needle"]);
  });
});

