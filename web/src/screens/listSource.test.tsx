import { describe, expect, it } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { card, json, mockFetch, pageOf } from "@/test/mockApi";
import { NO_RANGE_IN_PAGES, useItemsSource } from "./listSource";

function source(paged: boolean) {
  mockFetch({ "GET /api/items": () => json(pageOf([card(1), card(2), card(3)], null)) });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return renderHook(() => useItemsSource({ view: "unread" }, paged), { wrapper });
}

describe("the server's list as a source", () => {
  it("mark above and below reach the whole loaded list on rows", async () => {
    const { result } = source(false);
    await waitFor(() => expect(result.current.cards).toHaveLength(3));
    const reach = result.current.rangeOf(result.current.cards[1]!);
    expect("cards" in reach && reach.cards.map((c) => c.id)).toEqual(result.current.cards.map((c) => c.id));
  });

  it("in a page layout they reach nothing, and the source says why", async () => {
    const { result } = source(true);
    await waitFor(() => expect(result.current.cards).toHaveLength(3));
    expect(result.current.rangeOf(result.current.cards[1]!)).toEqual({ why: NO_RANGE_IN_PAGES });
  });
});
