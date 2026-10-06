import { useId } from "react";
import { Popover } from "radix-ui";
import { CaseSensitive } from "lucide-react";
import { useSettings } from "@/api/admin";
import { FONTS, FONT_GROUPS, fontById } from "@/lib/fonts";
import {
  SPACINGS,
  SPACING_LABELS,
  STEPS,
  STEP_LABELS,
  TEXT_SIZES,
  TEXT_SIZE_LABELS,
  prefsStore,
  updatePrefs,
  type Spacing,
  type Step,
} from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { useAllowedSchemes } from "@/theme/serverThemes";
import { themeStore, updateTheme } from "@/theme/theme";
import { chooseFixed, choosePair, themeChoice } from "@/theme/settings";
import { Segmented } from "@/ui/segmented";
import { Disclosure, Switch, inputCls } from "@/ui/kit";
import { updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { cn } from "@/lib/cn";

const stepOptions = STEPS.map((s) => ({ value: s, label: STEP_LABELS[s] }));

/** Label and help for a reader-menu setting come from the server metadata, with a local fallback. */
function useMeta(key: string, label: string, help?: string) {
  const s = useSettings().data?.settings.find((m) => m.key === key);
  return { label: s?.label ?? label, help: s?.description ?? help };
}

/**
 * Live sample of the chosen steps: a list row and a short article with a heading, a quote and a link, scoped
 * by data attributes. It has a FIXED height and is painted in its own layout box, so a step that makes the
 * text taller or shorter can never move the controls above or below it.
 */
export function DensityPreview({ list, reading }: { list: Step; reading: Step }) {
  return (
    <div
      aria-hidden="true"
      className="h-[27rem] overflow-hidden rounded-xl border border-line bg-bg [contain:layout_paint]"
      data-testid="density-preview"
    >
      <div
        data-list-density={list}
        className="flex min-h-[var(--row-min)] gap-3 border-b border-line px-4 py-[var(--row-py)]"
        style={{ fontFamily: "var(--kp-app-font, var(--font-sans))" }}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-[var(--row-gap)]">
          <div className="text-xs text-fg2">The Ubiquitous Gazette · 2h</div>
          <div className="clamp-title text-base leading-snug font-bold">Toaster files formal grievance against the kitchen, cites "a pattern of bread"</div>
          <div className="clamp-snippet text-sm leading-snug text-fg2">
            Officials confirm the appliance has hired counsel, a second toaster, who has never been seen to toast anything.
          </div>
        </div>
        <div className="size-[var(--thumb)] shrink-0 rounded-lg bg-surface" />
      </div>
      <div data-reading-density={reading} className="article-body px-4 py-3 text-base">
        <h3>Toaster files formal grievance against the kitchen</h3>
        <p className="text-sm text-fg2">By A. Reasonable Person · The Ubiquitous Gazette</p>
        <p>
          Officials said on Tuesday that the toaster, a model of no distinction, had submitted a three-page complaint alleging that the
          bread was not what it used to be. The kitchen declined to comment, adding that it had never met the toaster.
        </p>
        <blockquote>"I have been popping since before you were born," the toaster wrote. "That was not a metaphor."</blockquote>
        <p>
          This newscast wishes to correct itself: the toaster is a coffee machine, the complaint is a recipe, and the spokesman is,
          as of this sentence, a lamp. <span className="text-link underline">Read the full statement</span>.
        </p>
      </div>
    </div>
  );
}

/** A compact theme picker for the reading menu: Match my device and On a schedule first, then every scheme by group. */
export function ThemeSelect() {
  const t = useStore(themeStore);
  const id = useId();
  const schemes = useAllowedSchemes();
  const meta = useMeta("ui.theme", "Theme");
  const choice = themeChoice(t);
  const value = choice === "fixed" ? t.fixed : `__${choice}`;
  const groups = [
    ["light", "Light"],
    ["color", "Color"],
    ["dark", "Dark"],
    ["accessibility", "Accessibility"],
  ] as const;
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-semibold">
        {meta.label}
      </label>
      <select
        id={id}
        value={value}
        onChange={(e) => {
          const v = e.target.value;
          updateTheme(v === "__follow" ? choosePair("follow") : v === "__schedule" ? choosePair("schedule") : chooseFixed(v));
        }}
        className={inputCls}
      >
        <option value="__follow">Match my device</option>
        <option value="__schedule">On a schedule</option>
        {groups.map(([g, label]) => (
          <optgroup key={g} label={label}>
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

/** The sentence every font preview shows: it has the letters that tell typefaces apart (1 I l 0 O, a g). */
export const FONT_SAMPLE = "Pack my box with five dozen liquor jugs, 1 Il0O.";

/**
 * The reading-font list, grouped as everywhere else (Default, Easy to read, Serif, Sans-serif, Monospace, On this
 * device), with a live preview. `value` and `onChange` are font ids from lib/fonts. The "large" preview is a sample
 * paragraph in a box, for Settings and the setup wizard; the compact one is a single line, for the Aa menu.
 */
export function FontPicker({ value, onChange, label = "Reading font", help, preview = "compact" }: { value: string; onChange: (id: string) => void; label?: string; help?: string; preview?: "compact" | "large" }) {
  const id = useId();
  const helpId = useId();
  const stack = fontById(value).stack ?? "var(--kp-reading-font)";
  return (
    <div className="flex flex-col gap-1" data-testid="font-picker">
      <label htmlFor={id} className="text-sm font-semibold">
        {label}
      </label>
      {help ? (
        <p id={helpId} className="text-xs text-fg2">
          {help}
        </p>
      ) : null}
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)} aria-describedby={help ? helpId : undefined} className={inputCls}>
        {FONT_GROUPS.map((g) => {
          const fonts = FONTS.filter((f) => f.group === g);
          if (fonts.length === 0) return null;
          if (g === "Default" || g === "Easy to read") {
            return fonts.map((f) => (
              <option key={f.id} value={f.id}>
                {g === "Easy to read" ? "Easy to read (Atkinson Hyperlegible Next)" : f.label}
              </option>
            ));
          }
          return (
            <optgroup key={g} label={g}>
              {fonts.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.label}
                </option>
              ))}
            </optgroup>
          );
        })}
      </select>
      {preview === "large" ? (
        <div data-testid="font-preview" aria-hidden="true" className="mt-1 flex flex-col gap-1 rounded-xl border border-line bg-surface px-3 py-2" style={{ fontFamily: stack }}>
          <p className="text-lg leading-snug font-bold">Toaster files formal grievance against the kitchen</p>
          <p className="text-base leading-snug">{FONT_SAMPLE}</p>
        </div>
      ) : (
        <p data-testid="font-preview" className="text-xs text-fg2" style={{ fontFamily: stack }}>
          Sample: {FONT_SAMPLE}
        </p>
      )}
    </div>
  );
}

