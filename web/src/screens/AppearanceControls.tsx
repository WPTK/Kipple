import { useId } from "react";
import { Popover } from "radix-ui";
import { CaseSensitive } from "lucide-react";
import { useSettings } from "@/api/admin";
import { FONTS, FONT_GROUPS } from "@/lib/fonts";
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
import { SCHEMES } from "@/theme/schemes";
import { themeStore, updateTheme } from "@/theme/theme";
import { Segmented } from "@/ui/segmented";
import { Disclosure, inputCls } from "@/ui/kit";
import { cn } from "@/lib/cn";

const stepOptions = STEPS.map((s) => ({ value: s, label: STEP_LABELS[s] }));

/** Label and help for a reader-menu setting come from the server metadata, with a local fallback. */
function useMeta(key: string, label: string, help?: string) {
  const s = useSettings().data?.settings.find((m) => m.key === key);
  return { label: s?.label ?? label, help: s?.description ?? help };
}

/** Live sample of the chosen steps: a list row and a paragraph, scoped by data attributes. */
export function DensityPreview({ list, reading }: { list: Step; reading: Step }) {
  return (
    <div aria-hidden="true" className="overflow-hidden rounded-xl border border-line bg-bg" data-testid="density-preview">
      <div data-list-density={list} className="flex min-h-[var(--row-min)] gap-3 border-b border-line px-4 py-[var(--row-py)]">
        <div className="flex min-w-0 flex-1 flex-col gap-[var(--row-gap)]">
          <div className="text-xs text-fg2">Example feed · 2h</div>
          <div className="clamp-title text-base leading-snug font-bold">A sample headline that may run onto a second or third line</div>
          <div className="clamp-snippet text-sm leading-snug text-fg2">
            The excerpt shows the first words of the article, so you can decide whether to open it, and grows with the density step.
          </div>
        </div>
        <div className="size-[var(--thumb)] shrink-0 rounded-lg bg-surface" />
      </div>
      <div data-reading-density={reading} className="article-body px-4 py-3 text-base">
        <p>Reading text uses this spacing between lines and paragraphs.</p>
        <p>A second paragraph shows the gap between them.</p>
      </div>
    </div>
  );
}

/** A compact theme picker for the reading menu: Match my device first, then every scheme by group. */
export function ThemeSelect() {
  const t = useStore(themeStore);
  const id = useId();
  const meta = useMeta("ui.theme", "Theme");
  const value = t.mode === "follow" ? "__follow" : t.fixed;
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
        onChange={(e) => (e.target.value === "__follow" ? updateTheme({ mode: "follow" }) : updateTheme({ mode: "fixed", fixed: e.target.value }))}
        className={inputCls}
      >
        <option value="__follow">Match my device</option>
        {groups.map(([g, label]) => (
          <optgroup key={g} label={label}>
            {SCHEMES.filter((s) => s.group === g).map((s) => (
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

export function FontSelect() {
  const p = useStore(prefsStore);
  const id = useId();
  const meta = useMeta("ui.font_body", "Reading font");
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-semibold">
        {meta.label}
      </label>
      <select id={id} value={p.font} onChange={(e) => updatePrefs({ font: e.target.value })} className={inputCls}>
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
      <p className="text-xs text-fg2" style={{ fontFamily: "var(--kp-reading-font)" }}>
        Sample: Pack my box with five dozen liquor jugs, 1 Il0O.
      </p>
    </div>
  );
}

export function TextSizeControl() {
  const p = useStore(prefsStore);
  const meta = useMeta("ui.font_size", "Text size");
  return (
    <Segmented<number>
      legend={meta.label}
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
      legend="Reading spacing"
      hint="Changes space between lines and paragraphs. Normal follows Density."
      value={p.spacing}
      onChange={(spacing) => updatePrefs({ spacing })}
      options={SPACINGS.map((s) => ({ value: s, label: SPACING_LABELS[s] }))}
    />
  );
}

/**
 * The Kindle-style "Aa" panel: theme, font, text size, density and reading spacing. Everything applies at once
 * (the page behind is the live preview), is stored per device, and has no sliders.
 */
export function ReadingMenu({ className }: { className?: string }) {
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button type="button" aria-label="Reading appearance" className={cn("hit inline-flex items-center justify-center rounded-lg text-fg hover:bg-selection", className)}>
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
          <SpacingControl />
          <p className="text-xs text-fg2">Saved on this device only.</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
