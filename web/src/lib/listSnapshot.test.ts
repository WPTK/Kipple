import { describe, expect, it } from "vitest";
import type { InfiniteData } from "@tanstack/react-query";
import type { ItemsPage } from "@/api/types";
import { card, pageOf } from "@/test/mockApi";
import { listCards } from "./listSnapshot";

const at = (id: number) => card(0, { id: String(id) });
const data = (...pages: ItemsPage[]): InfiniteData<ItemsPage> => ({ pages, pageParams: pages.map(() => undefined) });
const ids = (d: InfiniteData<ItemsPage> | undefined) => listCards(d).map((c) => c.id);

describe("a list read a page at a time stays the list its first page opened", () => {
  it("leaves out of a later page an article that arrived after the first page", () => {
    // 108 was back-dated into the second page after the list opened; 107 is the newest the first page knew of.
    const d = data(pageOf([at(107), at(105)], "c1", "107"), pageOf([at(104), at(108), at(100)], null));
    expect(ids(d)).toEqual(["107", "105", "104", "100"]);
  });

  it("compares ids as numbers, not as text", () => {
    const d = data(pageOf([at(99)], "c1", "99"), pageOf([at(98), at(100)], null));
    expect(ids(d)).toEqual(["99", "98"]);
  });

  it("keeps the first page whole, whatever it holds", () => {
    const d = data(pageOf([at(110), at(105)], "c1", "107"), pageOf([at(104)], null));
    expect(ids(d)).toEqual(["110", "105", "104"]);
  });

  it("keeps everything when the first page has no as_of", () => {
    const d = data(pageOf([at(107)], "c1"), pageOf([at(108)], null));
    expect(ids(d)).toEqual(["107", "108"]);
  });

  it("is empty with no data", () => {
    expect(listCards(undefined)).toEqual([]);
  });
});
