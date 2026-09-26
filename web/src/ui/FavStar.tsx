import { Star } from "lucide-react";
import { cn } from "@/lib/cn";

/** The star that pins a folder or a feed to the top of the sidebar. A toggle: pressed when it is a favorite. */
export function FavStar({ on, name, onToggle, className }: { on: boolean; name: string; onToggle: () => void; className?: string }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      aria-label={`Favorite ${name}`}
      title={on ? `Remove ${name} from favorites` : `Add ${name} to favorites`}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onToggle();
      }}
      className={cn(
        "hit-row pointer-events-auto inline-flex shrink-0 items-center justify-center rounded-lg hover:bg-selection",
        on ? "text-star" : "text-fg2",
        className,
      )}
    >
      <Star className="size-4" fill={on ? "currentColor" : "none"} aria-hidden="true" />
    </button>
  );
}
