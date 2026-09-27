import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Collapsible } from "radix-ui";
import { ChevronDown } from "lucide-react";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/cn";
import { inputCls } from "@/ui/kit";
import { DEFAULT_DAY, DEFAULT_NIGHT, schemeById, type Scheme } from "./schemes";
import { useAllowedSchemes } from "./serverThemes";
import { chooseFixed, choosePair, clockMinutes, themeChoice, toClockTime } from "./settings";
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
        className={inputCls}
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

/** A clock time ("HH:MM") in the reader's own format, e.g. 9:00 PM or 21:00. */
function formatClock(t: string): string {
  const m = clockMinutes(t);
  return new Date(2000, 0, 1, Math.floor(m / 60), m % 60).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

/** A day/night pair entry (Follow system, On a schedule): a split swatch of the two picks. */
function PairOption({ title, detail, day, night, checked, onSelect }: { title: string; detail: string; day: string; night: string; checked: boolean; onSelect: () => void }) {
  return (
    <label
      className={cn(
        "relative flex min-h-14 cursor-pointer items-center gap-3 rounded-xl border p-2 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-accent",
        checked ? "border-accent bg-selection" : "border-line bg-surface hover:bg-selection",
      )}
    >
      <input type="radio" name="theme" className="sr-only-live" checked={checked} onChange={onSelect} />
      <span aria-hidden="true" className="flex size-11 shrink-0 overflow-hidden rounded-lg border border-line">
        <span className="flex-1" style={{ background: schemeById(day).tokens.bg }} />
        <span className="flex-1" style={{ background: schemeById(night).tokens.bg }} />
      </span>
      <span className="min-w-0">
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-xs text-fg2">{detail}</span>
      </span>
    </label>
  );
}

/**
 * A 24-hour "HH:MM" time field. The draft is local, and a time is saved only when it is committed: Enter, leaving the
 * field (closing the picker on a phone does that), Settings closing or the page being left. A browser reports complete
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
  const latest = useRef({ value, onChange });
  useEffect(() => {
    latest.current = { value, onChange };
  });
  // The time waiting to be saved, with the saved value it replaces: if that changed elsewhere meanwhile (another tab,
  // the server), the newer value wins and the waiting time is dropped.
  const pending = useRef<{ t: string; from: string } | null>(null);
  const flush = useCallback(() => {
    const p = pending.current;
    pending.current = null;
    if (p && p.from === latest.current.value) latest.current.onChange(p.t);
  }, []);
  // Closing Settings, or leaving the page, with a time still waiting saves it.
  useEffect(() => {
    window.addEventListener("pagehide", flush);
    return () => {
      window.removeEventListener("pagehide", flush);
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
 * Follow system and On a schedule are the first entries (default pair Paper by day, Midnight by night; both
 * pickers accept any scheme, and both modes share them). The schedule switches at two local times of day. A short
 * featured list sits after them, the rest under "More themes", and the accessibility group is collapsed.
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
  const pick = (id: string) => updateTheme(chooseFixed(id));
  const dayName = schemeById(t.day).name;
  const nightName = schemeById(t.night).name;
  const choice = themeChoice(stored);
  // Equal times never switch: the card shows the day theme all day, and the note under the fields says why.
  const sameTimes = t.nightStart === t.dayStart;

  return (
    <fieldset className="min-w-0">
      <legend className="mb-2 text-sm font-semibold">Theme</legend>
      {fellBack ? <p className="mb-2 text-xs text-fg2">The theme this device had is no longer offered, so the default is shown.</p> : null}
      <div className="grid grid-cols-2 gap-2">
        <PairOption
          title="Follow system"
          detail={`${dayName} by day, ${nightName} by night`}
          day={t.day}
          night={t.night}
          checked={choice === "follow"}
          onSelect={() => updateTheme(choosePair("follow"))}
        />
        <PairOption
          title="On a schedule"
          detail={sameTimes ? `${dayName} all day` : `${nightName} from ${formatClock(t.nightStart)} to ${formatClock(t.dayStart)}`}
          day={t.day}
          night={t.night}
          checked={choice === "schedule"}
          onSelect={() => updateTheme(choosePair("schedule"))}
        />
        {featured.map((s) => (
          <Option key={s.id} s={s} checked={t.mode === "fixed" && t.fixed === s.id} onSelect={() => pick(s.id)} />
        ))}
      </div>

      {t.mode !== "fixed" ? (
        <div className="mt-3 grid grid-cols-2 gap-3">
          <SchemeSelect label="Day theme" value={t.day} onChange={(id) => updateTheme({ day: id })} schemes={schemes} />
          <SchemeSelect label="Night theme" value={t.night} onChange={(id) => updateTheme({ night: id })} schemes={schemes} />
          {choice === "schedule" ? (
            <>
              <TimeField label="Day starts" value={t.dayStart} onChange={(dayStart) => updateTheme({ dayStart })} />
              <TimeField label="Night starts" value={t.nightStart} onChange={(nightStart) => updateTheme({ nightStart })} />
              <p className="col-span-2 text-xs text-fg2">
                {sameTimes ? "Day and night start at the same time, so the day theme stays on. " : ""}
                Uses this device&rsquo;s clock, whatever its light or dark setting.
              </p>
            </>
          ) : null}
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
