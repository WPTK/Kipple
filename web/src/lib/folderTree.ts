import type { Folder } from "@/api/types";

// The folder hierarchy as the web app sees it. The bootstrap lists folders flat, in pre-order (every folder after its
// parent, siblings by position), each with its `parent_id`; this module is the one place that turns that list into a
// tree, so the sidebar, Manage Feeds, the pickers, the counts and the layout lookup all agree on it.

/** How deep folders nest (the server refuses a ninth level with 409 folder_too_deep). */
export const MAX_FOLDER_DEPTH = 8;

/** Between the names of a folder path on screen. Not "/": a folder name may contain a slash. */
export const PATH_SEP = " › ";

type F = Pick<Folder, "id" | "name"> & { parent_id?: string | null; is_default?: boolean };

export interface FolderTree<T extends F = Folder> {
  byId: ReadonlyMap<string, T>;
  /** Child ids by parent id ("" is the top level), in the order the folders were listed. */
  children: ReadonlyMap<string, readonly string[]>;
  /** Every folder id, parents before children and siblings in order. */
  preorder: readonly string[];
  /** Each folder's parent in this tree (null at the top level). */
  parents: ReadonlyMap<string, string | null>;
}

const TOP = "";

/**
 * Build the tree. A parent that is not in the list (an older server sends none) puts the folder at the top level, and
 * so does a loop of parents (which the server never sends): every folder is in the tree exactly once.
 */
export function folderTree<T extends F>(folders: readonly T[]): FolderTree<T> {
  const byId = new Map(folders.map((f) => [f.id, f]));
  const parents = new Map<string, string | null>();
  const children = new Map<string, string[]>();
  const add = (p: string, id: string) => {
    let list = children.get(p);
    if (!list) children.set(p, (list = []));
    list.push(id);
  };
  for (const f of folders) {
    const p = f.parent_id && f.parent_id !== f.id && byId.has(f.parent_id) ? f.parent_id : null;
    parents.set(f.id, p);
    add(p ?? TOP, f.id);
  }
  const preorder: string[] = [];
  const seen = new Set<string>();
  const walk = (id: string) => {
    for (const c of children.get(id) ?? []) {
      if (seen.has(c)) continue;
      seen.add(c);
      preorder.push(c);
      walk(c);
    }
  };
  walk(TOP);
  // Whatever the walk from the top never reached sits in a loop: cut it at its first listed folder.
  for (const f of folders) {
    if (seen.has(f.id)) continue;
    const old = parents.get(f.id);
    if (old) children.set(old, (children.get(old) ?? []).filter((x) => x !== f.id));
    parents.set(f.id, null);
    add(TOP, f.id);
    seen.add(f.id);
    preorder.push(f.id);
    walk(f.id);
  }
  return { byId, children, preorder, parents };
}

export function parentOf(t: FolderTree<F>, id: string): string | null {
  return t.parents.get(id) ?? null;
}

/** The folders directly inside `id` (null: the top level). */
export function childrenOf(t: FolderTree<F>, id: string | null): readonly string[] {
  return t.children.get(id ?? TOP) ?? [];
}

/** The folder and its ancestors, nearest first. A folder not in the tree is its own chain. */
export function chainOf(t: FolderTree<F>, id: string): string[] {
  const out = [id];
  for (let cur = parentOf(t, id); cur !== null && out.length <= MAX_FOLDER_DEPTH * 2 && !out.includes(cur); cur = parentOf(t, cur)) out.push(cur);
  return out;
}

/** 1 for a top-level folder. */
export const depthOf = (t: FolderTree<F>, id: string): number => chainOf(t, id).length;

/** The folder and everything inside it. */
export function subtreeOf(t: FolderTree<F>, id: string): Set<string> {
  const out = new Set<string>();
  const walk = (x: string) => {
    if (out.has(x)) return;
    out.add(x);
    for (const c of childrenOf(t, x)) walk(c);
  };
  if (t.byId.has(id)) walk(id);
  return out;
}

/** How many levels the folder's subtree spans: 1 for a folder with no subfolders. */
export function heightOf(t: FolderTree<F>, id: string): number {
  let h = 0;
  for (const c of childrenOf(t, id)) h = Math.max(h, heightOf(t, c));
  return h + 1;
}

/** "Tech › Apple": the names from the top level down. */
export function folderPath(t: FolderTree<F>, id: string): string {
  return chainOf(t, id)
    .reverse()
    .map((x) => t.byId.get(x)?.name ?? "")
    .join(PATH_SEP);
}

/** The path of the folders above this one ("Tech" for Tech › Apple), or "" at the top level. */
export function parentPath(t: FolderTree<F>, id: string): string {
  const p = parentOf(t, id);
  return p === null ? "" : folderPath(t, p);
}

/** Each folder's total over its subtree, from the folders' own totals. */
export function rollUp(t: FolderTree<F>, own: ReadonlyMap<string, number>): Map<string, number> {
  const out = new Map<string, number>();
  for (let i = t.preorder.length - 1; i >= 0; i--) {
    const id = t.preorder[i] as string;
    const n = (own.get(id) ?? 0) + (out.get(id) ?? 0);
    out.set(id, n);
    const p = parentOf(t, id);
    if (p !== null) out.set(p, (out.get(p) ?? 0) + n);
  }
  return out;
}

/**
 * Where a folder can go: the folders that can hold it as a subfolder. `moving` null is a new folder. Leaves out the
 * default folder (it holds no subfolders), the folder itself and everything inside it, and any folder that would push
 * the moved subtree past MAX_FOLDER_DEPTH.
 */
export function parentChoices(t: FolderTree<F>, moving: string | null): string[] {
  if (moving !== null && t.byId.get(moving)?.is_default) return []; // the default folder stays at the top level
  const inside = moving === null ? new Set<string>() : subtreeOf(t, moving);
  const height = moving === null ? 1 : heightOf(t, moving);
  return t.preorder.filter((id) => !t.byId.get(id)?.is_default && !inside.has(id) && depthOf(t, id) + height <= MAX_FOLDER_DEPTH);
}

/**
 * Every folder's feeds over its whole subtree, in screen order (subfolders first, then its own), in one pass:
 * pre-order walked backwards builds each folder from its children, which were built already.
 */
export function subtreeFeeds(t: FolderTree<F>, feedsOf: (folder: string) => readonly string[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (let i = t.preorder.length - 1; i >= 0; i--) {
    const id = t.preorder[i] as string;
    const list: string[] = [];
    for (const c of childrenOf(t, id)) list.push(...(out.get(c) ?? []));
    list.push(...feedsOf(id));
    out.set(id, list);
  }
  return out;
}

/**
 * Feed ids in the order a tree of folders shows them: each folder's subfolders first, then its own feeds. `feedsOf`
 * gives a folder's own feeds in order.
 */
export function feedOrder(t: FolderTree<F>, feedsOf: (folder: string) => readonly string[]): string[] {
  const out: string[] = [];
  const walk = (parent: string | null) => {
    for (const id of childrenOf(t, parent)) {
      walk(id);
      out.push(...feedsOf(id));
    }
  };
  walk(null);
  return out;
}
