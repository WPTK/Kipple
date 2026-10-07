import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "./client";
import { keys } from "./queryKeys";
import type { StatsRange, StatsSummary } from "./types";

/** The Stats screen's data. Held for a minute; the previous range stays on screen while the next one loads. */
export function useStatsSummary(range: StatsRange, enabled = true) {
  return useQuery({
    queryKey: keys.stats(range),
    queryFn: ({ signal }) => api<StatsSummary>("/api/stats/summary", { params: { range }, signal }),
    enabled,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
    placeholderData: keepPreviousData,
  });
}

/**
 * Wrapped's data: the ordinary summary over one calendar year (to today for the current year). Its own key under the
 * ["stats"] prefix, so a delete or a new event refreshes it like the rest.
 */
export function useWrappedSummary(year: number, span: { from: string; to: string }, enabled = true) {
  return useQuery({
    queryKey: ["stats", "wrapped", year] as const,
    queryFn: ({ signal }) => api<StatsSummary>("/api/stats/summary", { params: { from: span.from, to: span.to }, signal }),
    enabled,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
    placeholderData: keepPreviousData,
  });
}

/** The summary of an explicit span, such as the period before the one on screen. Fetched only when asked for. */
export function useSpanSummary(span: { from: string; to: string } | null, enabled: boolean) {
  return useQuery({
    queryKey: ["stats", "span", span?.from, span?.to] as const,
    queryFn: ({ signal }) => api<StatsSummary>("/api/stats/summary", { params: { from: span!.from, to: span!.to }, signal }),
    enabled: enabled && span != null,
    staleTime: 60_000,
  });
}
