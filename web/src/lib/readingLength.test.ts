import { describe, expect, it } from "vitest";
import { itemsParams, parseScopeKey, scopeKey } from "@/api/queryKeys";
import { scopeBody } from "@/api/bulk";
import { BREAK_LENGTHS, READING_LENGTHS, READING_LENGTH_LABELS, READING_LENGTH_QUERY, isReadingLength } from "./readingLength";
import { scopeFromList, listTo } from "./routes";

// One table says what each length asks of the server; the query, the mark-read body and the section key all come from it.

describe("reading lengths", () => {
  it("the Coffee break's lengths partition the articles: a range for each, and the ones with no words", () => {
    expect(BREAK_LENGTHS.map((l) => READING_LENGTH_QUERY[l])).toEqual([{ max_minutes: 2 }, { min_minutes: 3, max_minutes: 7 }, { min_minutes: 8 }, { unknown: true }]);
    expect(BREAK_LENGTHS.map((l) => READING_LENGTH_LABELS[l])).toEqual(["Under 3 minutes", "3 to 7 minutes", "8 minutes and up", "Unknown length"]);
  });

  it("asks the list for each length", () => {
    const params = (length: (typeof BREAK_LENGTHS)[number]) => {
      const p = itemsParams({ view: "unread", folder: "9", length });
      return { min: p.min_minutes, max: p.max_minutes, unknown: (p as { unknown_length?: number }).unknown_length };
    };
    expect(params("under3")).toEqual({ min: undefined, max: 2, unknown: undefined });
    expect(params("3to7")).toEqual({ min: 3, max: 7, unknown: undefined });
    expect(params("8up")).toEqual({ min: 8, max: undefined, unknown: undefined });
    // Unknown is its own parameter, never a range (the server refuses both together).
    expect(params("unknown")).toEqual({ min: undefined, max: undefined, unknown: 1 });
  });

  it("marks only what the section holds", () => {
    expect(scopeBody({ view: "unread", folder: "9", length: "3to7" })).toEqual({ view: "unread", folder_id: "9", min_minutes: 3, max_minutes: 7 });
    expect(scopeBody({ view: "unread", length: "unknown" })).toEqual({ view: "unread", all: true, unknown_length: true });
  });

  it("a length is part of the section's key, so each section is its own list", () => {
    const keys = BREAK_LENGTHS.map((length) => scopeKey({ view: "unread", length }));
    expect(new Set(keys).size).toBe(4);
    expect(keys[3]).toBe("unread|len:unknown");
  });

  it("the filter menu's three lengths are the only ones an address or a key can carry", () => {
    for (const l of BREAK_LENGTHS) {
      expect(isReadingLength(l)).toBe(false);
      expect(scopeFromList("unread", new URLSearchParams(`len=${l}`))).toEqual({ view: "unread" });
      expect(parseScopeKey(`unread|len:${l}`)).toEqual({ view: "unread" });
    }
    for (const l of READING_LENGTHS) {
      expect(scopeFromList("unread", new URLSearchParams(`len=${l}`)).length).toBe(l);
      expect(parseScopeKey(`unread|len:${l}`).length).toBe(l);
    }
    expect(listTo({ view: "unread", length: "short" })).toEqual({ pathname: "/l/unread", search: "?len=short" });
  });
});
