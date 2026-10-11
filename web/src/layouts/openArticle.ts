import { createContext, useContext } from "react";

/**
 * The article open in the reader pane beside the list, if any. The list sets it once; a row marks itself with
 * `aria-current` and `data-open` (styled in index.css), which stays put when the keyboard cursor moves to another row.
 */
export const OpenArticle = createContext<string | undefined>(undefined);

/** The attributes of a row (or story) that is the open article: spread them on its element. */
export function useOpenMark(id: string): { "aria-current"?: "true"; "data-open"?: "" } {
  return useContext(OpenArticle) === id ? { "aria-current": "true", "data-open": "" } : {};
}
