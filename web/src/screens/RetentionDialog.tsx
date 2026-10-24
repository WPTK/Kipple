import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { applyRetention, fetchRetentionPreview, useSettings } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton } from "@/ui/kit";

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

/**
 * "Remove older articles?": asks the server how many articles from how many feeds a trim would remove, and says so before
 * the person agrees. `keep` is the number being chosen for the feeds without a limit of their own, or undefined for
 * "Apply retention now" (the limits as saved). A trim that would remove nothing says so and offers the plain action.
 */
export function RetentionDialog({ keep, apply, onConfirm, onClose }: { keep?: number; apply: boolean; onConfirm: () => void; onClose: () => void }) {
  const preview = useQuery({ queryKey: ["retention", "preview", keep ?? "saved"], queryFn: () => fetchRetentionPreview(keep), staleTime: 0, gcTime: 0, retry: false });
  const settings = useSettings();
  const days = Number(settings.data?.settings.find((s) => s.key === "retention.restore_days")?.value) || 0;
  const n = preview.data?.articles ?? 0;
  const none = preview.isSuccess && n === 0;
  const label = preview.isSuccess && !none ? `Remove ${plural(n, "article", "articles")}` : apply ? "Remove articles" : "Save";
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Remove older articles?"
      description={
        preview.isSuccess
          ? none
            ? "Every feed is already within its limit, so nothing is removed."
            : `About ${plural(n, "article", "articles")} from ${plural(preview.data.feeds, "feed", "feeds")} will be removed now, read or unread. Starred articles stay.${days > 0 ? ` Sync apps can still open the removed articles for ${plural(days, "day", "days")}.` : ""}`
          : "Articles beyond each feed's limit are removed now, read or unread. Starred articles stay."
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          {apply && none ? null : (
            <Button
              variant="solid"
              disabled={preview.isPending}
              onClick={() => {
                onConfirm();
                onClose();
              }}
            >
              {label}
            </Button>
          )}
        </>
      }
    >
      {preview.isPending ? <Skeleton rows={1} label="Counting articles" /> : null}
      {preview.isError ? <Notice tone="warn">Kipple couldn&apos;t count them first: {errorMessage(preview.error)}</Notice> : null}
    </Modal>
  );
}

/** Library: trims every feed to its limit now, after saying what that removes. */
export function ApplyRetentionNow() {
  const [open, setOpen] = useState(false);
  const run = async () => {
    try {
      await applyRetention();
      toast("Kipple is removing older articles now.");
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <div>
      <Button onClick={() => setOpen(true)}>Apply retention now</Button>
      <p className="mt-1 text-xs text-fg2">Removes older articles right away instead of at the next check.</p>
      {open ? <RetentionDialog apply onConfirm={() => void run()} onClose={() => setOpen(false)} /> : null}
    </div>
  );
}
