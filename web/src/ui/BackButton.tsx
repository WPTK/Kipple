import { Link, useNavigate } from "react-router";
import { ChevronLeft } from "lucide-react";
import { buttonVariants } from "./button";

/**
 * The way up from a screen that sits inside another: the chevron before its title (a Settings group, Feed health,
 * Your year). One pattern for all of them, so the way back is in the same place, looks the same and does the same:
 * it goes back in the history when the entry before this one is in the app (so it lands where you were, scroll and
 * all), and to the parent screen (`to`) when this is the first entry (a bookmark, a reload in a new tab). It is a link
 * to the parent, so it can also be opened in a new tab. The shell moves focus to the screen's heading on arrival.
 */
export function BackButton({ ariaLabel, to }: { ariaLabel: string; to: string }) {
  const navigate = useNavigate();
  return (
    <Link
      to={to}
      aria-label={ariaLabel}
      title={ariaLabel}
      className={buttonVariants({ variant: "ghost", size: "icon" })}
      onClick={(e) => {
        const idx = (window.history.state as { idx?: number } | null)?.idx ?? 0;
        if (idx <= 0 || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        navigate(-1);
      }}
    >
      <ChevronLeft aria-hidden="true" />
    </Link>
  );
}
