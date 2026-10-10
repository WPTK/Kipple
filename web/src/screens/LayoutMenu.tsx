import { DropdownMenu } from "radix-ui";
import { Check, LayoutGrid, Star } from "lucide-react";
import type { Scope } from "@/api/types";
import { useFolderTree } from "@/api/queries";
import { folderPath } from "@/lib/folderTree";
import { useResolvedLayout, useSetListOverride } from "@/layouts";
import {
  LAYOUT_HINTS,
  LAYOUT_IDS,
  LAYOUT_LABELS,
  LIST_VIEWS,
  LIST_VIEW_LABELS,
  ORDER_LABELS,
  followsOrder,
  inheritedList,
  overrideTarget,
  sessionLayoutStore,
  updateDevicePrefs,
  useDevicePrefs,
  type LayoutContext,
  type LayoutId,
  type ListField,
  type OrderPref,
} from "@/lib/devicePrefs";
import { cn } from "@/lib/cn";
import { announce } from "@/shell/toasts";

const item =
  "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 py-1.5 text-sm outline-none select-none data-[highlighted]:bg-selection";
const heading = "px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase";

const FIELD_LABELS: { [F in ListField]: Record<string, string> } = { layout: LAYOUT_LABELS, order: ORDER_LABELS, view: LIST_VIEW_LABELS };

/**
 * The first choice of a feed's or folder's group, which clears its own value: it names what the list then gets, "Inherited
 * from Tech › Apple (Cards)" when a folder above sets it, else the device default ("Unread" for the view).
 */
export function useFallbackLabel(ctx: LayoutContext, field: ListField): string {
  const dp = useDevicePrefs();
  const tree = useFolderTree();
  const up = inheritedList(dp, ctx, field);
  const value = FIELD_LABELS[field][up.value] ?? "";
  if (up.from) return `Inherited from ${folderPath(tree, up.from.id)} (${value})`;
  return field === "view" ? `Use the default (${value})` : `Use device default (${value})`;
}

/**
 * The list options menu in the list header (its button names the layout in effect). Layout: one list; the star beside each
 * layout makes it this device's default (a filled star is the default). Order, and on a feed or folder list the view it
 * opens in. On a feed or folder list each radio choice is that list's own override (the first choice clears it), under
 * its own heading; on the other lists the layout and order choices are the device defaults themselves. Everything is
 * stored per device (src/lib/devicePrefs).
 */
