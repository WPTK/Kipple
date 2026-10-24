import { CircleAlert, CircleCheck, CirclePause } from "lucide-react";
import type { Feed } from "@/api/types";
import { failureReason, statusInfo } from "@/lib/feedStatus";
import { useFeedTimes } from "./FeedRepair";

/** A Feeds screen row's status: the chip, and for a failing feed the reason it fails and when Kipple tries again (two lines at most). */
export function FeedRowStatus({ feed }: { feed: Pick<Feed, "id" | "status" | "last_error" | "last_error_class"> }) {
  if (feed.status === "ok") return null;
  return (
    <>
      <StatusChip status={feed.status} />
      {feed.last_error ? <FailureText feed={feed} /> : null}
    </>
  );
}

/** The reason in reader words, then "Trying again in 1 hour" once Feed health's figures are in. */
function FailureText({ feed }: { feed: Pick<Feed, "id" | "last_error" | "last_error_class"> }) {
  const { retry } = useFeedTimes(feed.id);
  return <span className="line-clamp-2 text-xs break-words text-fg2">{[`${failureReason(feed)}.`, retry ? `${retry}.` : ""].filter(Boolean).join(" ")}</span>;
}

/** Icon and text together: status is never color alone. */
export function StatusChip({ status }: { status: string }) {
  const s = statusInfo(status);
  const Icon = s.tone === "ok" ? CircleCheck : s.tone === "muted" ? CirclePause : CircleAlert;
  return (
    <span className={`inline-flex items-center gap-1 text-xs ${s.tone === "bad" ? "font-semibold text-danger" : "text-fg2"}`}>
      <Icon aria-hidden="true" className="size-4" />
      {s.label}
    </span>
  );
}
