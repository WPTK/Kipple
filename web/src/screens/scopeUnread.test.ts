import { describe, expect, it } from "vitest";
import type { Bootstrap } from "@/api/types";
import { scopeUnread } from "./ReaderRoute";

const boot = {
  counts: { unread: 7, starred: 1 },
  feeds: [{ id: "f1", unread: 0 }, { id: "f2", unread: 7 }],
  folders: [{ id: "d1", unread: 0 }],
} as unknown as Bootstrap;

describe("scopeUnread (#483: Mark all as read is off where nothing is unread)", () => {
  it("reads the live count of the whole library, a feed or a folder", () => {
    expect(scopeUnread({ view: "unread" }, boot)).toBe(7);
    expect(scopeUnread({ view: "all", feed: "f1" }, boot)).toBe(0);
    expect(scopeUnread({ view: "unread", feed: "f2" }, boot)).toBe(7);
    expect(scopeUnread({ view: "all", folder: "d1" }, boot)).toBe(0);
  });

  it("does not know where the counts do not say, so the action stays on", () => {
    expect(scopeUnread({ view: "all", q: "x" }, boot)).toBeNull();
    expect(scopeUnread({ view: "all", feed: "f1", length: "short" } as never, boot)).toBeNull();
    expect(scopeUnread({ view: "starred" }, boot)).toBeNull();
    expect(scopeUnread({ view: "muted" }, boot)).toBeNull();
    expect(scopeUnread({ view: "all", feed: "gone" }, boot)).toBeNull();
    expect(scopeUnread({ view: "all" }, undefined)).toBeNull();
  });
});
