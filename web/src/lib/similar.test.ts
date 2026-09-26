import { describe, expect, it } from "vitest";
import { similarSeed, titleKeywords } from "./similar";

describe("titleKeywords", () => {
  it("keeps distinctive words in order, without common words, short words, numbers or repeats", () => {
    expect(titleKeywords("The 10 best Golang giveaways: win a Golang mug, says the company")).toEqual(["best", "Golang", "giveaways", "company"]);
  });

  it("handles accents, apostrophes and other scripts", () => {
    expect(titleKeywords("Café Müller's 新iPhone発表")).toContain("Café");
    expect(titleKeywords("Don't panic")).toEqual(["Don't", "panic"]);
    expect(titleKeywords("")).toEqual([]);
  });

  it("caps the list", () => {
    expect(titleKeywords("alpha bravo charlie delta echoes foxtrot golfing hotel india juliet", 3)).toHaveLength(3);
  });
});

describe("similarSeed", () => {
  it("scopes the rule to the article's feed, starts with the first three keywords and offers the author", () => {
    const s = similarSeed({ title: "Sponsored giveaway: win a free laptop today", author: " Ada ", feed_id: "9", source: "Tech Daily" });
    expect(s.draft).toMatchObject({ scope: "feed", feed_id: "9", action: "mute", fields: ["title"], terms: ["Sponsored", "giveaway", "free"] });
    expect(s.draft.name).toBe("Mute: Sponsored, giveaway, free");
    expect(s.keywords.length).toBeGreaterThan(3);
    expect(s.author).toBe("Ada");
    expect(s.feedTitle).toBe("Tech Daily");
  });

  it("works for an article with no usable words", () => {
    const s = similarSeed({ title: "42", author: "", feed_id: "1", source: "X" });
    expect(s.draft.terms).toEqual([]);
    expect(s.author).toBeNull();
  });
});
