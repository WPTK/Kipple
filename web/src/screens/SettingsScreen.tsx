import { useEffect, useId, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useSettings, type SettingGroup, type SettingMeta } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { LAYOUT_IDS, LAYOUT_LABELS, updateDevicePrefs, useDevicePrefs, type LayoutId } from "@/lib/devicePrefs";
import { MOTIONS, MOTION_LABELS, RATES, prefsStore, updatePrefs, type Motion } from "@/lib/prefs";
import { listVoices, speechSupported } from "@/lib/speech";
import { useStore } from "@/lib/store";
import { ThemePicker } from "@/theme/ThemePicker";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { Disclosure, Notice, Skeleton, Switch, inputCls } from "@/ui/kit";
import { DensityControl, FontSelect, SpacingControl, TextSizeControl } from "./AppearanceControls";
import { AccountActions } from "./AccountSection";
import { SettingField } from "./SettingField";

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="border-b border-line px-4 py-5">
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
];

/** Settings from the server, grouped by their `group`. Advanced is collapsed. */
export function ServerSettings({ settings }: { settings: SettingMeta[] }) {
  const shown = settings.filter((s) => s.surface === "settings" && !SPECIAL.has(s.key) && s.kind !== "json");
  const by = (g: SettingGroup) => shown.filter((s) => s.group === g);
  const advanced = by("advanced");
  return (
    <>
      {GROUPS.map(({ id, title }) =>
        by(id).length ? (
          <Section key={id} title={title}>
            {by(id).map((s) => (
              <SettingField key={s.key} meta={s} />
            ))}
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

let layoutBeforeTitlesOnly: LayoutId = "magazine";

function AccessibilitySection({ scrollSetting, loading }: { scrollSetting: SettingMeta | undefined; loading: boolean }) {
  const p = useStore(prefsStore);
  const dp = useDevicePrefs();
  const supported = speechSupported();
  const titlesOnly = dp.layout === "headlines";
  return (
    <Section title="Accessibility">
      <p className="text-sm text-fg2">Kipple follows your device's text, motion and contrast settings. These are extra controls for this device.</p>
      <TextSizeControl />
      <Switch
        label="Easy-to-read font"
        help="Switches to Atkinson Hyperlegible Next, a font designed for clear letter shapes."
        checked={p.font === "easy"}
        onChange={(v) => updatePrefs({ font: v ? "easy" : "default" })}
      />
      <SpacingControl />
      <Segmented<Motion>
        legend="Reduce motion"
        hint="Turns off slides and fades. Follow system uses your device setting."
        value={p.motion}
        onChange={(motion) => updatePrefs({ motion })}
        options={MOTIONS.map((m) => ({ value: m, label: MOTION_LABELS[m] }))}
      />
      {scrollSetting ? <SettingField meta={scrollSetting} /> : loading ? <Skeleton rows={1} label="Loading setting" /> : null}
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
            layoutBeforeTitlesOnly = dp.layout;
            updateDevicePrefs({ layout: "headlines" });
          } else updateDevicePrefs({ layout: layoutBeforeTitlesOnly === "headlines" ? "magazine" : layoutBeforeTitlesOnly });
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
  const scrollSetting = settings.data?.settings.find((s) => s.key === "ui.mark_read_on_scroll");

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <h1 className="pt-2 text-xl font-bold" tabIndex={-1} data-route-heading>
          Settings
        </h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <Section title="Appearance">
          <p className="text-sm text-fg2">Saved on this device only.</p>
          <ThemePicker />
          <FontSelect />
          <TextSizeControl />
          <DensityControl />
        </Section>

        <AccessibilitySection scrollSetting={scrollSetting} loading={settings.isPending} />

        <Section title="Lists">
          <Segmented<LayoutId>
            legend="Layout"
            hint="The default for this device. Each feed and folder can override it from the layout button in its list."
            value={dp.layout}
            onChange={(layout) => updateDevicePrefs({ layout })}
            options={LAYOUT_IDS.map((id) => ({ value: id, label: LAYOUT_LABELS[id] }))}
          />
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
              <span className="block text-xs text-fg2">j and k move, s stars, m marks read, o opens the original. Turn off if letter keys clash with assistive technology.</span>
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

        <Section title="Account">
          {settings.data?.settings
            .filter((s) => s.group === "account" && s.surface === "settings" && s.kind !== "json")
            .map((s) => <SettingField key={s.key} meta={s} />)}
          <AccountActions />
        </Section>
      </div>
    </div>
  );
}
