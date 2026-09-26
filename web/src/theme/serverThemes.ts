import { useSettings } from "@/api/admin";
import { SCHEMES, type Scheme } from "./schemes";

/**
 * The schemes to offer: the server's list is authoritative for which ids exist (it validates every write),
 * the local schemes file supplies their names and colors. Until the server's list is known, or if it names
 * none this build has, every local scheme is offered. An id the server has that this build has no colors for
 * is not shown (it could not be drawn).
 */
export function pickSchemes(serverIds: readonly string[] | null | undefined): Scheme[] {
  if (!serverIds || serverIds.length === 0) return SCHEMES;
  const ids = new Set(serverIds);
  const list = SCHEMES.filter((s) => ids.has(s.id));
  return list.length ? list : SCHEMES;
}

export function useAllowedSchemes(): Scheme[] {
  const meta = useSettings().data?.settings.find((s) => s.key === "ui.theme_day");
  return pickSchemes(meta?.options?.map((o) => String(o.value)));
}
