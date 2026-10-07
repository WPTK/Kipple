import { describe, expect, it } from "vitest";
import { similarSeed, titleKeywords } from "./similar";
import { STOP_WORDS, STOP_WORD_LIST } from "./stopwords";

describe("titleKeywords", () => {
  it("keeps distinctive words in order, without common words, short words, numbers or repeats", () => {
    expect(titleKeywords("The 10 best Golang giveaways: win a Golang mug, says the company")).toEqual(["Golang", "giveaways", "company"]);
  });

  it("handles accents, apostrophes and other scripts", () => {
    expect(titleKeywords("Café Müller's 新iPhone発表")).toContain("Café");
    expect(titleKeywords("Golang's panic")).toEqual(["Golang's", "panic"]);
    expect(titleKeywords("")).toEqual([]);
  });

  it("caps the list", () => {
    expect(titleKeywords("alpha bravo charlie delta echoes foxtrot golfing hotel india juliet", 3)).toHaveLength(3);
  });
});

describe("similarSeed", () => {
  it("scopes the rule to the article's feed, starts with no terms and no name, and offers the keywords and the author", () => {
    const s = similarSeed({ title: "Sponsored giveaway: win a free laptop today", author: " Ada ", feed_id: "9", source: "Tech Daily" });
    expect(s.draft).toMatchObject({ scope: "feed", feed_id: "9", action: "mute", fields: ["title"], terms: [], name: "" });
    expect(s.keywords).toEqual(["Sponsored", "giveaway", "laptop"]);
    expect(s.author).toBe("Ada");
    expect(s.feedTitle).toBe("Tech Daily");
  });

  it("offers no generic headline words, and keeps the distinctive one", () => {
    const s = similarSeed({ title: "Best Apps Like Kalshi Alternatives You Should Try This Week", author: "", feed_id: "2", source: "Sports" });
    expect(s.keywords).toEqual(["Kalshi"]);
    expect(s.draft.terms).toEqual([]);
  });
  it("works for an article with no usable words", () => {
    const s = similarSeed({ title: "42", author: "", feed_id: "1", source: "X" });
    expect(s.draft.terms).toEqual([]);
    expect(s.author).toBeNull();
  });
});

describe("unspaced scripts and the term limit (review finding 5)", () => {
  it("cuts a spaceless Chinese title into short pieces instead of one giant keyword", () => {
    const title = "新闻联播今日要闻全球经济形势分析报告发布会";
    const k = titleKeywords(title);
    expect(k.length).toBeGreaterThan(1);
    expect(k.length).toBeLessThanOrEqual(3);
    for (const w of k) expect([...w].length).toBeLessThanOrEqual(3);
  });

  it("keeps a short CJK word whole and separates it from Latin text", () => {
    expect(titleKeywords("新iPhone発表")).toEqual(expect.arrayContaining(["iPhone", "発表"]));
  });

  it("no keyword exceeds the server's 100-character term limit, and the seeded name fits 200 bytes", () => {
    const long = "a".repeat(300);
    for (const w of titleKeywords(long)) expect([...w].length).toBeLessThanOrEqual(100);
    const thai = "ประกาศผลการแข่งขันฟุตบอลโลกรอบชิงชนะเลิศ";
    const s = similarSeed({ title: thai + " " + thai, author: "", feed_id: "1", source: "x" });
    expect(new TextEncoder().encode(s.draft.name).length).toBeLessThanOrEqual(200);
    for (const t of s.draft.terms) expect([...t].length).toBeLessThanOrEqual(100);
  });
});


describe("stop words", () => {
  it("is a long, lower-case list without repeats", () => {
    expect(STOP_WORD_LIST.length).toBeGreaterThan(300);
    expect(STOP_WORD_LIST.every((w) => w === w.toLowerCase() && w === w.trim() && w !== "")).toBe(true);
    expect(new Set(STOP_WORD_LIST).size).toBe(STOP_WORD_LIST.length);
    expect(STOP_WORDS.size).toBe(STOP_WORD_LIST.length);
  });

  it("is compared without regard to case", () => {
    expect(titleKeywords("ALTERNATIVES Guide REVIEWS Zebra")).toEqual(["Zebra"]);
  });
});