import type { LucideIcon, LucideProps } from "lucide-react";
import { cn } from "@/lib/cn";

/**
 * A decorative icon at one of the two sizes the interface uses: sm (16 px, beside text) and md (20 px, in a button).
 * Always hidden from assistive technology: whatever it stands for is named by the control around it.
 */
export function Icon({ of: Glyph, size = "md", className, ...rest }: { of: LucideIcon; size?: "sm" | "md" } & Omit<LucideProps, "size" | "aria-hidden">) {
  return <Glyph aria-hidden="true" className={cn(size === "sm" ? "size-4" : "size-5", className)} {...rest} />;
}
