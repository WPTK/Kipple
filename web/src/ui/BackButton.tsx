import { Link } from "react-router";
import { ChevronLeft } from "lucide-react";
import { Button, buttonVariants } from "./button";

/**
 * The way up from a screen that sits inside another: the chevron before its title (a Settings group, Feed health,
 * Your year). One pattern for all of them, so the way back is in the same place and looks the same everywhere. A link
 * when the parent has an address (`to`), a button when the way back depends on where you came from (`onClick`).
 */
export function BackButton({ ariaLabel, to, onClick }: { ariaLabel: string; to?: string; onClick?: () => void }) {
  if (to) {
    return (
      <Link to={to} aria-label={ariaLabel} title={ariaLabel} className={buttonVariants({ variant: "ghost", size: "icon" })}>
        <ChevronLeft aria-hidden="true" />
      </Link>
    );
  }
  return (
    <Button variant="ghost" size="icon" onClick={onClick} aria-label={ariaLabel} title={ariaLabel}>
      <ChevronLeft aria-hidden="true" />
    </Button>
  );
}
