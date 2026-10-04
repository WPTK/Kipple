import type { SelectHTMLAttributes } from "react";
import { useFolderTree } from "@/api/queries";
import { folderPath, type FolderTree } from "@/lib/folderTree";
import { inputCls } from "./kit";

/**
 * One <option> per folder, in the order the tree shows them, each labelled by its full path ("Tech › Apple"), so two
 * subfolders with the same name are told apart. `only` limits the list (for example to where a folder may move);
 * `optionValue` maps a folder id to the option value.
 */
export function FolderOptions({ tree, only, optionValue = (id) => id }: { tree: FolderTree; only?: readonly string[]; optionValue?: (id: string) => string }) {
  const keep = only ? new Set(only) : null;
  return (
    <>
      {tree.preorder
        .filter((id) => !keep || keep.has(id))
        .map((id) => (
          <option key={id} value={optionValue(id)}>
            {folderPath(tree, id)}
          </option>
        ))}
    </>
  );
}

/**
 * The folder picker every dialog uses (add feed, edit feed, move, new folder, filters). `none` adds a first option
 * with the empty value (for example "Top level" or "Default folder").
 */
export function FolderSelect({
  value,
  onChange,
  only,
  none,
  className,
  ...rest
}: { value: string; onChange: (id: string) => void; only?: readonly string[]; none?: string } & Omit<SelectHTMLAttributes<HTMLSelectElement>, "value" | "onChange">) {
  const tree = useFolderTree();
  return (
    <select {...rest} value={value} onChange={(e) => onChange(e.target.value)} className={className ?? inputCls}>
      {none !== undefined ? <option value="">{none}</option> : null}
      <FolderOptions tree={tree} only={only} />
    </select>
  );
}
