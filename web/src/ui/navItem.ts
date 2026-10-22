import { cn } from "@/lib/cn";

/**
 * A navigation row: the app's sidebar and the Settings rail share it so they always look the same. The current row is
 * told apart by more than colour: a 3 px accent bar on its leading edge, semibold text and the selection fill. Hover
 * uses the fill alone.
 */
export const navItemClass = ({ isActive }: { isActive: boolean }): string =>
  cn(
    "relative flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm font-medium hover:bg-selection",
    isActive && "bg-selection font-semibold before:absolute before:inset-y-2 before:left-0 before:w-[3px] before:rounded-full before:bg-accent",
  );
