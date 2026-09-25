import { useId } from "react";
import { cn } from "@/lib/cn";

interface Props<T extends string | number> {
  legend: string;
  hint?: string;
  value: T;
  options: readonly { value: T; label: string }[];
  onChange: (v: T) => void;
}

/** Named steps as a segmented radio group (native radios, arrow keys work). */
export function Segmented<T extends string | number>({ legend, hint, value, options, onChange }: Props<T>) {
  const name = useId();
  return (
    <fieldset className="min-w-0">
      <legend className="text-sm font-semibold">{legend}</legend>
      {hint ? <p className="mb-2 text-xs text-fg2">{hint}</p> : <div className="mb-2" />}
      <div className="flex w-full overflow-hidden rounded-xl border border-line bg-surface">
        {options.map((o) => (
          <label
            key={String(o.value)}
            className={cn(
              "relative flex min-h-11 min-w-0 flex-1 cursor-pointer items-center justify-center px-1 text-center text-xs font-medium min-[380px]:text-sm",
              "has-[:focus-visible]:outline-2 has-[:focus-visible]:-outline-offset-2 has-[:focus-visible]:outline-accent",
              o.value === value ? "bg-accent text-bg" : "text-fg hover:bg-selection",
            )}
          >
            <input
              type="radio"
              name={name}
              value={String(o.value)}
              checked={o.value === value}
              onChange={() => onChange(o.value)}
              className="sr-only-live"
            />
            {o.label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
