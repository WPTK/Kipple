import type { Feed } from "@/api/types";

/** What a feed row names it by: its title, and below it the short address when another feed has the same title. */
export function FeedRowName({ feed }: { feed: Pick<Feed, "title" | "address_hint"> }) {
  return (
    <>
      <span className="block truncate">{feed.title}</span>
      {feed.address_hint ? <span className="block truncate text-xs text-fg2">{feed.address_hint}</span> : null}
    </>
  );
}

/** The feed's name for a label: its title, and its short address when another feed has the same title. */
export const feedLabel = (feed: Pick<Feed, "title" | "address_hint">) => (feed.address_hint ? `${feed.title}, ${feed.address_hint}` : feed.title);
