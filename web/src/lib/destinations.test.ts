import { describe, expect, it } from "vitest";
import { currentDestination, DESTINATIONS, LISTS } from "./destinations";

describe("currentDestination", () => {
  it.each([
    ["/l/unread", "", "unread"],
    ["/l", "", "unread"],
    ["/l/all", "", "all"],
    ["/l/starred", "", "starred"],
    ["/l/muted", "", "muted"],
    ["/l/unread", "?feed=3", "feeds"],
    ["/l/all", "?folder=2", "feeds"],
    ["/feeds", "", "feeds"],
    ["/feeds/index", "", "feedIndex"],
    ["/health", "", "feeds"],
    ["/search", "?q=x", "search"],
    ["/stats", "", "stats"],
    ["/stats/wrapped", "", "stats"],
    ["/settings/backup", "", "settings"],
    ["/i/12", "", null],
    ["/setup", "", null],
  ])("%s%s is %s", (path, search, want) => {
    expect(currentDestination(path, search)).toBe(want);
  });

  it("names the four lists once, in order, each with its own icon", () => {
    expect(LISTS.map((d) => d.label)).toEqual(["Unread", "All articles", "Starred", "Muted"]);
    expect(new Set(Object.values(DESTINATIONS).map((d) => d.icon)).size).toBe(Object.keys(DESTINATIONS).length);
  });
});
