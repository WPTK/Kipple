import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useSettings, type SettingGroup, type SettingMeta } from "@/api/admin";
import { AUTO_READ_PRESETS, autoReadLabel } from "@/api/autoRead";
import { errorMessage } from "@/api/client";
import {
  ARTICLE_WIDTHS,
  ARTICLE_WIDTH_LABELS,
  LAYOUT_IDS,
  LAYOUT_LABELS,
  UNREAD_BADGES,
  updateDevicePrefs,
  useDevicePrefs,
  type ArticleWidth,
  type LayoutId,
  type LinkTarget,
  type UnreadBadge,
} from "@/lib/devicePrefs";
import { resolveLinkTarget } from "@/lib/links";
import { MOTIONS, MOTION_LABELS, RATES, prefsStore, updatePrefs, type Motion } from "@/lib/prefs";
import { listVoices, speechSupported } from "@/lib/speech";
import { useStore } from "@/lib/store";
import { ThemePicker } from "@/theme/ThemePicker";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { Disclosure, Notice, Skeleton, Switch, inputCls } from "@/ui/kit";
import { DensityControl, SpacingControl, TextSizeControl } from "./AppearanceControls";
import { DevicesSection } from "./DevicesSection";
import { FiltersSection } from "./filters/FiltersSection";
import { AccountActions } from "./AccountSection";
import { AutoReadCatchUp } from "./AutoReadCatchUp";
import { ImageCachePanel } from "./ImageCachePanel";
import { SavedSearchesSection } from "./SavedSearchesSection";
import { SettingField } from "./SettingField";
import { StatsDataSection } from "./StatsDataDialogs";
import { loadRange } from "@/lib/statsFormat";

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="border-b border-line py-5">
      <h2 id={id} className="mb-3 text-lg font-bold">
        {title}
      </h2>
      <div className="flex flex-col gap-5">{children}</div>
    </section>
  );
}

/** Keys the screen draws itself (or does not honor yet), so the generic renderer skips them. */
const SPECIAL = new Set(["ui.mark_read_on_scroll", "ui.font_ui"]);

const GROUPS: { id: SettingGroup; title: string }[] = [
  { id: "reading", title: "Reading" },
  { id: "sync", title: "Sync" },
  { id: "library", title: "Library" },
  { id: "images", title: "Images" },
  { id: "stats", title: "Statistics" },
];

/** Number settings that get a row of presets ("Custom" opens the stepper). Everything else is drawn from the metadata alone. */
export const PRESETS: Record<string, readonly { value: number; label: string }[]> = {
  "library.auto_read_days": AUTO_READ_PRESETS.map((value) => ({ value, label: autoReadLabel(value) })),
  "imgproxy.cache_mb": [
    { value: 256, label: "256 MB" },
    { value: 512, label: "512 MB" },
    { value: 1024, label: "1 GB" },
    { value: 2048, label: "2 GB" },
    { value: 0, label: "Off" },
  ],
};

/** Under "Mark old articles as read after": the previewed catch-up, offered again whenever the number changes. */
function AutoReadPanel({ days }: { days: number }) {
  const first = useRef(days);
  const [changed, setChanged] = useState(false);
  const [whatIf, setWhatIf] = useState("");
  useEffect(() => {
    if (days !== first.current) setChanged(true);
  }, [days]);
  const trial = whatIf.trim() === "" ? undefined : Number(whatIf);
  const valid = trial === undefined || (Number.isInteger(trial) && trial >= 0 && trial <= 365);
  return (
    <div className="flex flex-col gap-2">
      <AutoReadCatchUp
        offer={changed && days > 0}
        days={valid ? trial : undefined}
        blockedReason={valid ? undefined : "Enter a whole number of days from 0 to 365."}
        // A what-if number was never saved: marking with it would clear articles the saved setting keeps.
        runBlocked={valid && trial !== undefined && trial !== days ? "That number is only a preview. Save it as the setting above first, then mark." : undefined}
      />
      <Disclosure label="Try a different number of days">
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-semibold">Preview as if it were set to</span>
          <input
            inputMode="numeric"
            value={whatIf}
            onChange={(e) => setWhatIf(e.target.value)}
            placeholder={`${days} (the saved value)`}
            aria-invalid={valid ? undefined : true}
            className={`${inputCls} w-40`}
          />
          <span className="text-xs text-fg2">Leave empty to use the saved value. Trying a number only counts; it is never saved, and articles are only marked using the saved setting.</span>
        </label>
      </Disclosure>
    </div>
  );
}

