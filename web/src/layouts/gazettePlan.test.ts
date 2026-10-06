import { describe, expect, it } from "vitest";
import {
  FRONT_BRIEFS,
  INNER_PAGE_SIZE,
  LEAD_WINDOW,
  planGazette,
  STORIES_PER_COLUMN,
  type GazetteInput,
  type PagePlan,
  type PlanCard,
} from "./gazettePlan";

const IMG = "https://example.com/i.jpg";

const folders = [
  { id: "tech", name: "Tech", parent_id: null },
  { id: "apple", name: "Apple", parent_id: "tech" },
  { id: "news", name: "News", parent_id: null },
];
const feeds = [
  { id: "f1", title: "Daily Wire", folder_id: "news" },
  { id: "f2", title: "Chip Weekly", folder_id: "tech" },
  { id: "f3", title: "Orchard", folder_id: "apple" },
  { id: "f4", title: "Other Press", folder_id: "news" },
];

/** Article n: larger n is older. */
function art(n: number, over: Partial<PlanCard> = {}): PlanCard {
  return { id: `a${String(n).padStart(4, "0")}`, feed_id: "f1", title: `Story ${n}`, image: null, sort_at: 1_000_000 - n * 60, ...over };
}
const many = (count: number, over: (n: number) => Partial<PlanCard> = () => ({})) => Array.from({ length: count }, (_, i) => art(i + 1, over(i + 1)));

function input(articles: PlanCard[], over: Partial<GazetteInput> = {}): GazetteInput {
  return { articles, feeds, folders, favorites: [], scope: {}, screen: "wide", more: false, ...over };
}
const ids = (p: PagePlan) => p.blocks.flatMap((b) => b.slots.map((s) => s.id));
const allIds = (pages: PagePlan[]) => pages.flatMap(ids);
const kinds = (p: PagePlan) => p.blocks.flatMap((b) => b.slots.map((s) => s.kind));
const fav = (t: "feed" | "folder", id: string) => ({ t, id });

