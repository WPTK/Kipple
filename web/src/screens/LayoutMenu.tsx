import { Check, LayoutGrid, Star } from "lucide-react";
import type { Scope } from "@/api/types";
import { useFolderTree } from "@/api/queries";
import { folderPath } from "@/lib/folderTree";
import { useResolvedLayout, useSetListOverride } from "@/layouts";
import {
  DIGEST_ROWS,
  GROUP_BYS,
  GROUP_LABELS,
  LAYOUT_HINTS,
  LAYOUT_IDS,
  LAYOUT_LABELS,
  LIST_VIEWS,
  LIST_VIEW_LABELS,
  ORDER_LABELS,
  followsOrder,
  inheritedList,
  overrideTarget,
  updateDevicePrefs,
  useDevicePrefs,
  type DigestOrder,
  type DigestRows,
  type DigestSections,
  type GroupBy,
  type LayoutContext,
  type LayoutId,
  type ListField,
  type OrderPref,
} from "@/lib/devicePrefs";
import { cn } from "@/lib/cn";
import { announce } from "@/shell/toasts";
import { DropdownMenu } from "@/ui/menu";

const heading = "px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase";

const DIGEST_ORDER_LABELS: Record<DigestOrder, string> = { sidebar: "Sidebar order", quiet: "Quiet Feeds First" };
const DIGEST_SECTION_LABELS: Record<DigestSections, string> = { folder: "Folders", feed: "Feeds" };
const sub = "px-3 pt-1 text-xs text-fg2";

const FIELD_LABELS: { [F in ListField]: Record<string, string> } = { layout: LAYOUT_LABELS, order: ORDER_LABELS, view: LIST_VIEW_LABELS, group: GROUP_LABELS };

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
  return field === "view" ? `Use the standard view (${value})` : `Use device default (${value})`;
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
  const groupFallback = useFallbackLabel(ctx, "group");
  const say = (l: LayoutId) => announce(`${LAYOUT_LABELS[l]} layout`);
  const makeDefault = (l: LayoutId) => {
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
        <DropdownMenu.Content align="start" sideOffset={4} popoverWidth="w-72">
          {target ? <p className="px-3 pt-2 text-xs text-fg2">These choices are for this {noun} on this device.</p> : null}
          <DropdownMenu.Label className={heading}>Layout</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            aria-label={target ? `Layout of this ${noun}` : "Layout"}
            value={target ? (own?.layout ?? "default") : dp.layout}
            onValueChange={(v) => {
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

          {layout.id === "digest" && !scope.q ? (
            <>
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
              <DropdownMenu.Label className={heading}>Digest, for this device</DropdownMenu.Label>
              <DropdownMenu.Label className={sub}>Order of sections</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label="Order of sections"
                value={dp.digestOrder}
                onValueChange={(v) => {
                  updateDevicePrefs({ digestOrder: v as DigestOrder });
                  announce(DIGEST_ORDER_LABELS[v as DigestOrder]);
                }}
              >
                {(["sidebar", "quiet"] as const).map((o) => (
                  <Radio key={o} value={o} label={DIGEST_ORDER_LABELS[o]} hint={o === "quiet" ? "The sections with the fewest new articles a day come first; a section with a feed under a week old goes last" : undefined} />
                ))}
              </DropdownMenu.RadioGroup>
              <DropdownMenu.Label className={sub}>Sections</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label="Sections"
                value={dp.digestSections}
                onValueChange={(v) => {
                  updateDevicePrefs({ digestSections: v as DigestSections });
                  announce(`Sections are ${DIGEST_SECTION_LABELS[v as DigestSections].toLowerCase()}`);
                }}
              >
                {(["folder", "feed"] as const).map((o) => (
                  <Radio key={o} value={o} label={DIGEST_SECTION_LABELS[o]} />
                ))}
              </DropdownMenu.RadioGroup>
              <DropdownMenu.Label className={sub}>Images</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label="Images"
                value={dp.digestImages ? "on" : "off"}
                onValueChange={(v) => {
                  updateDevicePrefs({ digestImages: v === "on" });
                  announce(v === "on" ? "Images on" : "Images off");
                }}
              >
                <Radio value="on" label="Show images" />
                <Radio value="off" label="Text only" />
              </DropdownMenu.RadioGroup>
              <DropdownMenu.Label className={sub}>Rows per section</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label="Rows per section"
                value={String(dp.digestRows)}
                onValueChange={(v) => {
                  updateDevicePrefs({ digestRows: Number(v) as DigestRows });
                  announce(`${v} rows per section`);
                }}
              >
                {DIGEST_ROWS.map((n) => (
                  <Radio key={n} value={String(n)} label={String(n)} />
                ))}
              </DropdownMenu.RadioGroup>
            </>
          ) : null}

          {/* The Gazette prints its own pages and a search its own headers; the other lists group under headers. */}
          {scope.q || !followsOrder(layout.id) ? null : (
            <>
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
              <DropdownMenu.Label className={heading}>Group by</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label={target ? `Grouping of this ${noun}` : "Group by"}
                value={target ? (own?.group ?? "default") : dp.group}
                onValueChange={(v) => {
                  const group = v === "default" ? null : (v as GroupBy);
                  if (!target) updateDevicePrefs({ group: group ?? "day" });
                  else setListOverride(target.kind, target.id, "group", group);
                  if (group) announce(`Group by ${GROUP_LABELS[group].toLowerCase()}`);
                }}
              >
                {target ? <Radio value="default" label={groupFallback} /> : null}
                {GROUP_BYS.map((g) => (
                  <Radio key={g} value={g} label={GROUP_LABELS[g]} />
                ))}
              </DropdownMenu.RadioGroup>
            </>
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
    <DropdownMenu.RadioItem value={value} className={cn("relative py-1.5 pl-9", className)}>
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
