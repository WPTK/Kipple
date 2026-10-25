import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "@/lib/cn";

// shadcn-style button, hand-written on the scheme tokens. Every size keeps a
// 44 x 44 px minimum hit area (Apple HIG; WCAG 2.5.8 asks for 24).
export const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 rounded-lg text-sm font-medium transition-colors select-none disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 [&_svg]:size-5 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        solid: "bg-accent text-bg hover:opacity-90",
        outline: "border border-line bg-surface text-fg hover:bg-selection",
        ghost: "text-fg hover:bg-selection",
        link: "text-link underline underline-offset-2",
        // A destructive action: the one look for Delete, Forget, Reset and Sign out, and for the confirm button of their dialogs.
        // Its hover underlines the label: danger text on the selection fill is under 4.5:1 in six schemes, so it never sits
        // on it. Not a box-shadow: the focus ring's gap is an unlayered box-shadow and would cancel it while focused.
        danger: "border border-danger bg-surface font-semibold text-danger underline-offset-2 hover:underline",
      },
      size: {
        default: "min-h-11 min-w-11 px-4",
        icon: "size-11",
      },
    },
    defaultVariants: { variant: "outline", size: "default" },
  },
);

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {}

/**
 * `aria-disabled` is how a control that has (or may have) keyboard focus is switched off: a native `disabled` button
 * drops focus to the page the moment it turns off, so Enter again does nothing. It stays focusable and is announced
 * as unavailable, and activating it does nothing (here, for every caller).
 */
export function Button({ className, variant, size, type = "button", onClick, ...props }: ButtonProps) {
  const off = props["aria-disabled"] === true || props["aria-disabled"] === "true";
  return <button type={type} className={cn(buttonVariants({ variant, size }), className)} onClick={off ? (e) => e.preventDefault() : onClick} {...props} />;
}
