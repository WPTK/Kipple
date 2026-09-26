import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { DeviceView } from "./types";

// Device profiles (docs/design.md 7.1c). The settings themselves sync through lib/deviceSync.ts; this file
// is the device list and the name.

export interface DeviceRow {
  id: string;
  name: string;
  current: boolean;
  user_agent: string;
  client: string;
  created_at: number;
  last_seen_at: number;
  /** How many settings this device has of its own (overrides of the defaults). */
  overrides: number;
}

export const devicesKey = ["devices"] as const;

export function useDevices() {
  return useQuery({
    queryKey: devicesKey,
    queryFn: ({ signal }) => api<{ devices: DeviceRow[] }>("/api/devices", { signal }).then((r) => r.devices),
    staleTime: 0,
  });
}

export const renameDevice = (name: string) => api<DeviceView>("/api/device/name", { method: "PUT", body: { name } });
export const deleteDevice = (id: string) => api(`/api/devices/${encodeURIComponent(id)}`, { method: "DELETE" });

/** "Safari on iPhone", "Chrome on Windows": a short description of a user agent, for a device with no name. */
export function describeUserAgent(ua: string, client?: string): string {
  const has = (re: RegExp) => re.test(ua);
  const browser = has(/Edg\//) ? "Edge" : has(/OPR\/|Opera/) ? "Opera" : has(/Firefox\//) ? "Firefox" : has(/CriOS\/|Chrome\//) ? "Chrome" : has(/Safari\//) ? "Safari" : "";
  const os = has(/iPhone/) ? "iPhone" : has(/iPad/) ? "iPad" : has(/Android/) ? "Android" : has(/Windows/) ? "Windows" : has(/Mac OS X|Macintosh/) ? "Mac" : has(/Linux/) ? "Linux" : "";
  const base = browser && os ? `${browser} on ${os}` : browser || os || "Unknown device";
  return client === "pwa" ? `${base} (installed app)` : base;
}
