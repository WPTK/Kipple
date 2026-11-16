import { Button } from "@/ui/button";
import { cn } from "@/lib/cn";

/** The offer's one action, named with the zone it will save. The label is the control's name. */
export function UseDeviceZone({ zone, onUse, inline, busy, className }: { zone: string; onUse: (zone: string) => void; inline?: boolean; busy?: boolean; className?: string }) {
  return (
    <Button
      variant={inline ? "link" : "outline"}
      className={cn("h-auto max-w-full py-2 text-left", inline && "px-0", className)}
      aria-disabled={busy || undefined}
      onClick={() => onUse(zone)}
    >
      {`Use this device's time zone (${zone})${inline ? "?" : ""}`}
    </Button>
  );
}