/** Settings from the server, grouped by their `group`. Advanced is collapsed. */
export function ServerSettings({ settings }: { settings: SettingMeta[] }) {
  const shown = settings.filter((s) => s.surface === "settings" && !SPECIAL.has(s.key) && s.kind !== "json");
  const by = (g: SettingGroup) => shown.filter((s) => s.group === g);
  const advanced = by("advanced");
  const imgWatch = `${settings.find((s) => s.key === "imgproxy.cache_mb")?.value}|${settings.find((s) => s.key === "imgproxy.mode")?.value}`;
  const extra = (s: SettingMeta): ReactNode =>
    s.key === "library.auto_read_days" ? <AutoReadPanel days={Number(s.value) || 0} /> : s.key === "imgproxy.cache_mb" ? <ImageCachePanel watch={imgWatch} /> : null;
  return (
    <>
      {GROUPS.map(({ id, title }) =>
        by(id).length || id === "stats" ? (
          <Section key={id} title={title}>
            {by(id).map((s) => (
              <div key={s.key} className="flex flex-col gap-3">
                <SettingField meta={s} presets={PRESETS[s.key]} />
                {extra(s)}
              </div>
            ))}
            {id === "stats" ? <StatsDataSection defaultRange={loadRange()} /> : null}
          </Section>
        ) : null,
      )}
      {advanced.length ? (
        <Section title="Advanced">
          <Disclosure label="Show advanced settings">
            {advanced.map((s) => (
              <SettingField key={s.key} meta={s} />
            ))}
          </Disclosure>
        </Section>
      ) : null}
    </>
  );
}

function VoicePicker() {
  const p = useStore(prefsStore);
  const id = useId();
  const [voices, setVoices] = useState(() => listVoices());
  useEffect(() => {
    const load = () => setVoices(listVoices());
    load();
    speechSynthesis.addEventListener?.("voiceschanged", load);
    return () => speechSynthesis.removeEventListener?.("voiceschanged", load);
  }, []);
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-semibold">
        Voice
      </label>
      <select id={id} value={p.voice} onChange={(e) => updatePrefs({ voice: e.target.value })} className={inputCls}>
        <option value="">Device default</option>
        {voices.map((v) => (
          <option key={v.voiceURI} value={v.voiceURI}>
            {v.name} ({v.lang})
          </option>
        ))}
      </select>
      <p className="text-xs text-fg2">Voices come from your device. On iPhone, better voices are in Settings, Accessibility, Spoken Content, Voices.</p>
    </div>
  );
}

function AccessibilitySection({ scrollHelp }: { scrollHelp: string | undefined }) {
  const p = useStore(prefsStore);
  const dp = useDevicePrefs();
  const supported = speechSupported();
  const titlesOnly = dp.layout === "headlines";
  return (
    <Section title="Accessibility">
      <p className="text-sm text-fg2">Kipple follows your device's text, motion and contrast settings. These are extra controls for this device.</p>
      <p className="text-sm text-fg2">
        For a font designed for clear letter shapes, choose Atkinson Hyperlegible Next ("Easy to read") in the Aa menu.
      </p>
      <SpacingControl />
      <Segmented<Motion>
        legend="Reduce motion"
        hint="Turns off slides and fades. Follow system uses your device setting."
        value={p.motion}
        onChange={(motion) => updatePrefs({ motion })}
        options={MOTIONS.map((m) => ({ value: m, label: MOTION_LABELS[m] }))}
      />
      <Switch
        label="Mark articles read as I scroll"
        help={scrollHelp ?? "Articles you scroll past in the list are marked read automatically."}
        checked={p.markReadOnScroll}
        onChange={(markReadOnScroll) => updatePrefs({ markReadOnScroll })}
      />
      <Switch
        label="Listen to articles"
        help={supported ? "Adds a Listen button to articles. Voices come from your device." : "This browser can't read aloud."}
        checked={p.listen && supported}
        disabled={!supported}
        onChange={(listen) => updatePrefs({ listen })}
      />
      {p.listen && supported ? (
        <>
          <VoicePicker />
          <Segmented<number>
            legend="Reading speed"
            value={p.rate}
            onChange={(rate) => updatePrefs({ rate })}
            options={RATES.map((r) => ({ value: r, label: `${r}x` }))}
          />
        </>
      ) : null}
      <Switch
        label="Larger buttons"
        help="Makes buttons and tap targets bigger and spreads them out."
        checked={p.largeTargets}
        onChange={(largeTargets) => updatePrefs({ largeTargets })}
      />
      <Switch
        label="Titles only in lists"
        help="Hides pictures and excerpts so lists show just headlines."
        checked={titlesOnly}
        onChange={(v) => {
          if (v) {
            updateDevicePrefs({ layoutBeforeTitlesOnly: dp.layout === "headlines" ? null : dp.layout, layout: "headlines" });
          } else updateDevicePrefs({ layout: dp.layoutBeforeTitlesOnly ?? "magazine", layoutBeforeTitlesOnly: null });
        }}
      />
    </Section>
  );
}

