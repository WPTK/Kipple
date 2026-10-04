import { DropdownMenu } from "radix-ui";
import { Check, LayoutGrid, Star } from "lucide-react";
import type { Scope } from "@/api/types";
import { useFolderTree } from "@/api/queries";
import { folderPath } from "@/lib/folderTree";
import { useResolvedLayout } from "@/layouts";
import {
  LAYOUT_HINTS,
  LAYOUT_IDS,
  LAYOUT_LABELS,
  overrideTarget,
  sessionLayoutStore,
  setLayoutOverride,
  updateDevicePrefs,
  useDevicePrefs,
  type LayoutId,
} from "@/lib/devicePrefs";
import { cn } from "@/lib/cn";
import { announce } from "@/shell/toasts";

const item =
  "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";

/**
 * Layout picker in the list header. One list of layouts; the star beside each one makes it this device's
 * default (a filled star is the default). On a feed or folder list the radio choice is that list's own
 * override (the first choice clears it: "Use device default", or "Inherited from <folder>" when a folder above has
 * an override), under its own heading; on the other lists the radio choice is the
 * device default itself. Everything is stored per device (src/lib/devicePrefs).
 */
export function LayoutMenu({ scope }: { scope: Scope }) {
  const dp = useDevicePrefs();
  const { layout, ctx } = useResolvedLayout(scope);
  const target = overrideTarget(ctx);
  const current = target ? dp.overrides[target.kind][target.id] : undefined;
  const noun = target?.kind === "folder" ? "folder" : "feed";
  const tree = useFolderTree();
  // Without its own override a list follows the nearest folder above it that has one, else the device default.
  const above = (target?.kind === "folder" ? ctx.folderIds?.slice(1) : ctx.folderIds) ?? [];
  const from = above.find((id) => dp.overrides.folder[id]);
  const fallback = from
    ? `Inherited from ${folderPath(tree, from)} (${LAYOUT_LABELS[dp.overrides.folder[from] as LayoutId] ?? ""})`
    : `Use device default (${LAYOUT_LABELS[dp.layout]})`;
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
          aria-label={`Layout: ${layout.label}`}
          className="hit inline-flex items-center justify-center rounded-lg text-fg hover:bg-selection"
        >
          <LayoutGrid className="size-5" aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="start" sideOffset={4} collisionPadding={8} className="z-50 w-72 max-w-[calc(100vw-1rem)] rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
          <DropdownMenu.Label className="px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">
            {target ? `This ${noun}` : "Layout"}
          </DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            value={target ? (current ?? "default") : dp.layout}
            onValueChange={(v) => {
              sessionLayoutStore.set(null);
              if (!target) {
                updateDevicePrefs({ layout: v as LayoutId });
                say(v as LayoutId);
              } else if (v === "default") setLayoutOverride(target.kind, target.id, null);
              else {
                setLayoutOverride(target.kind, target.id, v as LayoutId);
                say(v as LayoutId);
              }
            }}
          >
            {target ? <Radio value="default" label={fallback} /> : null}
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
        <span className="block truncate">{label}</span>
        {hint ? <span className="block truncate text-xs text-fg2">{hint}</span> : null}
      </span>
    </DropdownMenu.RadioItem>
  );
}
