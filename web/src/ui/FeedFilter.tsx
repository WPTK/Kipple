import type { KeyboardEvent } from "react";
import { cn } from "@/lib/cn";
import { feedCountText } from "@/lib/feedFilter";
import { inputCls } from "./kit";

/**
 * The "Find a feed" field: a search box whose text keeps the feeds and folders whose names hold it, as you type (the
 * Feeds screen and the sidebar tree both draw it). The matching is `feedMatch` over what is already loaded. Escape
 * clears the text; on an empty field it calls `onEmptyEscape` (the sidebar closes the field there). The result count
 * is announced politely in the Feed health search's words, only while there is text, so an empty field says nothing.
 */
export function FeedFilter({ value, onChange, shown, total, onEmptyEscape, autoFocus, className }: { value: string; onChange: (v: string) => void; shown: number; total: number; onEmptyEscape?: () => void; autoFocus?: boolean; className?: string }) {
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key !== "Escape") return;
    if (value) {
      e.preventDefault();
      e.stopPropagation();
      onChange("");
    } else if (onEmptyEscape) {
      e.preventDefault();
      e.stopPropagation();
      onEmptyEscape();
    }
  };
  return (
    <div className={className}>
      <input
        autoFocus={autoFocus}
        type="search"
        aria-label="Find a feed"
        placeholder="Find a feed"
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        className={cn(inputCls, "min-w-0")}
      />
      <span role="status" className="sr-only-live">
        {value.trim() ? feedCountText(shown, total) : ""}
      </span>
    </div>
  );
}
