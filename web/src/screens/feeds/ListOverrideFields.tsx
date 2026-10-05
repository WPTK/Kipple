import {
  LAYOUT_IDS,
  LAYOUT_LABELS,
  LIST_VIEWS,
  LIST_VIEW_LABELS,
  ORDER_LABELS,
  setListOverride,
  useDevicePrefs,
  type ListField,
  type ListOverride,
} from "@/lib/devicePrefs";
import { Field, inputCls } from "@/ui/kit";

const CHOICES: { field: ListField; label: string; ids: readonly string[]; names: Record<string, string>; fallback: string }[] = [
  { field: "layout", label: "Layout on this device", ids: LAYOUT_IDS, names: LAYOUT_LABELS, fallback: "Use device default" },
  { field: "order", label: "Order on this device", ids: ["newest", "oldest"], names: ORDER_LABELS, fallback: "Use device default" },
  { field: "view", label: "Opens in", ids: LIST_VIEWS, names: LIST_VIEW_LABELS, fallback: "Use the default (Unread)" },
];

/**
 * A feed's or folder's own layout, order and opening view on this device (the same overrides the list header's layout
 * menu sets). Each applies at once, like the menu; leaving one on its default inherits it from the folder above, else the
 * device.
 */
export function ListOverrideFields({ kind, id }: { kind: "feed" | "folder"; id: string }) {
  const own = useDevicePrefs().overrides[kind][id];
  const help =
    kind === "feed"
      ? "For this feed only. Left on the default, it follows its folder, else this device."
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
              <option value="default">{c.fallback}</option>
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
