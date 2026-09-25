import { DropdownMenu } from "radix-ui";
import { Check, LayoutGrid } from "lucide-react";
import type { Scope } from "@/api/types";
import { useResolvedLayout } from "@/layouts";
import {
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
 * Layout picker in the list header. On a feed or folder list it sets that list's override
 * ("Use device default" clears it); the device default is always one section below.
 * Choices are stored per device (src/lib/devicePrefs).
 */
export function LayoutMenu({ scope }: { scope: Scope }) {
  const dp = useDevicePrefs();
  const { layout, ctx } = useResolvedLayout(scope);
  const target = overrideTarget(ctx);
  const current = target ? dp.overrides[target.kind][target.id] : undefined;
  const noun = target?.kind === "folder" ? "folder" : "feed";
  const say = (l: LayoutId) => announce(`${LAYOUT_LABELS[l]} layout`);

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
        <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-60 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
          {target ? (
            <>
              <DropdownMenu.Label className="px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">This {noun}</DropdownMenu.Label>
              <DropdownMenu.RadioGroup
                value={current ?? "default"}
                onValueChange={(v) => {
                  sessionLayoutStore.set(null);
                  if (v === "default") setLayoutOverride(target.kind, target.id, null);
                  else {
                    setLayoutOverride(target.kind, target.id, v as LayoutId);
                    say(v as LayoutId);
                  }
                }}
              >
                <Radio value="default" label={`Use device default (${LAYOUT_LABELS[dp.layout]})`} />
                {LAYOUT_IDS.map((id) => (
                  <Radio key={id} value={id} label={LAYOUT_LABELS[id]} />
                ))}
              </DropdownMenu.RadioGroup>
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
            </>
          ) : null}
          <DropdownMenu.Label className="px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">Device default</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            value={dp.layout}
            onValueChange={(v) => {
              sessionLayoutStore.set(null);
              updateDevicePrefs({ layout: v as LayoutId });
              say(v as LayoutId);
            }}
          >
            {LAYOUT_IDS.map((id) => (
              <Radio key={id} value={id} label={LAYOUT_LABELS[id]} />
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function Radio({ value, label }: { value: string; label: string }) {
  return (
    <DropdownMenu.RadioItem value={value} className={cn(item, "pl-9 relative")}>
      <DropdownMenu.ItemIndicator className="absolute left-3 inline-flex">
        <Check className="size-4" aria-hidden="true" />
      </DropdownMenu.ItemIndicator>
      {label}
    </DropdownMenu.RadioItem>
  );
}