describe("front-page type", () => {
  it("co-leads: two pinned feeds both have a picture; the newest of each leads", () => {
    const list = many(30, (n) => (n === 3 ? { feed_id: "f1", image: IMG } : n === 5 ? { feed_id: "f2", image: IMG } : n === 2 ? { feed_id: "f4", image: IMG } : {}));
    const plan = planGazette(input(list, { favorites: [fav("feed", "f1"), fav("feed", "f2")] }));
    const front = plan.pages[0]!;
    expect(front.type).toBe("co-leads");
    expect(front.blocks[0]!.slots.map((s) => [s.kind, s.id])).toEqual([
      ["lead", "a0003"],
      ["co-lead", "a0005"],
    ]);
  });

  it("lead with image: newest picture from a pinned feed, with stories beside it", () => {
    const list = many(30, (n) => (n === 4 ? { feed_id: "f2", image: IMG } : n === 1 ? { image: IMG } : {}));
    const front = planGazette(input(list, { favorites: [fav("feed", "f2")] })).pages[0]!;
    expect(front.type).toBe("lead-image");
    const first = front.blocks[0]!;
    expect(first.slots[0]).toEqual({ kind: "lead", id: "a0004", column: 0, picture: true });
    expect(first.slots.slice(1).map((s) => [s.id, s.column])).toEqual([
      ["a0001", 1],
      ["a0002", 1],
      ["a0003", 1],
    ]);
  });

  it("a favorite folder pins every feed inside it, subfolders included", () => {
    const list = many(20, (n) => (n === 6 ? { feed_id: "f3", image: IMG } : {}));
    const front = planGazette(input(list, { favorites: [fav("folder", "tech")] })).pages[0]!;
    expect(front.type).toBe("lead-image");
    expect(front.blocks[0]!.slots[0]!.id).toBe("a0006");
  });

  it("two pictures from the same pinned feed are not co-leads", () => {
    const list = many(20, (n) => (n === 2 || n === 3 ? { feed_id: "f2", image: IMG } : {}));
    expect(planGazette(input(list, { favorites: [fav("feed", "f2")] })).pages[0]!.type).toBe("lead-image");
  });

  it("a picture from an unpinned feed never leads, even when it is the newest", () => {
    const list = many(20, (n) => (n === 1 ? { image: IMG } : {}));
    const front = planGazette(input(list, { favorites: [fav("feed", "f2")] })).pages[0]!;
    expect(front.type).not.toBe("lead-image");
    expect(front.type).not.toBe("co-leads");
  });

  it("a pinned picture outside the first 100 articles does not lead", () => {
    const list = many(150, (n) => (n === LEAD_WINDOW + 1 ? { feed_id: "f2", image: IMG } : {}));
    expect(planGazette(input(list, { favorites: [fav("feed", "f2")] })).pages[0]!.type).not.toBe("lead-image");
    const inside = many(150, (n) => (n === LEAD_WINDOW ? { feed_id: "f2", image: IMG } : {}));
    expect(planGazette(input(inside, { favorites: [fav("feed", "f2")] })).pages[0]!.type).toBe("lead-image");
  });

  it("big headline: a pinned feed has news but no picture, so its newest story leads", () => {
    const list = many(20, (n) => (n === 7 ? { feed_id: "f2" } : n === 1 ? { image: IMG } : {}));
    const front = planGazette(input(list, { favorites: [fav("feed", "f2")] })).pages[0]!;
    expect(front.type).toBe("big-headline");
    expect(front.blocks[0]!.slots).toEqual([{ kind: "lead", id: "a0007", column: 0, picture: false }]);
  });

  it("big headline: with nothing from a pinned feed the longest recent headline leads, newest on a tie", () => {
    const list = many(10, (n) => ({ image: n === 9 ? IMG : null, title: n === 4 || n === 6 ? "A rather long headline indeed" : `Story ${n}` }));
    const front = planGazette(input(list)).pages[0]!;
    expect(front.type).toBe("big-headline");
    expect(front.blocks[0]!.slots[0]!.id).toBe("a0004");
  });

  it("quiet day: fewer than six stories, two columns, no briefs", () => {
    const front = planGazette(input(many(5))).pages[0]!;
    expect(front.type).toBe("quiet");
    expect(kinds(front)).not.toContain("brief");
    expect(front.blocks[1]!.columns).toBe(2);
    expect(ids(front)).toHaveLength(5);
  });

  it("busy day: a photo row of three, then four columns", () => {
    const list = many(60, (n) => (n % 5 === 0 ? { image: IMG } : {}));
    const front = planGazette(input(list)).pages[0]!;
    expect(front.type).toBe("busy");
    const photos = front.blocks.find((b) => b.slots[0]!.kind === "photo")!;
    expect(photos.slots.map((s) => s.id)).toEqual(["a0005", "a0010", "a0015"]);
    const stories = front.blocks.find((b) => b.slots[0]!.kind === "story")!;
    expect(stories.columns).toBe(4);
    expect(stories.slots).toHaveLength(4 * STORIES_PER_COLUMN);
  });

  it("text-only day: no picture anywhere", () => {
    const front = planGazette(input(many(40))).pages[0]!;
    expect(front.type).toBe("text-only");
    expect(kinds(front)).not.toContain("photo");
  });

  it("a single feed or folder has no pin: the newest picture in the list leads", () => {
    const list = many(20, (n) => (n === 8 ? { image: IMG } : n === 3 ? { image: IMG, feed_id: "f4" } : {}));
    // Favorites are ignored inside a list.
    const front = planGazette(input(list, { scope: { feed: "f1" }, favorites: [fav("feed", "f4")] })).pages[0]!;
    expect(front.type).toBe("lead-image");
    expect(front.blocks[0]!.slots[0]!.id).toBe("a0003");
  });

  it("a list never has co-leads", () => {
    const list = many(20, (n) => (n === 2 ? { image: IMG } : n === 3 ? { image: IMG, feed_id: "f4" } : {}));
    expect(planGazette(input(list, { scope: { folder: "news" } })).pages[0]!.type).toBe("lead-image");
  });
});

describe("front-page layout", () => {
  it("columns grow with the story count, between two and four, and never exceed their capacity", () => {
    const columnsFor = (count: number) => {
      const front = planGazette(input(many(count, (n) => (n === 1 ? { image: IMG } : {})), { favorites: [fav("feed", "f1")] })).pages[0]!;
      return front.blocks.find((b) => b.slots[0]!.kind === "story" && b.slots.length > 3)?.columns;
    };
    expect(columnsFor(11)).toBe(2);
    expect(columnsFor(16)).toBe(3);
    expect(columnsFor(40)).toBe(4);
  });

  it("stories that do not fit become briefs, capped at ten, and the rest go to inner pages", () => {
    const front = planGazette(input(many(120, (n) => (n === 1 ? { image: IMG } : {})), { favorites: [fav("feed", "f1")] })).pages[0]!;
    expect(kinds(front).filter((k) => k === "brief")).toHaveLength(FRONT_BRIEFS);
    expect(kinds(front).filter((k) => k === "story")).toHaveLength(3 + 4 * STORIES_PER_COLUMN);
  });

  it("reading order is column by column: columns never go backwards", () => {
    const front = planGazette(input(many(60, (n) => (n % 5 === 0 ? { image: IMG } : {})))).pages[0]!;
    for (const b of front.blocks) {
      const cols = b.slots.map((s) => s.column);
      expect(cols).toEqual([...cols].sort((a, c) => a - c));
    }
  });

  it("a story is on exactly one page, once", () => {
    const plan = planGazette(input(many(150, (n) => (n % 7 === 0 ? { image: IMG } : {}))));
    const all = allIds(plan.pages);
    expect(new Set(all).size).toBe(all.length);
    expect(all).toHaveLength(150);
  });
});

