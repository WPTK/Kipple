import { DropdownMenu } from "radix-ui";
import { Check, Timer } from "lucide-react";
import { useNavigate } from "react-router";
import type { Scope } from "@/api/types";
import { cn } from "@/lib/cn";
import { READING_LENGTHS, READING_LENGTH_LABELS, isReadingLength } from "@/lib/readingLength";
import { listTo } from "@/lib/routes";

const item =
  "relative flex min-h-11 cursor-default items-center gap-3 rounded-lg pr-3 pl-9 text-sm outline-none select-none data-[highlighted]:bg-selection";

/**
 * The reading-time filter in the list header: only articles of one length. It lives in the list's address (`?len=`), so
 * the view pills and an opened article keep it and another list starts without it. "Mark all as read" marks only what
 * the filter shows.
 */
export function LengthMenu({ scope }: { scope: Scope }) {
  const navigate = useNavigate();
  const label = scope.length ? READING_LENGTH_LABELS[scope.length] : "Any length";
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label={`Reading time: ${label}`}
          className={cn("hit inline-flex items-center justify-center rounded-lg hover:bg-selection", scope.length ? "text-accent" : "text-fg")}
        >
          <Timer className="size-5" aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="start" sideOffset={4} collisionPadding={8} className="z-50 w-60 max-w-[calc(100vw-1rem)] rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
          <DropdownMenu.Label className="px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">Reading time</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            value={scope.length ?? "any"}
            onValueChange={(v) => navigate(listTo({ ...scope, length: isReadingLength(v) ? v : undefined }), { replace: true })}
          >
            {(["any", ...READING_LENGTHS] as const).map((v) => (
              <DropdownMenu.RadioItem key={v} value={v} className={item}>
                <DropdownMenu.ItemIndicator className="absolute left-3 inline-flex">
                  <Check className="size-4" aria-hidden="true" />
                </DropdownMenu.ItemIndicator>
                {v === "any" ? "Any length" : READING_LENGTH_LABELS[v]}
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
          <p className="px-3 pt-1 pb-2 text-xs text-fg2">Very short articles count as 5 min or less. Articles with no text are left out while a length is picked.</p>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