/** The reading font of this device, with the switch that extends it to lists and the sidebar (Settings and menus always keep the system font). */
export function FontSelect({ preview = "compact" }: { preview?: "compact" | "large" }) {
  const p = useStore(prefsStore);
  const meta = useMeta("ui.font_body", "Reading font");
  return (
    <div className="flex flex-col gap-3">
      <FontPicker
        value={p.font}
        onChange={(font) => updatePrefs({ font })}
        label={meta.label}
        help={preview === "large" ? "Used for article text on this device." : undefined}
        preview={preview}
      />
      <Switch
        label="Use it everywhere"
        help="Also use this font for lists and the sidebar. Settings and menus keep the system font."
        checked={p.fontEverywhere && p.font !== "default"}
        disabled={p.font === "default"}
        onChange={(fontEverywhere) => updatePrefs({ fontEverywhere })}
      />
    </div>
  );
}

export function TextSizeControl() {
  const p = useStore(prefsStore);
  return (
    <Segmented<number>
      legend="Text size"
      hint="Makes all text larger or smaller. Also follows your zoom."
      value={p.textSize}
      onChange={(textSize) => updatePrefs({ textSize })}
      options={TEXT_SIZES.map((v, i) => ({ value: v, label: TEXT_SIZE_LABELS[i] ?? String(v) }))}
    />
  );
}

export function DensityControl({ preview = true }: { preview?: boolean }) {
  const p = useStore(prefsStore);
  return (
    <div className="flex flex-col gap-3">
      <Segmented<Step>
        legend="Density"
        hint="How much fits on screen, and how much space there is in articles."
        value={p.listDensity}
        onChange={(v) => updatePrefs({ listDensity: v, readingDensity: v, adjustSeparately: false })}
        options={stepOptions}
      />
      {preview ? <DensityPreview list={p.listDensity} reading={p.readingDensity} /> : null}
      <Disclosure label="Adjust separately" defaultOpen={p.adjustSeparately}>
        <Segmented<Step>
          legend="Lists"
          hint="How much fits on screen."
          value={p.listDensity}
          onChange={(listDensity) => updatePrefs({ listDensity, adjustSeparately: true })}
          options={stepOptions}
        />
        <Segmented<Step>
          legend="Reading"
          hint="How much space between lines and paragraphs."
          value={p.readingDensity}
          onChange={(readingDensity) => updatePrefs({ readingDensity, adjustSeparately: true })}
          options={stepOptions}
        />
      </Disclosure>
    </div>
  );
}

export function SpacingControl() {
  const p = useStore(prefsStore);
  return (
    <Segmented<Spacing>
      legend="Text spacing"
      hint="Adds extra space between letters, words and lines"
      value={p.spacing}
      onChange={(spacing) => updatePrefs({ spacing })}
      options={SPACINGS.map((s) => ({ value: s, label: SPACING_LABELS[s] }))}
    />
  );
}

/** "Highlight keywords": draw the words of Highlight filters in lists and articles. */
export function HighlightToggle() {
  const on = useDevicePrefs().highlightKeywords;
  return (
    <Switch
      label="Highlight keywords"
      help="Marks the words your Highlight filters look for, in lists and in articles."
      checked={on}
      onChange={(highlightKeywords) => updateDevicePrefs({ highlightKeywords })}
    />
  );
}

/**
 * The Kindle-style "Aa" panel: theme, font, text size and density. Everything applies at once (the page behind is
 * the live preview), is stored per device, and has no sliders. The font chosen here is the same setting as
 * Settings > Appearance & Reading > Reading font: it styles articles, and "Use it everywhere" extends it to the
 * lists and the sidebar (Settings and menus keep the system font). It sits above every list (feeds, folders, Unread, All, Starred,
 * Search) and every article. Text spacing, an accessibility control, lives in Settings so it never looks like a
 * second density picker.
 */
export function ReadingMenu({ className }: { className?: string }) {
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button type="button" aria-label="Reading appearance" title="Theme, font and text size" className={cn("hit inline-flex items-center justify-center rounded-lg text-fg hover:bg-selection", className)}>
          <CaseSensitive className="size-6" aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          sideOffset={4}
          collisionPadding={8}
          aria-label="Reading appearance"
          className="z-50 flex max-h-[80dvh] w-[min(22rem,calc(100vw-1rem))] flex-col gap-4 overflow-y-auto rounded-xl border border-line bg-bg p-4 text-fg shadow-xl"
        >
          <ThemeSelect />
          <FontSelect />
          <TextSizeControl />
          <DensityControl preview={false} />
          <HighlightToggle />
          <p className="text-xs text-fg2">Saved for this device.</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
