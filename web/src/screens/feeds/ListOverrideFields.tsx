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
import { useState } from "react";
import { Field, inputCls } from "@/ui/kit";
import { useFallbackLabel } from "../LayoutMenu";

const CHOICES: { field: ListField; label: string; ids: readonly string[]; names: Record<string, string> }[] = [
  { field: "layout", label: "Layout on this device", ids: LAYOUT_IDS, names: LAYOUT_LABELS },
  { field: "order", label: "Order on this device", ids: ["newest", "oldest"], names: ORDER_LABELS },
  { field: "view", label: "Opens in", ids: LIST_VIEWS, names: LIST_VIEW_LABELS },
];

/**
 * A feed's or folder's own layout, order and opening view on this device (the same overrides the list header's

 * options menu sets), as a draft: nothing is written until the dialog's Save calls `apply` from
 * useListOverrideDraft, so Cancel leaves them as they were. The first choice of each clears the list's own value and
 * names what it then gets, as the menu does: "Inherited from Tech (Cards)", or the device default.
 */
export interface OverrideDraft {
  /** The fields changed in the dialog; a null value clears the override. */
  draft: Partial<Record<ListField, string | null>>;
  setDraft: (field: ListField, value: string | null) => void;
  /** Write the draft to this device's preferences. */
  apply: () => void;
  /** Whether Save has anything to write. */
  dirty: boolean;
}

export function useListOverrideDraft(kind: "feed" | "folder", id: string): OverrideDraft {
  const [draft, setAll] = useState<Partial<Record<ListField, string | null>>>({});
  const setListOverride = useSetListOverride();
  return {
    draft,
    setDraft: (field, value) => setAll((d) => ({ ...d, [field]: value })),
    apply: () => {
      for (const [field, value] of Object.entries(draft)) setListOverride(kind, id, field as ListField, value as ListOverride[ListField] | null);
    },
    dirty: Object.keys(draft).length > 0,
  };
}

export function ListOverrideFields({ kind, id, draft: state }: { kind: "feed" | "folder"; id: string; draft: OverrideDraft }) {
  const stored = useDevicePrefs().overrides[kind][id];
  const own = (field: ListField): string | null => (field in state.draft ? (state.draft[field] ?? null) : ((stored?.[field] as string | undefined) ?? null));
  const ctx = useListContext(kind === "feed" ? { feed: id } : { folder: id });
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
              value={own(c.field) ?? "default"}
              onChange={(e) => state.setDraft(c.field, e.target.value === "default" ? null : e.target.value)}
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
