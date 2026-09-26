import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

// The image cache (docs/design.md 7.4). Counters are since the server process started.

export interface ImgCacheStats {
  enabled: boolean;
  mode: string;
  cache_mb: number;
  max_bytes: number;
  used_bytes: number;
  entries: number;
  neg_entries: number;
  thumbnails?: number;
  hits: number;
  misses: number;
  evictions: number;
  failures: number;
  /** Unix seconds: when the counters started (the server's start). */
  since: number;
  oldest_access_at: number | null;
  disk_free_bytes: number;
  disk_floor_bytes: number;
  low_disk: boolean;
}

export const imgCacheKey = ["imgcache"] as const;

/** Stats, fetched afresh every time the section opens (the cache changes under it: fills, evictions, sweeps). */
export function useImgCache() {
  return useQuery({
    queryKey: imgCacheKey,
    queryFn: ({ signal }) => api<ImgCacheStats>("/api/imgcache", { signal }),
    staleTime: 0,
    refetchOnMount: "always",
    gcTime: 0,
  });
}

export const clearImgCache = () => api<{ cleared: number }>("/api/imgcache/clear", { method: "POST" });

/** Share of image requests answered from disk since the counters started, or null before any request. */
export function hitRate(s: Pick<ImgCacheStats, "hits" | "misses">): number | null {
  const n = s.hits + s.misses;
  return n === 0 ? null : s.hits / n;
}
