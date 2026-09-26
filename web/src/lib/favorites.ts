import { useCallback, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage } from "@/api/client";
import { keys, useBootstrap } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
import { toast } from "@/shell/toasts";
import { cleanFavorites, updateDevicePrefs, useDevicePrefs, type Favorite } from "./devicePrefs";

// Sidebar favorites: folders and feeds pinned at the top. They are a library setting, so they follow you
// to every device: the server keeps them in `library.favorites` (PATCH /api/settings, at most 500 items).
// If a server does not know that setting (an older build answers 400 "unknown setting"), they are kept on this device instead
// and the UI still works; `mode` says which one is in use.

export const FAVORITES_KEY = "library.favorites";
export const MAX_FAVORITES = 500;

export const isFav = (list: readonly Favorite[], t: Favorite["t"], id: string): boolean => list.some((f) => f.t === t && f.id === id);

/** Toggle one entry, keeping the order of the others; a new favorite goes to the end. */
export function toggleFavorite(list: readonly Favorite[], t: Favorite["t"], id: string): Favorite[] {
  return isFav(list, t, id) ? list.filter((f) => !(f.t === t && f.id === id)) : [...list, { t, id }];
}

/** Move one favorite to a new position (drag, or the up and down buttons). */
export function moveFavorite(list: readonly Favorite[], from: number, to: number): Favorite[] {
  if (from < 0 || from >= list.length) return [...list];
  const out = [...list];
  const [x] = out.splice(from, 1);
  out.splice(Math.min(Math.max(to, 0), out.length), 0, x as Favorite);
  return out;
}

export interface FavoritesApi {
  /** Favorites whose folder or feed still exists, in the saved order. */
  favorites: Favorite[];
  mode: "server" | "device";
  has: (t: Favorite["t"], id: string) => boolean;
  toggle: (t: Favorite["t"], id: string) => void;
  set: (next: Favorite[]) => Promise<boolean>;
}

/** Where the server answered "not a setting I know": remembered for the session so we stop asking. */
let serverRejects = false;
/** Numbers the saves, so a slow older reply cannot overwrite a newer one. */
let sent = 0;
export function resetFavoritesMode(): void {
  serverRejects = false;
  sent = 0;
}

/**
 * The server saying it has no such setting (an older build): a 404, or a 400 whose issue for this key is "unknown
 * setting". Any other failure (a bad value, a busy server, being offline) is an error to show, not a reason to give
 * up on syncing for the rest of the session.
 */
export function isUnknownSetting(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false;
  if (e.status === 404) return true;
  if (e.status !== 400) return false;
  const issues = e.body?.issues;
  return Array.isArray(issues) && issues.some((i) => i && i.key === FAVORITES_KEY && /unknown setting/i.test(String(i.message)));
}

export function useFavorites(): FavoritesApi {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const raw = boot.data?.settings?.[FAVORITES_KEY];
  const serverList = Array.isArray(raw) ? cleanFavorites(raw) : null;
  const mode: "server" | "device" = serverList && !serverRejects ? "server" : "device";
  const stored = mode === "server" ? (serverList as Favorite[]) : dp.favoritesLocal;

  const alive = useMemo(() => {
    const folders = new Set((boot.data?.folders ?? []).map((f) => f.id));
    const feeds = new Set((boot.data?.feeds ?? []).filter((f) => !f.is_archive).map((f) => f.id));
    return stored.filter((f) => (f.t === "folder" ? folders.has(f.id) : feeds.has(f.id)));
  }, [stored, boot.data?.folders, boot.data?.feeds]);

  const set = useCallback(
    async (next: Favorite[]): Promise<boolean> => {
      const list = cleanFavorites(next);
      if (list.length > MAX_FAVORITES) {
        toast(`You can pin up to ${MAX_FAVORITES} favorites.`, "error");
        return false;
      }
      if (mode === "device") {
        updateDevicePrefs({ favoritesLocal: list });
        return true;
      }
      const readFavs = () => qc.getQueryData<Bootstrap>(keys.bootstrap)?.settings?.[FAVORITES_KEY];
      const patchBoot = (value: unknown) =>
        qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => (old ? { ...old, settings: { ...old.settings, [FAVORITES_KEY]: value } } : old));
      const before = readFavs();
      const mine = ++sent;
      patchBoot(list);
      try {
        const res = await api<{ values: Record<string, unknown> }>("/api/settings", { method: "PATCH", body: { [FAVORITES_KEY]: list } });
        // Replies can arrive out of order: only the newest request may write what the server says.
        if (mine === sent) patchBoot(res.values?.[FAVORITES_KEY] ?? list);
        return true;
      } catch (e) {
        // Undo only the favorites slice, and only if nothing newer has been applied since: the rest of the
        // bootstrap (counts, feeds) may have changed while this was in flight.
        const unknown = isUnknownSetting(e);
        if (mine === sent) patchBoot(before);
        if (unknown) {
          // An older server that has no such setting: keep them on this device from now on.
          serverRejects = true;
          updateDevicePrefs({ favoritesLocal: list });
          return true;
        }
        toast(errorMessage(e), "error");
        return false;
      }
    },
    [mode, qc],
  );

  const has = useCallback((t: Favorite["t"], id: string) => isFav(alive, t, id), [alive]);
  const toggle = useCallback((t: Favorite["t"], id: string) => void set(toggleFavorite(stored, t, id)), [set, stored]);
  return { favorites: alive, mode, has, toggle, set };
}
