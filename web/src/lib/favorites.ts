import { useCallback, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, errorMessage } from "@/api/client";
import { keys, useBootstrap } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
import { toast } from "@/shell/toasts";
import { arrayMove } from "./dnd";
import { cleanFavorites, type Favorite } from "./devicePrefs";
import { visibleFeeds } from "./visibleFeeds";

// Sidebar favorites: folders and feeds pinned at the top. They are a library setting, so they follow you
// to every device: the server keeps them in `library.favorites` (PATCH /api/settings, at most 500 items).

export const FAVORITES_KEY = "library.favorites";
export const MAX_FAVORITES = 500;

export const isFav = (list: readonly Favorite[], t: Favorite["t"], id: string): boolean => list.some((f) => f.t === t && f.id === id);

/** Toggle one entry, keeping the order of the others; a new favorite goes to the end. */
export function toggleFavorite(list: readonly Favorite[], t: Favorite["t"], id: string): Favorite[] {
  return isFav(list, t, id) ? list.filter((f) => !(f.t === t && f.id === id)) : [...list, { t, id }];
}

/** Move one favorite to a new position (drag, or the up and down buttons). */
export function moveFavorite(list: readonly Favorite[], from: number, to: number): Favorite[] {
  return arrayMove(list, from, to);
}

export interface FavoritesApi {
  /** Favorites whose folder or feed still exists, in the saved order. */
  favorites: Favorite[];
  has: (t: Favorite["t"], id: string) => boolean;
  toggle: (t: Favorite["t"], id: string) => void;
  set: (next: Favorite[]) => Promise<boolean>;
}

/** Numbers the saves, so a slow older reply cannot overwrite a newer one. */
let sent = 0;

export function useFavorites(): FavoritesApi {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const raw = boot.data?.settings?.[FAVORITES_KEY];
  const stored = useMemo(() => (Array.isArray(raw) ? cleanFavorites(raw) : []), [raw]);

  const alive = useMemo(() => {
    const folders = new Set((boot.data?.folders ?? []).map((f) => f.id));
    const feeds = new Set(visibleFeeds(boot.data?.feeds).map((f) => f.id));
    return stored.filter((f) => (f.t === "folder" ? folders.has(f.id) : feeds.has(f.id)));
  }, [stored, boot.data?.folders, boot.data?.feeds]);

  const set = useCallback(
    async (next: Favorite[]): Promise<boolean> => {
      // Count before cleaning: cleanFavorites truncates at 500, which would hide the 501st.
      if (cleanFavorites(next, Infinity).length > MAX_FAVORITES) {
        toast(`You can pin up to ${MAX_FAVORITES} favorites.`, "error");
        return false;
      }
      const list = cleanFavorites(next);
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
        if (mine === sent) patchBoot(before);
        toast(errorMessage(e), "error");
        return false;
      }
    },
    [qc],
  );

  const has = useCallback((t: Favorite["t"], id: string) => isFav(alive, t, id), [alive]);
  const toggle = useCallback((t: Favorite["t"], id: string) => void set(toggleFavorite(stored, t, id)), [set, stored]);
  return { favorites: alive, has, toggle, set };
}