export function LayoutMenu({ scope }: { scope: Scope }) {
  const dp = useDevicePrefs();
  const setListOverride = useSetListOverride();
  const { layout, ctx } = useResolvedLayout(scope);
  const target = overrideTarget(ctx);
  const own = target ? dp.overrides[target.kind][target.id] : undefined;
  const noun = target?.kind === "folder" ? "folder" : "feed";
  const layoutFallback = useFallbackLabel(ctx, "layout");
  const orderFallback = useFallbackLabel(ctx, "order");
  const viewFallback = useFallbackLabel(ctx, "view");
  const say = (l: LayoutId) => announce(`${LAYOUT_LABELS[l]} layout`);
  const makeDefault = (l: LayoutId) => {
    sessionLayoutStore.set(null);
    updateDevicePrefs({ layout: l });
    announce(`${LAYOUT_LABELS[l]} is now the default layout on this device`);
  };

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label={`List options, ${layout.label} layout`}
          className="hit inline-flex items-center justify-center rounded-lg text-fg hover:bg-selection"
        >
          <LayoutGrid className="size-5" aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="start"
          sideOffset={4}
          collisionPadding={8}
          className="z-50 max-h-[var(--radix-dropdown-menu-content-available-height)] w-72 max-w-[calc(100vw-1rem)] overflow-y-auto rounded-xl border border-line bg-bg p-1 text-fg shadow-xl"
        >
          <DropdownMenu.Label className={heading}>{target ? `This ${noun}` : "Layout"}</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            aria-label={target ? `Layout of this ${noun}` : "Layout"}
            value={target ? (own?.layout ?? "default") : dp.layout}
            onValueChange={(v) => {
              sessionLayoutStore.set(null);
              if (!target) {
                updateDevicePrefs({ layout: v as LayoutId });
                say(v as LayoutId);
              } else if (v === "default") setListOverride(target.kind, target.id, "layout", null);
              else {
                setListOverride(target.kind, target.id, "layout", v as LayoutId);
                say(v as LayoutId);
              }
            }}
          >
            {target ? <Radio value="default" label={layoutFallback} /> : null}
            {LAYOUT_IDS.map((id) => (
              <div key={id} className="flex items-center">
                <Radio value={id} label={LAYOUT_LABELS[id]} hint={LAYOUT_HINTS[id]} className="min-w-0 flex-1" />
                <DropdownMenu.CheckboxItem
                  checked={dp.layout === id}
                  aria-label={dp.layout === id ? `${LAYOUT_LABELS[id]} is the device default` : `Make ${LAYOUT_LABELS[id]} the device default`}
                  title={dp.layout === id ? "Device default" : "Make this the device default"}
                  // Keep the menu open so the star visibly fills.
                  onSelect={(e) => {
                    e.preventDefault();
                    makeDefault(id);
                  }}
                  className={cn("hit-row inline-flex shrink-0 cursor-default items-center justify-center rounded-lg outline-none data-[highlighted]:bg-selection", dp.layout === id ? "text-star" : "text-fg2")}
                >
                  <Star className="size-5" fill={dp.layout === id ? "currentColor" : "none"} aria-hidden="true" />
                </DropdownMenu.CheckboxItem>
              </div>
            ))}
          </DropdownMenu.RadioGroup>
          <p className="px-3 pt-1 pb-2 text-xs text-fg2">The star sets this device's default layout.</p>

          <DropdownMenu.Separator className="my-1 h-px bg-line" />
          <DropdownMenu.Label className={heading}>Order</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            aria-label={target ? `Order of this ${noun}` : "Order"}
            value={target ? (own?.order ?? "default") : dp.order}
            onValueChange={(v) => {
              const order = v === "default" ? null : (v as OrderPref);
              if (!target) updateDevicePrefs({ order: order ?? "newest" });
              else setListOverride(target.kind, target.id, "order", order);
              if (order) announce(ORDER_LABELS[order]);
            }}
          >
            {target ? <Radio value="default" label={orderFallback} /> : null}
            {(["newest", "oldest"] as const).map((o) => (
              <Radio key={o} value={o} label={ORDER_LABELS[o]} />
            ))}
          </DropdownMenu.RadioGroup>
          {followsOrder(layout.id) ? null : (
            <p className="px-3 pt-1 pb-2 text-xs text-fg2">The {layout.label} is always newest first. The order applies to the other layouts.</p>
          )}

          {target ? (
            <>
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
              <DropdownMenu.Label className={heading}>Opens in</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label={`View this ${noun} opens in`}
                value={own?.view ?? "default"}
                onValueChange={(v) => setListOverride(target.kind, target.id, "view", v === "default" ? null : (v as (typeof LIST_VIEWS)[number]))}
              >
                <Radio value="default" label={viewFallback} />
                {LIST_VIEWS.map((v) => (
                  <Radio key={v} value={v} label={LIST_VIEW_LABELS[v]} />
                ))}
              </DropdownMenu.RadioGroup>
              <p className="px-3 pt-1 pb-2 text-xs text-fg2">The view this {noun} shows when you open it from your feeds.</p>
            </>
          ) : null}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function Radio({ value, label, hint, className }: { value: string; label: string; hint?: string; className?: string }) {
  return (
    <DropdownMenu.RadioItem value={value} className={cn(item, "relative pl-9", className)}>
      <DropdownMenu.ItemIndicator className="absolute left-3 inline-flex">
        <Check className="size-4" aria-hidden="true" />
      </DropdownMenu.ItemIndicator>
      <span className="min-w-0">
        <span className="block break-words">{label}</span>
        {hint ? <span className="block text-xs break-words text-fg2">{hint}</span> : null}
      </span>
    </DropdownMenu.RadioItem>
  );
}
