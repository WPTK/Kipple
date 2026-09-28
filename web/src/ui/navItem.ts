import { cn } from "@/lib/cn";

/** A navigation row: the app's sidebar and the Settings rail share it so they always look the same. */
export const navItemClass = ({ isActive }: { isActive: boolean }): string =>
  cn("flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm font-medium hover:bg-selection", isActive && "bg-selection");