export function SettingsScreen() {
  const p = useStore(prefsStore);
  const dp = useDevicePrefs();
  const settings = useSettings();
  const switchId = useId();
  const scrollHelp = settings.data?.settings.find((s) => s.key === "ui.mark_read_on_scroll")?.description;

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line">
        <h1 className="mx-auto w-full max-w-[720px] px-4 pt-2 pb-2 text-xl font-bold" tabIndex={-1} data-route-heading>
          Settings
        </h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[720px] px-4 pb-10">
        <Section title="Appearance">
          <p className="text-sm text-fg2">Saved for this device. Change the font from the Aa button in any list or article.</p>
          <ThemePicker />
          <TextSizeControl />
          <DensityControl />
        </Section>

        <AccessibilitySection scrollHelp={scrollHelp} />

        <Section title="Lists and reading">
          <Segmented<LayoutId>
            legend="Layout"
            hint="The default for this device. Each feed and folder can override it from the layout button in its list."
            value={dp.layout}
            onChange={(layout) => updateDevicePrefs({ layout })}
            options={LAYOUT_IDS.map((id) => ({ value: id, label: LAYOUT_LABELS[id] }))}
            wrap
          />
          <Segmented<ArticleWidth>
            legend="Article width"
            hint="How wide the text of an article is. Full uses the whole pane."
            value={dp.articleWidth}
            onChange={(articleWidth) => updateDevicePrefs({ articleWidth })}
            options={ARTICLE_WIDTHS.map((w) => ({ value: w, label: ARTICLE_WIDTH_LABELS[w] }))}
          />
          <Segmented<LinkTarget>
            legend="Open links in"
            hint="Same tab lets a link that an installed app claims (ESPN, YouTube) open in the app cleanly, and Back returns here. It is the default on iPhone and iPad. Undo is not available after you leave Kipple."
            value={resolveLinkTarget(dp.linkTarget)}
            onChange={(linkTarget) => updateDevicePrefs({ linkTarget })}
            options={[
              { value: "new", label: "New tab" },
              { value: "same", label: "Same tab" },
            ]}
          />
          <Segmented<UnreadBadge>
            legend="Unread badge"
            hint="On the Unread tab and in the sidebar. Counts show up to 99+."
            value={dp.unreadBadge}
            onChange={(unreadBadge) => updateDevicePrefs({ unreadBadge })}
            options={UNREAD_BADGES.map((b) => ({ value: b, label: b === "count" ? "Count" : b === "dot" ? "Dot only" : "Off" }))}
          />
          <p className="text-xs text-fg2">Drag the edge of the sidebar or of the article list to resize them on a wide screen. Double-click an edge to reset it.</p>
          <Segmented<"auto" | "off">
            legend="Thumbnails in Inbox"
            value={dp.inboxThumbs}
            onChange={(inboxThumbs) => updateDevicePrefs({ inboxThumbs })}
            options={[
              { value: "auto", label: "Auto" },
              { value: "off", label: "Off" },
            ]}
          />
          <div>
            <Button onClick={() => updateDevicePrefs({ peekSeen: false })} className="self-start">
              Show swipe tips again
            </Button>
            <p className="mt-1 text-xs text-fg2">Swipe a row right to mark it read or unread, left to star it or see more. Every swipe has a button.</p>
          </div>
          <Link to="/feeds" className="text-sm text-link underline underline-offset-2">
            Manage feeds and folders
          </Link>
        </Section>

        <Section title="Keyboard">
          <label htmlFor={switchId} className="flex min-h-11 items-start gap-3 text-sm">
            <input
              id={switchId}
              type="checkbox"
              checked={p.shortcuts}
              onChange={(e) => updatePrefs({ shortcuts: e.target.checked })}
              className="mt-0.5 size-5 accent-[var(--kp-accent)]"
            />
            <span>
              Single-key shortcuts
              <span className="block text-xs text-fg2">j and k move, s stars, m marks read, o opens the original. Off by default on a phone or tablet with no keyboard; turn it off if letter keys clash with assistive technology.</span>
            </span>
          </label>
        </Section>

        {settings.isPending ? <Skeleton rows={3} label="Loading settings" /> : null}
        {settings.isError ? (
          <div className="px-4 py-5">
            <Notice tone="error">
              Couldn't load your settings. {errorMessage(settings.error)}{" "}
              <Button variant="link" onClick={() => void settings.refetch()}>
                Try again
              </Button>
            </Notice>
          </div>
        ) : null}
        {settings.data ? <ServerSettings settings={settings.data.settings} /> : null}

        <Section title="Filters">
          <FiltersSection />
        </Section>

        <Section title="Saved searches">
          <SavedSearchesSection />
        </Section>

        <Section title="Devices">
          <DevicesSection />
        </Section>

        <Section title="Account">
          {settings.data?.settings
            .filter((s) => s.group === "account" && s.surface === "settings" && s.kind !== "json")
            .map((s) => <SettingField key={s.key} meta={s} />)}
          <AccountActions />
        </Section>
        </div>
      </div>
    </div>
  );
}
