import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The type scale's own sizes (--text-* in index.css) are font sizes: a text color after one must not drop it.
const twMerge = extendTailwindMerge({ extend: { classGroups: { "font-size": [{ text: ["micro", "title", "display"] }] } } });

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
