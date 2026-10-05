import { useListContext, useSetListOverride } from "@/layouts";
import {
  LAYOUT_IDS,
  LAYOUT_LABELS,
  LIST_VIEWS,
  LIST_VIEW_LABELS,
  ORDER_LABELS,
  useDevicePrefs,
  type ListField,
  type ListOverride,
} from "@/lib/devicePrefs";
import { Field, inputCls } from "@/ui/kit";
import { useFallbackLabel } from "../LayoutMenu";

const CHOICES: { field: ListField; label: string; ids: readonly string[]; names: Record<string, string> }[] = [
  { field: "layout", label: "Layout on this device", ids: LAYOUT_IDS, names: LAYOUT_LABELS },
  { field: "order", label: "Order on this device", ids: ["newest", "oldest"], names: ORDER_LABELS },
  { field: "view", label: "Opens in", ids: LIST_VIEWS, names: LIST_VIEW_LABELS },
];

/**
 * A feed's or folder's own layout, order and opening view on this device (the same overrides the list header's
 * options menu sets). Each applies at once, like the menu. The first choice of each clears the list's own value and
 * names what it then gets, as the menu does: "Inherited from Tech (Cards)", or the device default.
 */
export function ListOverrideFields({ kind, id }: { kind: "feed" | "folder"; id: string }) {
  const own = useDevicePrefs().overrides[kind][id];
  const ctx = useListContext(kind === "feed" ? { feed: id } : { folder: id });
  const setListOverride = useSetListOverride();
  const fallback: Record<ListField, string> = {
    layout: useFallbackLabel(ctx, "layout"),
    order: useFallbackLabel(ctx, "order"),
    view: useFallbackLabel(ctx, "view"),
  };
  const help =
    kind === "feed"
      ? "For this feed only. Left on the first choice, it follows its folder, else this device."
      : "For this folder and its subfolders, unless a subfolder or a feed has its own.";
  return (
    <>
      {CHOICES.map((c, i) => (
        <Field key={c.field} label={c.label} help={i === 0 ? help : undefined}>
          {(a) => (
            <select
              {...a}
              value={own?.[c.field] ?? "default"}
              onChange={(e) => setListOverride(kind, id, c.field, e.target.value === "default" ? null : (e.target.value as NonNullable<ListOverride[ListField]>))}
              className={inputCls}
            >
              <option value="default">{fallback[c.field]}</option>
              {c.ids.map((v) => (
                <option key={v} value={v}>
                  {c.names[v]}
                </option>
              ))}
            </select>
          )}
        </Field>
      ))}
    </>
  );
}
