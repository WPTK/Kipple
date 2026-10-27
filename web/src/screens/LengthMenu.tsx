import { Check, Timer } from "lucide-react";
import { useNavigate } from "react-router";
import type { Scope } from "@/api/types";
import { cn } from "@/lib/cn";
import { READING_LENGTHS, READING_LENGTH_LABELS, isReadingLength } from "@/lib/readingLength";
import { listTo } from "@/lib/routes";
import { DropdownMenu } from "@/ui/menu";
import { Icon } from "@/ui/Icon";

const item = "relative pr-3 pl-9";

/**
 * The article-length filter in the list header: only articles of one length. It lives in the list's address (`?len=`), so
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
          aria-label={`Article length: ${label}`}
          title={`Article length: ${label}`}
          className={cn("hit inline-flex items-center justify-center rounded-lg hover:bg-selection", scope.length ? "text-accent" : "text-fg")}
        >
          <Icon of={Timer} />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="start" sideOffset={4} popoverWidth="w-60">
          <DropdownMenu.Label className="px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">Article length</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            value={scope.length ?? "any"}
            onValueChange={(v) => navigate(listTo({ ...scope, length: isReadingLength(v) ? v : undefined }), { replace: true })}
          >
            {(["any", ...READING_LENGTHS] as const).map((v) => (
              <DropdownMenu.RadioItem key={v} value={v} className={item}>
                <DropdownMenu.ItemIndicator className="absolute left-3 inline-flex">
                  <Icon of={Check} size="sm" />
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