describe("edge counts", () => {
  it("0 articles: no pages", () => {
    expect(planGazette(input([]))).toEqual({ pages: [], complete: true });
    expect(planGazette(input([], { more: true }))).toEqual({ pages: [], complete: false });
  });

  it("1 article: a front page with just the lead and nothing else", () => {
    const plan = planGazette(input([art(1)]));
    expect(plan.pages).toHaveLength(1);
    expect(plan.pages[0]!.type).toBe("quiet");
    expect(plan.pages[0]!.blocks).toEqual([{ title: null, columns: 1, slots: [{ kind: "lead", id: "a0001", column: 0, picture: false }] }]);
  });

  it("1 article with a picture and no pin in a list: a lead with no stories beside it", () => {
    const front = planGazette(input([art(1, { image: IMG })], { scope: { feed: "f1" } })).pages[0]!;
    expect(front.type).toBe("lead-image");
    expect(ids(front)).toEqual(["a0001"]);
  });

  it("a few articles: one page, no inner pages, and it ends the paper", () => {
    const plan = planGazette(input(many(4)));
    expect(plan.pages).toHaveLength(1);
    expect(plan.complete).toBe(true);
    expect(ids(plan.pages[0]!)).toHaveLength(4);
  });

  it("exactly the lead window", () => {
    const plan = planGazette(input(many(LEAD_WINDOW)));
    expect(allIds(plan.pages)).toHaveLength(LEAD_WINDOW);
  });

  it("an unknown feed or folder does not throw", () => {
    const list = many(40, (n) => ({ feed_id: n % 2 ? "gone" : "f2" }));
    expect(() => planGazette(input(list, { favorites: [fav("folder", "gone")] }))).not.toThrow();
  });
});

