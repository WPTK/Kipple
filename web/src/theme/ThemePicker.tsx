import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Collapsible } from "radix-ui";
import { ChevronDown } from "lucide-react";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/cn";
import { inputCls, selectedCard } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";
import { DEFAULT_DAY, DEFAULT_NIGHT, schemeById, type Scheme } from "./schemes";
import { useAllowedSchemes } from "./serverThemes";
import { chooseFixed, choosePair, clockMinutes, themeChoice, toClockTime, type ThemeChoice } from "./settings";
import { themeStore, updateTheme } from "./theme";
import { useLatestRef } from "@/lib/useLatestRef";

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

function Option({ s, name, checked, onSelect }: { s: Scheme; name: string; checked: boolean; onSelect: () => void }) {
  return (
    <label
      className={cn(
        "relative flex min-h-14 cursor-pointer items-center gap-3 rounded-xl border p-2 has-[:focus-visible]:kp-ring",
        checked ? selectedCard : "border-field bg-surface hover:bg-selection",
      )}
    >
      <input type="radio" name={name} className="sr-only-live" checked={checked} onChange={onSelect} />
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

/** A clock time ("HH:MM") in the reader's own format, e.g. 9:00 PM or 21:00. */
function formatClock(t: string): string {
  const m = clockMinutes(t);
  return new Date(2000, 0, 1, Math.floor(m / 60), m % 60).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

/**
 * A 24-hour "HH:MM" time field. The draft is local, and a time is saved only when it is committed: Enter, leaving the
 * field (closing the picker on a phone does that), Settings closing, the page going to the background or being left. A browser reports complete
 * but unintended times on the way to the one wanted (typing 22:30 passes 02:00; a desktop picker reports each column
 * click), so saving each change would flip the theme and sync those. Leaving with an incomplete time restores the
 * saved one.
 */
function TimeField({ label, value, onChange }: { label: string; value: string; onChange: (t: string) => void }) {
  const id = useId();
  const [draft, setDraft] = useState(value);
  // A change from elsewhere (another tab, the server) replaces the draft; adjusted during render, not in an effect.
  const [seen, setSeen] = useState(value);
  if (seen !== value) {
    setSeen(value);
    setDraft(value);
  }
  // The latest props, for the saves that outlive the render that started them (unmount, page hide).
  const latest = useLatestRef({ value, onChange });
  // The time waiting to be saved, with the saved value it replaces: if that changed elsewhere meanwhile (another tab,
  // the server), the newer value wins and the waiting time is dropped.
  const pending = useRef<{ t: string; from: string } | null>(null);
  const flush = useCallback(() => {
    const p = pending.current;
    pending.current = null;
    if (p && p.from === latest.current.value) latest.current.onChange(p.t);
  }, [latest]);
  // Closing Settings, or leaving the page, with a time still waiting saves it. A phone may discard a page in the
  // background without pagehide, so going to the background (visibility hidden) saves too.
  useEffect(() => {
    const hidden = () => {
      if (document.visibilityState === "hidden") flush();
    };
    window.addEventListener("pagehide", flush);
    document.addEventListener("visibilitychange", hidden);
    return () => {
      window.removeEventListener("pagehide", flush);
      document.removeEventListener("visibilitychange", hidden);
      flush();
    };
  }, [flush]);

  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <input
        id={id}
        type="time"
        required
        value={draft}
        onKeyDown={(e) => {
          if (e.key === "Enter") flush();
        }}
        onChange={(e) => {
          setDraft(e.target.value);
          const t = toClockTime(e.target.value);
          pending.current = t && t !== value ? { t, from: value } : null;
        }}
        onBlur={() => {
          flush();
          if (!toClockTime(draft)) setDraft(value);
        }}
        className={inputCls}
      />
    </div>
  );
}

/**
 * Swatches for choosing one scheme: the featured ones, then the rest (and the accessibility schemes) under "More
 * themes", which starts open when the chosen scheme is in it. `legend` names the group for a screen reader.
 */
function SchemeGrid({ legend, name, value, schemes, onPick }: { legend: string; name: string; value: string; schemes: Scheme[]; onPick: (id: string) => void }) {
  const featured = schemes.filter((s) => s.featured);
  const rest = schemes.filter((s) => !s.featured);
  const [open, setOpen] = useState(rest.some((s) => s.id === value));
  const grid = (list: Scheme[]) => (
    <div className="grid grid-cols-2 gap-2">
      {list.map((s) => (
        <Option key={s.id} s={s} name={name} checked={value === s.id} onSelect={() => onPick(s.id)} />
      ))}
    </div>
  );
  return (
    <fieldset className="min-w-0">
      <legend className="mb-2 text-sm font-semibold">{legend}</legend>
      {grid(featured)}
      {rest.length > 0 ? (
        <Fold label="More themes" open={open} onOpenChange={setOpen}>
          <div className="mt-1">{grid(rest)}</div>
        </Fold>
      ) : null}
    </fieldset>
  );
}

const MODES: { value: ThemeChoice; label: string }[] = [
  { value: "fixed", label: "One theme" },
  { value: "follow", label: "Follow device" },
  { value: "schedule", label: "On a schedule" },
];

/**
 * The mode first (One theme, Follow device, On a schedule), then swatches: one grid for a single theme, or a Day and a
 * Night grid for the two modes that switch between a pair (default Paper by day, Midnight by night). The schedule
 * switches at two local times of day. Any scheme may be a day or a night pick.
 */
export function ThemePicker() {
  const stored = useStore(themeStore);
  const schemes = useAllowedSchemes();
  // The server may narrow the allowed schemes below what this device stored. A stored id that is not offered is
  // shown as the default scheme (never a picker with nothing selected).
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
  const dayName = schemeById(t.day).name;
  const nightName = schemeById(t.night).name;
  const choice = themeChoice(stored);
  // Equal times never switch: the day theme shows all day, and the note under the fields says why.
  const sameTimes = t.nightStart === t.dayStart;
  const summary =
    choice === "schedule"
      ? sameTimes
        ? `${dayName} all day`
        : `${nightName} from ${formatClock(t.nightStart)} to ${formatClock(t.dayStart)}, ${dayName} the rest of the day`
      : `${dayName} by day, ${nightName} by night`;

  return (
    <div className="flex flex-col gap-4">
      {fellBack ? <p className="text-xs text-fg2">The theme this device had is no longer offered, so the default is shown.</p> : null}
      <Segmented<ThemeChoice>
        legend="Theme"
        value={choice}
        options={MODES}
        onChange={(c) => updateTheme(c === "fixed" ? chooseFixed(t.fixed) : choosePair(c))}
      />
      {choice === "fixed" ? (
        <SchemeGrid legend="Theme colors" name="theme" value={t.fixed} schemes={schemes} onPick={(id) => updateTheme(chooseFixed(id))} />
      ) : (
        <>
          <p className="text-sm text-fg2">{summary}</p>
          <SchemeGrid legend="Day theme" name="theme-day" value={t.day} schemes={schemes} onPick={(day) => updateTheme({ day })} />
          <SchemeGrid legend="Night theme" name="theme-night" value={t.night} schemes={schemes} onPick={(night) => updateTheme({ night })} />
          {choice === "schedule" ? (
            <div className="grid grid-cols-2 gap-3">
              <TimeField label="Day starts" value={t.dayStart} onChange={(dayStart) => updateTheme({ dayStart })} />
              <TimeField label="Night starts" value={t.nightStart} onChange={(nightStart) => updateTheme({ nightStart })} />
              <p className="col-span-2 text-xs text-fg2">
                {sameTimes ? "Day and night start at the same time, so the day theme stays on. " : ""}
                Uses this device&rsquo;s clock, whatever its light or dark setting.
              </p>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
