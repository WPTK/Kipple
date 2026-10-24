import type { ReactNode } from "react";
import { Link } from "react-router";
import { listTo } from "@/lib/routes";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { buttonVariants } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";

/**
 * Something the address names is not there: a page, a feed, a folder, an article. The noun sets the tab title and the
 * heading ("Feed not found"); the sentence says what to do about it, never the raw address. `action` replaces the
 * way out when the screen has a better one.
 */
export function NotFoundBlock({ noun, why = "It may have been deleted, or the address is out of date.", action }: { noun: string; why?: string; action?: Exclude<ReactNode, undefined> }) {
  const title = `${noun.charAt(0).toUpperCase()}${noun.slice(1)} not found`;
  useDocumentTitle(title);
  return (
    <EmptyState
      role="alert"
      asH1
      title={title}
      why={why}
      action={
        action !== undefined ? (
          action
        ) : (
          <Link to={listTo({ view: "unread" })} replace className={buttonVariants({ variant: "solid" })}>
            Go to Unread
          </Link>
        )
      }
    />
  );
}
