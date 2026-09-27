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
