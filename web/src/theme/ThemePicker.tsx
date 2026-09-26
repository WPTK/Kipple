import { useId, useState, type ReactNode } from "react";
import { Collapsible } from "radix-ui";
import { ChevronDown } from "lucide-react";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/cn";
import { DEFAULT_DAY, DEFAULT_NIGHT, schemeById, type Scheme } from "./schemes";
import { useAllowedSchemes } from "./serverThemes";
import { themeStore, updateTheme } from "./theme";

const GROUP_LABEL: Record<Scheme["group"], string> = {
  light: "Light",
  color: "Color",
  dark: "Dark",
  accessibility: "Accessibility",
};

function Swatch({ s }: { s: Scheme }) {
  return (
    <span
      aria-hidden="true"
      className="flex size-11 shrink-0 items-center justify-center gap-0.5 rounded-lg border text-sm font-bold"
      style={{ background: s.tokens.bg, color: s.tokens.text, borderColor: s.tokens.border }}
    >
      Aa
      <span className="size-2 rounded-full" style={{ background: s.tokens.accent }} />
    </span>
  );
}

function Option({ s, checked, onSelect }: { s: Scheme; checked: boolean; onSelect: () => void }) {
  return (
    <label
      className={cn(
        "relative flex min-h-14 cursor-pointer items-center gap-3 rounded-xl border p-2 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-accent",
        checked ? "border-accent bg-selection" : "border-line bg-surface hover:bg-selection",
      )}
    >
      <input type="radio" name="theme" className="sr-only-live" checked={checked} onChange={onSelect} />
      <Swatch s={s} />
      <span className="min-w-0">
        <span className="block text-sm font-medium">{s.name}</span>
        {s.note ? <span className="block text-xs text-fg2">{s.note}</span> : null}
      </span>
    </label>
  );
}

function Fold({ label, open, onOpenChange, children }: { label: string; open: boolean; onOpenChange: (o: boolean) => void; children: ReactNode }) {
  return (
    <Collapsible.Root open={open} onOpenChange={onOpenChange} className="mt-3">
      <Collapsible.Trigger className="group flex min-h-11 w-full items-center justify-between rounded-lg px-1 text-sm font-semibold text-fg">
        {label}
        <ChevronDown aria-hidden="true" className="size-5 transition-transform group-data-[state=open]:rotate-180" />
      </Collapsible.Trigger>
      <Collapsible.Content>{children}</Collapsible.Content>
    </Collapsible.Root>
  );
}

function SchemeSelect({ label, value, onChange, schemes }: { label: string; value: string; onChange: (id: string) => void; schemes: Scheme[] }) {
  const id = useId();
  const groups = ["light", "color", "dark", "accessibility"] as const;
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <select
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="min-h-11 rounded-lg border border-line bg-surface px-3 text-base text-fg"
      >
        {groups.map((g) => (
          <optgroup key={g} label={GROUP_LABEL[g]}>
            {schemes.filter((s) => s.group === g).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </div>
  );
}

/**
 * Follow system is the first entry (default pair Paper by day, Midnight by
 * night; both pickers accept any scheme). A short featured list sits on top,
 * the rest under "More themes", and the accessibility group is collapsed.
 */
export function ThemePicker() {
  const stored = useStore(themeStore);
  const schemes = useAllowedSchemes();
  // The server may narrow the allowed schemes below what this device stored. A stored id that is not offered is
  // shown as the default scheme (never a picker with nothing selected, or a select showing a different option
  // than the one in force).
  const offered = (id: string) => schemes.some((s) => s.id === id);
  const first = schemes[0]?.id ?? DEFAULT_DAY;
  const fallbackOf = (want: string) => (offered(want) ? want : first);
  const t = {
    ...stored,
    fixed: offered(stored.fixed) ? stored.fixed : fallbackOf(DEFAULT_DAY),
    day: offered(stored.day) ? stored.day : fallbackOf(DEFAULT_DAY),
    night: offered(stored.night) ? stored.night : fallbackOf(DEFAULT_NIGHT),
  };
  const fellBack = stored.mode === "fixed" ? !offered(stored.fixed) : !offered(stored.day) || !offered(stored.night);
  const featured = schemes.filter((s) => s.featured);
  const more = schemes.filter((s) => !s.featured && s.group !== "accessibility");
  const access = schemes.filter((s) => s.group === "accessibility");
  const inMore = t.mode === "fixed" && more.some((s) => s.id === t.fixed);
  const inAccess = t.mode === "fixed" && access.some((s) => s.id === t.fixed);
  const [moreOpen, setMoreOpen] = useState(inMore);
  const [accessOpen, setAccessOpen] = useState(inAccess);
  const pick = (id: string) => updateTheme({ mode: "fixed", fixed: id });

  return (
    <fieldset className="min-w-0">
      <legend className="mb-2 text-sm font-semibold">Theme</legend>
      {fellBack ? <p className="mb-2 text-xs text-fg2">The theme this device had is no longer offered, so the default is shown.</p> : null}
      <div className="grid grid-cols-2 gap-2">
        <label
          className={cn(
            "relative flex min-h-14 cursor-pointer items-center gap-3 rounded-xl border p-2 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-accent",
            t.mode === "follow" ? "border-accent bg-selection" : "border-line bg-surface hover:bg-selection",
          )}
        >
          <input type="radio" name="theme" className="sr-only-live" checked={t.mode === "follow"} onChange={() => updateTheme({ mode: "follow" })} />
          <span aria-hidden="true" className="flex size-11 shrink-0 overflow-hidden rounded-lg border border-line">
            <span className="flex-1" style={{ background: schemeById(t.day).tokens.bg }} />
            <span className="flex-1" style={{ background: schemeById(t.night).tokens.bg }} />
          </span>
          <span className="min-w-0">
            <span className="block text-sm font-medium">Follow system</span>
            <span className="block text-xs text-fg2">
              {schemeById(t.day).name} by day, {schemeById(t.night).name} by night
            </span>
          </span>
        </label>
        {featured.map((s) => (
          <Option key={s.id} s={s} checked={t.mode === "fixed" && t.fixed === s.id} onSelect={() => pick(s.id)} />
        ))}
      </div>

      {t.mode === "follow" ? (
        <div className="mt-3 grid grid-cols-2 gap-3">
          <SchemeSelect label="Day theme" value={t.day} onChange={(id) => updateTheme({ day: id })} schemes={schemes} />
          <SchemeSelect label="Night theme" value={t.night} onChange={(id) => updateTheme({ night: id })} schemes={schemes} />
        </div>
      ) : null}

      <Fold label="More themes" open={moreOpen} onOpenChange={setMoreOpen}>
        <div className="mt-1 grid grid-cols-2 gap-2">
          {more.map((s) => (
            <Option key={s.id} s={s} checked={t.mode === "fixed" && t.fixed === s.id} onSelect={() => pick(s.id)} />
          ))}
        </div>
      </Fold>
      <Fold label="Accessibility themes" open={accessOpen} onOpenChange={setAccessOpen}>
        <div className="mt-1 grid grid-cols-2 gap-2">
          {access.map((s) => (
            <Option key={s.id} s={s} checked={t.mode === "fixed" && t.fixed === s.id} onSelect={() => pick(s.id)} />
          ))}
        </div>
      </Fold>
    </fieldset>
  );
}