describe("inner pages", () => {
  it("full chunks of articles, numbered from 2, the last one briefs only when the paper is complete", () => {
    const plan = planGazette(input(many(140, (n) => (n === 1 ? { image: IMG } : {})), { favorites: [fav("feed", "f1")] }));
    expect(plan.pages.map((p) => p.number)).toEqual(plan.pages.map((_, i) => i + 1));
    expect(plan.pages.map((p) => p.kind)).toEqual(["front", "inner", "inner", "inner", "inner", "briefs"]); // 140 less 30 on the front is 110: four chunks and 14 left
    const last = plan.pages.at(-1)!;
    expect(new Set(kinds(last))).toEqual(new Set(["brief"]));
    expect(plan.complete).toBe(true);
    for (const p of plan.pages.slice(1, -1)) expect(ids(p)).toHaveLength(INNER_PAGE_SIZE);
  });

  it("while the server has more, a trailing partial page is withheld and there is no briefs page", () => {
    const list = many(150, (n) => (n === 1 ? { image: IMG } : {}));
    const plan = planGazette(input(list, { more: true, favorites: [fav("feed", "f1")] }));
    expect(plan.complete).toBe(false);
    expect(plan.pages.map((p) => p.kind)).toEqual(["front", "inner", "inner", "inner", "inner", "inner"]);
    expect(plan.pages.every((p) => p.kind !== "briefs")).toBe(true);
    for (const p of plan.pages.slice(1)) expect(ids(p)).toHaveLength(INNER_PAGE_SIZE);
  });

  it("a partial trailing chunk appears only once the paper is complete", () => {
    const list = many(140, (n) => (n === 1 ? { image: IMG } : {}));
    const opts = { favorites: [fav("feed", "f1")] };
    const waiting = planGazette(input(list, { ...opts, more: true }));
    const done = planGazette(input(list, opts));
    expect(waiting.pages).toHaveLength(5);
    expect(done.pages).toHaveLength(6);
    expect(done.pages.slice(0, 5)).toEqual(waiting.pages);
    expect(done.pages[5]!.kind).toBe("briefs");
    expect(ids(done.pages[5]!)).toHaveLength(14);
  });

  it("a full last chunk stays an inner page when the list completes; no page already shown becomes briefs", () => {
    const list = many(150, (n) => (n === 1 ? { image: IMG } : {})); // 150 less 30 on the front is 120: five full chunks
    const opts = { favorites: [fav("feed", "f1")] };
    const waiting = planGazette(input(list, { ...opts, more: true }));
    const done = planGazette(input(list, opts));
    expect(done.pages.map((p) => p.kind)).toEqual(["front", "inner", "inner", "inner", "inner", "inner"]);
    expect(done.pages).toEqual(waiting.pages);
  });

  it("returns no pages until the lead window is loaded or the list is complete", () => {
    expect(planGazette(input(many(LEAD_WINDOW - 1), { more: true })).pages).toEqual([]);
    expect(planGazette(input(many(LEAD_WINDOW), { more: true })).pages.length).toBeGreaterThan(0);
    expect(planGazette(input(many(LEAD_WINDOW - 1))).pages.length).toBeGreaterThan(0);
  });

  it("equal times order by id descending, numerically, like the server", () => {
    const same = (id: string): PlanCard => ({ id, feed_id: "f1", title: "t", image: null, sort_at: 5 });
    const plan = planGazette(input([same("9"), same("100"), same("10")]));
    expect(ids(plan.pages[0]!)).toEqual(["100", "10", "9"]);
  });

  it("a lead that does not come from a pinned picture never draws a picture", () => {
    const pinned = { favorites: [fav("feed", "f2")] };
    const lead = (list: PlanCard[]) => planGazette(input(list, pinned)).pages[0]!.blocks[0]!.slots[0]!;
    expect(lead(many(20, (n) => (n === 1 ? { image: IMG } : {}))).picture).toBe(false); // big headline
    expect(lead(many(5, (n) => (n === 1 ? { image: IMG } : {}))).picture).toBe(false); // quiet
    expect(lead(many(40, (n) => (n <= 5 ? { image: IMG } : {}))).picture).toBe(false); // busy
    expect(lead(many(20, (n) => (n === 3 ? { feed_id: "f2", image: IMG } : {}))).picture).toBe(true);
  });

  it("busy needs three pictures besides the lead for the photo row", () => {
    const withPictures = (count: number) => planGazette(input(many(40, (n) => (n <= count ? { image: IMG } : {})))).pages[0]!;
    expect(withPictures(3).type).toBe("big-headline"); // the lead takes one, only two are left
    const four = withPictures(4);
    expect(four.type).toBe("busy");
    expect(four.blocks.find((b) => b.slots[0]!.kind === "photo")!.slots).toHaveLength(3);
  });

  it("a block never has an empty column when it holds fewer items than columns", () => {
    const front = planGazette(input(many(6, () => ({ image: IMG })))).pages[0]!; // busy: lead, three photos, two stories
    expect(front.type).toBe("busy");
    for (const b of front.blocks) expect(new Set(b.slots.map((s) => s.column)).size).toBe(b.columns);
    const stories = front.blocks.find((b) => b.slots[0]!.kind === "story")!;
    expect(stories.columns).toBe(2);
  });

  it("loading more never changes a page already planned", () => {
    const base = many(400, (n) => (n % 9 === 0 ? { image: IMG, feed_id: n % 2 ? "f2" : "f3" } : { feed_id: n % 3 ? "f1" : "f4" }));
    const opts = { favorites: [fav("feed", "f2")], more: true };
    const first = planGazette(input(base.slice(0, 200), opts));
    const second = planGazette(input(base.slice(0, 300), opts));
    const third = planGazette(input(base, { ...opts, more: false }));
    expect(second.pages.slice(0, first.pages.length)).toEqual(first.pages);
    expect(third.pages.slice(0, second.pages.length)).toEqual(second.pages);
  });

  it("everything groups by top-level folder, in the order of each folder's newest story", () => {
    const list = many(24, (n) => ({ feed_id: n % 6 === 0 ? "f3" : n % 3 === 0 ? "f2" : "f1" }));
    // 24 articles, none pinned: the front page takes some, so use a large list to force an inner page.
    const plan = planGazette(input([...list, ...many(60).map((a) => ({ ...a, id: `z${a.id}`, sort_at: a.sort_at - 10_000 }))]));
    const inner = plan.pages.find((p) => p.kind === "inner")!;
    const titles = inner.blocks.map((b) => b.title);
    expect(new Set(titles).size).toBe(titles.length);
    for (const t of titles) expect(["Tech", "News"]).toContain(t);
  });

  it("a parent folder groups by subfolder, and feeds directly inside it by feed name", () => {
    const list = many(120, (n) => ({ feed_id: n % 3 === 0 ? "f3" : "f2" }));
    const plan = planGazette(input(list, { scope: { folder: "tech" } }));
    const titles = plan.pages.filter((p) => p.kind === "inner").flatMap((p) => p.blocks.map((b) => b.title));
    expect(new Set(titles)).toEqual(new Set(["Apple", "Chip Weekly"]));
  });

  it("a folder with no subfolders groups by feed name", () => {
    const list = many(120, (n) => ({ feed_id: n % 2 ? "f1" : "f4" }));
    const plan = planGazette(input(list, { scope: { folder: "news" } }));
    const titles = plan.pages.filter((p) => p.kind === "inner").flatMap((p) => p.blocks.map((b) => b.title));
    expect(new Set(titles)).toEqual(new Set(["Daily Wire", "Other Press"]));
  });

  it("a single feed is one section", () => {
    const plan = planGazette(input(many(120), { scope: { feed: "f1" } }));
    const titles = plan.pages.filter((p) => p.kind === "inner").flatMap((p) => p.blocks.map((b) => b.title));
    expect(new Set(titles)).toEqual(new Set(["Daily Wire"]));
  });

  it("at most one picture feature per section, and it comes first in its section", () => {
    const list = many(200, (n) => ({ image: n > 40 ? IMG : null, feed_id: n % 2 ? "f1" : "f2" }));
    const plan = planGazette(input(list));
    for (const p of plan.pages.filter((x) => x.kind === "inner")) {
      for (const b of p.blocks) {
        const photos = b.slots.filter((s) => s.kind === "photo");
        expect(photos.length).toBeLessThanOrEqual(1);
        if (photos.length) expect(b.slots[0]).toBe(photos[0]);
      }
    }
  });
});

