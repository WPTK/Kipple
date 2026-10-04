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

export function Button({ className, variant, size, type = "button", ...props }: ButtonProps) {
  return <button type={type} className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
