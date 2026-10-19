import { CircleAlert, CircleCheck, CirclePause } from "lucide-react";
import { statusInfo } from "@/lib/feedStatus";

/** A Feeds screen row's status: the chip, and for a failing feed the reason it fails (two lines at most; the feed's empty list and Feed health show it whole). */
export function FeedRowStatus({ status, error }: { status: string; error?: string | null }) {
  if (status === "ok") return null;
  return (
    <>
      <StatusChip status={status} />
      {error ? (
        <span className="line-clamp-2 text-xs break-words text-fg2" title={error}>
          {error}
        </span>
      ) : null}
    </>
  );
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