describe("phone", () => {
  const list = many(150, (n) => (n === 2 ? { feed_id: "f2", image: IMG } : n % 11 === 0 ? { image: IMG } : {}));
  const opts = { favorites: [fav("feed", "f2")] };

  it("is one column everywhere", () => {
    const plan = planGazette(input(list, { ...opts, screen: "phone" }));
    for (const p of plan.pages) for (const b of p.blocks) {
      expect(b.columns).toBe(1);
      expect(b.slots.every((s) => s.column === 0)).toBe(true);
    }
  });

  it("keeps the same front-page type and reading order as a wide screen", () => {
    const wide = planGazette(input(list, opts));
    const phone = planGazette(input(list, { ...opts, screen: "phone" }));
    expect(phone.pages.map((p) => p.type)).toEqual(wide.pages.map((p) => p.type));
    expect(allIds(phone.pages)).toEqual(allIds(wide.pages));
  });
});

describe("determinism", () => {
  const list = many(260, (n) => ({
    feed_id: ["f1", "f2", "f3", "f4"][n % 4]!,
    image: n % 6 === 0 ? IMG : null,
    // Several articles share a time, so the id tie-break matters.
    sort_at: 1_000_000 - Math.floor(n / 3) * 60,
  }));
  const opts = { favorites: [fav("feed", "f2"), fav("folder", "news")] };

  it("gives the same pages for the same input, and does not change the input", () => {
    const copy = structuredClone(list);
    const a = planGazette(input(list, opts));
    expect(planGazette(input(list, opts))).toEqual(a);
    expect(list).toEqual(copy);
  });

  it("does not depend on the order of articles, feeds, folders or favorites", () => {
    const a = planGazette(input(list, opts));
    const shuffled = list.map((x, i) => [x, (i * 7919) % 263] as const).sort((p, q) => p[1] - q[1]).map((p) => p[0]);
    const b = planGazette(
      input(shuffled, { favorites: [...opts.favorites].reverse(), feeds: [...feeds].reverse(), folders: [...folders].reverse() }),
    );
    expect(b).toEqual(a);
  });

  it("lists every article newest first within its page's sections", () => {
    const plan = planGazette(input(list, opts));
    const time = new Map(list.map((x) => [x.id, x.sort_at]));
    for (const p of plan.pages.filter((x) => x.kind === "briefs")) {
      const t = ids(p).map((id) => time.get(id)!);
      expect(t).toEqual([...t].sort((x, y) => y - x));
    }
  });
});
