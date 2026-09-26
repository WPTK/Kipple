import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { clearImgCache, hitRate, imgCacheKey, useImgCache } from "@/api/imgcache";
import { errorMessage } from "@/api/client";
import { bytesLabel, whenLabel } from "@/lib/format";
import { announce, toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton } from "@/ui/kit";

/**
 * The image cache stats card under "Image cache size": used against the cap as a bar, how many files, the hit rate
 * since the server started, a warning when the data volume is nearly full, and "Clear image cache". The stats are
 * read afresh whenever the section opens, and again after the cap or the mode is changed above (`watch`), because a
 * lower cap evicts at once and 0 turns the cache off.
 */
export function ImageCachePanel({ watch }: { watch: string }) {
  const qc = useQueryClient();
  const stats = useImgCache();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A changed cap or mode: look again after the server has applied it.
  useEffect(() => {
    const h = setTimeout(() => void qc.invalidateQueries({ queryKey: imgCacheKey }), 800);
    return () => clearTimeout(h);
  }, [watch, qc]);

  const clear = async () => {
    setBusy(true);
    setError(null);
    try {
      const { cleared } = await clearImgCache();
      setConfirm(false);
      toast(cleared === 1 ? "Cleared 1 image from the cache" : `Cleared ${cleared.toLocaleString()} images from the cache`);
      announce("Image cache cleared");
      void qc.invalidateQueries({ queryKey: imgCacheKey });
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const s = stats.data;
  if (stats.isPending) return <Skeleton rows={1} label="Loading image cache" />;
  if (stats.isError || !s) {
    return (
      <Notice tone="error">
        Couldn't load the image cache. {errorMessage(stats.error)}{" "}
        <Button variant="link" onClick={() => void stats.refetch()}>
          Try again
        </Button>
      </Notice>
    );
  }
  const pct = s.max_bytes > 0 ? Math.min(100, (s.used_bytes / s.max_bytes) * 100) : 0;
  const rate = hitRate(s);
  return (
    <div data-testid="imgcache-card" className="flex flex-col gap-3 rounded-xl border border-line bg-surface p-3">
      {s.low_disk ? (
        <Notice tone="warn">
          The server's disk is nearly full ({bytesLabel(s.disk_free_bytes)} free). Kipple is storing fewer images until there is room again.
        </Notice>
      ) : null}
      {s.enabled ? (
        <>
          <div>
            <p className="text-sm">
              <strong className="font-semibold">{bytesLabel(s.used_bytes)}</strong> of {bytesLabel(s.max_bytes)} used
            </p>
            <div
              role="progressbar"
              aria-label="Image cache used"
              aria-valuemin={0}
              aria-valuemax={s.max_bytes}
              aria-valuenow={Math.min(s.used_bytes, s.max_bytes)}
              aria-valuetext={`${bytesLabel(s.used_bytes)} of ${bytesLabel(s.max_bytes)}`}
              className="mt-1 h-2 w-full overflow-hidden rounded-full bg-line"
            >
              <div className="h-full bg-accent" style={{ width: `${pct}%` }} />
            </div>
          </div>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
            <dt className="text-fg2">Images stored</dt>
            <dd className="tabular-nums">
              {s.entries.toLocaleString()}
              {s.thumbnails ? <span className="text-fg2"> ({s.thumbnails.toLocaleString()} thumbnails)</span> : null}
            </dd>
            <dt className="text-fg2">Served from the cache</dt>
            <dd className="tabular-nums">{rate === null ? "No requests yet" : `${Math.round(rate * 100)}%`}</dd>
          </dl>
          <p className="text-xs text-fg2">Since the server started {whenLabel(s.since).toLowerCase()}. Free space on the disk: {bytesLabel(s.disk_free_bytes)}.</p>
        </>
      ) : (
        <p className="text-sm">The image cache is off. Images are passed through without being stored, so they load slower and use more of the servers that host them.</p>
      )}
      <div>
        <Button onClick={() => setConfirm(true)} disabled={busy || (s.enabled && s.entries === 0 && s.neg_entries === 0)}>
          Clear image cache
        </Button>
      </div>
      {confirm ? (
        <Modal
          open
          onOpenChange={(o) => !o && !busy && (setConfirm(false), setError(null))}
          title="Clear the image cache?"
          description="Every stored image is deleted. They load again from their sites the next time you see them, which is slower for a while. Your articles are not touched."
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirm(false)} disabled={busy}>
                Cancel
              </Button>
              <Button variant="solid" onClick={() => void clear()} disabled={busy}>
                {busy ? "Clearing" : "Clear image cache"}
              </Button>
            </>
          }
        >
          {error ? <Notice tone="error">{error}</Notice> : null}
        </Modal>
      ) : null}
    </div>
  );
}
