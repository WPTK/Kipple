import { beforeEach, describe, expect, it, vi } from "vitest";
import { LIST_PLACE_CAP, LIST_PLACE_KEY, listPlace, rememberListTop } from "./listPlace";

describe("list place on the device (#481)", () => {
  beforeEach(() => localStorage.clear());

  it("keeps the article at the top of each list, and forgets a list back at its top", () => {
    rememberListTop("unread", "42");
    rememberListTop("all|feed=1", "7");
    expect(listPlace("unread")).toBe("42");
    expect(listPlace("all|feed=1")).toBe("7");
    rememberListTop("unread", null);
    expect(listPlace("unread")).toBeNull();
    expect(listPlace("all|feed=1")).toBe("7");
  });

  it("keeps only the most recently scrolled lists", () => {
    vi.useFakeTimers();
    try {
      for (let i = 0; i <= LIST_PLACE_CAP; i++) {
        vi.setSystemTime(1_000_000 + i);
        rememberListTop(`list${i}`, String(i));
      }
    } finally {
      vi.useRealTimers();
    }
    expect(listPlace("list0")).toBeNull();
    expect(listPlace(`list${LIST_PLACE_CAP}`)).toBe(String(LIST_PLACE_CAP));
    expect(Object.keys(JSON.parse(localStorage.getItem(LIST_PLACE_KEY)!))).toHaveLength(LIST_PLACE_CAP);
  });

  it("reads a damaged entry as no place", () => {
    localStorage.setItem(LIST_PLACE_KEY, "not json");
    expect(listPlace("unread")).toBeNull();
    localStorage.setItem(LIST_PLACE_KEY, JSON.stringify({ unread: { id: 5 } }));
    expect(listPlace("unread")).toBeNull();
  });
});
