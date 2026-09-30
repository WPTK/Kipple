import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

/** GET /api/about: what this server is. No username, no address, no path (docs/design.md, About). */
export interface About {
  version: string;
  commit: string;
  build_date: string;
  go_version: string;
  os_arch: string;
  schema_version: number;
  schema_latest: number;
  sqlite_version: string;
  started_at: string;
  uptime_s: number;
  data_dir_writable: boolean;
  tz: string;
  auth_mode: "password" | "access" | "open";
  access_enabled: boolean;
  public_url_set: boolean;
  web_build: string;
  /** The release that last opened the database before this run, when it recorded one. */
  last_version?: string;
}

export const aboutKey = ["about"] as const;

export function useAbout() {
  return useQuery({
    queryKey: aboutKey,
    queryFn: ({ signal }) => api<About>("/api/about", { signal }),
    staleTime: 0,
    gcTime: 0,
  });
}
