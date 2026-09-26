import { CircleHelp } from "lucide-react";
import { Popover } from "radix-ui";

/** The search syntax in a small popover beside the box (docs/design.md 2.4). */
export function SearchHelp() {
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button type="button" aria-label="Search tips" className="hit inline-flex items-center justify-center rounded-lg text-fg2 hover:bg-selection hover:text-fg">
          <CircleHelp className="size-5" aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          sideOffset={4}
          collisionPadding={8}
          aria-label="Search tips"
          className="z-50 w-[min(22rem,calc(100vw-1rem))] rounded-xl border border-line bg-bg p-4 text-sm text-fg shadow-xl"
        >
          <p className="font-semibold">Search tips</p>
          <ul className="mt-2 flex flex-col gap-1.5 text-fg2">
            <li>
              <code className="font-mono text-fg">&quot;exact phrase&quot;</code> matches the words together
            </li>
            <li>
              <code className="font-mono text-fg">-exclude</code> leaves out articles with that word
            </li>
            <li>
              <code className="font-mono text-fg">title:word</code> looks only in titles
            </li>
            <li>
              <code className="font-mono text-fg">author:name</code> looks only at authors
            </li>
            <li>
              <code className="font-mono text-fg">word*</code> matches words that start that way
            </li>
          </ul>
          <p className="mt-2 text-xs text-fg2">When nothing matches exactly, Kipple shows partial matches and says so.</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
