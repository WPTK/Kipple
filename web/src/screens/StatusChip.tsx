import { CircleAlert, CircleCheck, CirclePause } from "lucide-react";
import { statusInfo } from "@/lib/feedStatus";

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
