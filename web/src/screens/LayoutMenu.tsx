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
  LAYOUT_LABELS,
  LIST_VIEWS,
  LIST_VIEW_LABELS,
  ORDER_LABELS,
  groupLockReason,
  inheritedList,
  noReadingTimeSections,
  orderLockReason,
  overrideTarget,
  sectionsFor,
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
const DIGEST_SECTION_LABELS: Record<DigestSections, string> = { folder: "Folders", feed: "Feeds", length: "Reading time" };
const sub = "px-3 pt-1 text-xs text-fg2";

/** The layouts by what they lead with: pictures, rows of text, or whole pages. One heading deep. */
const LAYOUT_GROUPS: { label: string; ids: readonly LayoutId[] }[] = [
  { label: "Pictures", ids: ["magazine", "cards"] },
  { label: "Lists", ids: ["compact", "inbox", "headlines"] },
  { label: "Pages", ids: ["gazette", "digest"] },
];
const ORDER_WHY_ID = "layout-menu-order-why";
const GROUP_WHY_ID = "layout-menu-group-why";

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
 * The Show and sort menu in the list header (its button names the layout in effect). Layout: one list; on a feed or folder list the star
 * beside each layout makes it this device's default (a filled star is the default; on the other lists the radio is the default). Order, and on a feed or folder list the view it
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
  const orderWhy = orderLockReason(layout.id);
  const groupWhy = groupLockReason(layout.id, !!scope.q);
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
          aria-label={`Show and sort, ${layout.label} layout`}
          className="hit inline-flex items-center justify-center rounded-lg text-fg hover:bg-selection"
        >
          <LayoutGrid className="size-5" aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="start" sideOffset={4} popoverWidth="w-72">
          {target ? <p className="px-3 pt-2 text-xs text-fg2">These choices are for this {noun} on this device.</p> : null}
          {!target ? <p className="px-3 pt-2 text-xs text-fg2">These choices are the defaults on this device.</p> : null}
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
            {LAYOUT_GROUPS.map((g) => (
              <div key={g.label} role="presentation">
                <DropdownMenu.Label className={sub}>{g.label}</DropdownMenu.Label>
                {g.ids.map((id) => (
                  <div key={id} className="flex items-center">
                    <Radio value={id} label={LAYOUT_LABELS[id]} hint={LAYOUT_HINTS[id]} className="min-w-0 flex-1" />
                    {target ? (
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
                    ) : null}
                  </div>
                ))}
                {g.label === "Pictures" ? <PicturesChoice /> : null}
              </div>
            ))}
          </DropdownMenu.RadioGroup>
          {target ? <p className="px-3 pt-1 pb-2 text-xs text-fg2">The star sets this device's default layout.</p> : null}

          <DropdownMenu.Separator className="my-1 h-px bg-line" />
          <DropdownMenu.Label className={heading}>Order</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            aria-label={target ? `Order of this ${noun}` : "Order"}
            value={target ? (own?.order ?? "default") : dp.order}
            onValueChange={(v) => {
              if (orderWhy) return;
              const order = v === "default" ? null : (v as OrderPref);
              if (!target) updateDevicePrefs({ order: order ?? "newest" });
              else setListOverride(target.kind, target.id, "order", order);
              if (order) announce(ORDER_LABELS[order]);
            }}
          >
            {target ? <Radio value="default" label={orderFallback} unavailable={!!orderWhy} describedBy={ORDER_WHY_ID} /> : null}
            {(["newest", "oldest"] as const).map((o) => (
              <Radio key={o} value={o} label={ORDER_LABELS[o]} unavailable={!!orderWhy} describedBy={ORDER_WHY_ID} />
            ))}
          </DropdownMenu.RadioGroup>
          {orderWhy ? (
            <p id={ORDER_WHY_ID} className="px-3 pt-1 pb-2 text-xs text-fg2">
              {orderWhy}
            </p>
          ) : null}

          {layout.id === "digest" && !scope.q ? (
            <>
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
              <DropdownMenu.Label className={heading}>Digest, for this device</DropdownMenu.Label>
              <SectionOrderChoice name="sections" />
              <DropdownMenu.Label className={sub}>Sections</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                aria-label="Sections"
                value={sectionsFor(dp.digestSections, scope)}
                onValueChange={(v) => {
                  if (v === "length" && noReadingTimeSections(scope)) return;
                  updateDevicePrefs({ digestSections: v as DigestSections });
                  announce(`Sections are ${DIGEST_SECTION_LABELS[v as DigestSections].toLowerCase()}`);
                }}
              >
                {(["folder", "feed", "length"] as const).map((o) => {
                  const why = o === "length" ? noReadingTimeSections(scope) : null;
                  return <Radio key={o} value={o} label={DIGEST_SECTION_LABELS[o]} hint={why ?? undefined} unavailable={!!why} />;
                })}
              </DropdownMenu.RadioGroup>
              <RowsPerSection />
            </>
          ) : null}

          <DropdownMenu.Separator className="my-1 h-px bg-line" />
          <DropdownMenu.Label className={heading}>Group by</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            aria-label={target ? `Grouping of this ${noun}` : "Group by"}
            value={target ? (own?.group ?? "default") : dp.group}
            onValueChange={(v) => {
              if (groupWhy) return;
              const group = v === "default" ? null : (v as GroupBy);
              if (!target) updateDevicePrefs({ group: group ?? "day" });
              else setListOverride(target.kind, target.id, "group", group);
              if (group) announce(`Group by ${GROUP_LABELS[group].toLowerCase()}`);
            }}
          >
            {target ? <Radio value="default" label={groupFallback} unavailable={!!groupWhy} describedBy={GROUP_WHY_ID} /> : null}
            {GROUP_BYS.map((g) => (
              <Radio key={g} value={g} label={GROUP_LABELS[g]} unavailable={!!groupWhy} describedBy={GROUP_WHY_ID} />
            ))}
          </DropdownMenu.RadioGroup>
          {groupWhy ? (
            <p id={GROUP_WHY_ID} className="px-3 pt-1 pb-2 text-xs text-fg2">
              {groupWhy}
            </p>
          ) : null}

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

/** Pictures in lists, Show or Hide: the Inbox's thumbnails and the Digest's images. The same device setting as Settings, Appearance, Lists. */
function PicturesChoice() {
  const dp = useDevicePrefs();
  return (
    <>
      <div role="presentation" className="mx-3 mt-1 mb-0.5 h-px bg-line" />
      <DropdownMenu.Label className={cn(sub, "font-semibold text-fg")}>Pictures in lists</DropdownMenu.Label>
      <DropdownMenu.RadioGroup
        aria-label="Pictures in lists"
        value={dp.listImages ? "show" : "hide"}
        onValueChange={(v) => {
          updateDevicePrefs({ listImages: v === "show" });
          announce(v === "show" ? "Pictures shown in lists" : "Pictures hidden in lists");
        }}
      >
        <Radio value="show" label="Show" hint="Inbox and Digest" />
        <Radio value="hide" label="Hide" />
      </DropdownMenu.RadioGroup>
    </>
  );
}

/** Rows per section: how many articles a Digest section shows before "more". */
function RowsPerSection() {
  const dp = useDevicePrefs();
  return (
    <>
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
  );
}

/**
 * The device's order for the Digest's sections and the Feed index's blocks (one setting, `digestOrder`): the sidebar's
 * order, or the feeds that post least first. `name` is what the order is of ("sections", "feeds").
 */
export function SectionOrderChoice({ name }: { name: "sections" | "feeds" }) {
  const dp = useDevicePrefs();
  const label = `Order of ${name}`;
  return (
    <>
      <DropdownMenu.Label className={sub}>{label}</DropdownMenu.Label>
      <DropdownMenu.RadioGroup
        aria-label={label}
        value={dp.digestOrder}
        onValueChange={(v) => {
          updateDevicePrefs({ digestOrder: v as DigestOrder });
          announce(DIGEST_ORDER_LABELS[v as DigestOrder]);
        }}
      >
        {(["sidebar", "quiet"] as const).map((o) => (
          <Radio key={o} value={o} label={DIGEST_ORDER_LABELS[o]} hint={o === "quiet" ? `The ${name} with the fewest new articles a day come first; ${name === "feeds" ? "a feed" : "a section with a feed"} under a week old goes last` : undefined} />
        ))}
      </DropdownMenu.RadioGroup>
    </>
  );
}

/** `unavailable` greys the choice but keeps it focusable (aria-disabled, selection blocked), so its reason (its hint, or the line `describedBy` names) is reachable by keyboard and screen reader. The caller's onValueChange must ignore it too: a menu radio reports its value even when its select is prevented. */
function Radio({ value, label, hint, className, unavailable, describedBy }: { value: string; label: string; hint?: string; className?: string; unavailable?: boolean; describedBy?: string }) {
  return (
    <DropdownMenu.RadioItem
      value={value}
      aria-disabled={unavailable || undefined}
      aria-describedby={unavailable ? describedBy : undefined}
      onSelect={unavailable ? (e) => e.preventDefault() : undefined}
      className={cn("relative py-1.5 pl-9", unavailable && "opacity-50", className)}
    >
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
