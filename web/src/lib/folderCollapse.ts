import { updateDevicePrefs } from "./devicePrefs";

/** Whether every folder in `ids` (the folders on screen) is collapsed: the next press of the toggle expands them all. */
export const allCollapsed = (ids: readonly string[], collapsedIds: readonly string[]): boolean => ids.length > 0 && ids.every((id) => collapsedIds.includes(id));

/** Collapse every folder in `ids`, or expand every folder; remembered on this device like a single chevron. */
export function setAllCollapsed(ids: readonly string[], collapse: boolean): void {
  updateDevicePrefs({ collapsedFolders: collapse ? [...ids] : [] });
}
