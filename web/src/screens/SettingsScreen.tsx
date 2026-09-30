import { createContext, useContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link, Navigate, Route, Routes, useLocation, useMatch, useNavigate } from "react-router";
import { BarChart3, ChevronLeft, ChevronRight, Filter, Info, Palette, Rss, SlidersHorizontal, UserRound, type LucideIcon } from "lucide-react";
import { useSettings, type SettingGroup, type SettingMeta } from "@/api/admin";
import { AUTO_READ_PRESETS, autoReadLabel } from "@/api/autoRead";
import { errorMessage } from "@/api/client";
import { cn } from "@/lib/cn";
import { everyLabel } from "@/lib/interval";
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
import { fontById } from "@/lib/fonts";
import { resolveLinkTarget } from "@/lib/links";
import { MOTIONS, MOTION_LABELS, RATES, prefsStore, updatePrefs, type Motion } from "@/lib/prefs";
import { listVoices, speechSupported } from "@/lib/speech";
import { useStore } from "@/lib/store";
import { useWide } from "@/lib/useMedia";
import { ThemePicker } from "@/theme/ThemePicker";
import { schemeById } from "@/theme/schemes";
import { themeStore } from "@/theme/theme";
import { navItemClass } from "@/ui/navItem";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { Disclosure, Notice, Skeleton, Switch, inputCls } from "@/ui/kit";
import { DensityControl, FontSelect, SpacingControl, TextSizeControl } from "./AppearanceControls";
import { DevicesSection } from "./DevicesSection";
import { FiltersSection } from "./filters/FiltersSection";
import { AboutSection } from "./AboutSection";
import { AccountActions } from "./AccountSection";
import { AutoReadCatchUp } from "./AutoReadCatchUp";
import { ImageCachePanel } from "./ImageCachePanel";
import { SavedSearchesSection } from "./SavedSearchesSection";
import { SettingField } from "./SettingField";
import { StatsDataSection } from "./StatsDataDialogs";
import { loadRange } from "@/lib/statsFormat";

/*
 * Settings is master-detail (issue #55): seven groups, each at its own URL (/settings/<group>). On a wide screen a rail
 * of the groups sits beside the chosen group's page, and a bare /settings opens the first group. On a narrow screen
 * /settings is the list of groups and a group is a page of its own with a back button.
 */

/** The seven groups, in rail order. `id` is the URL segment. */
export const SETTINGS_GROUPS = [
  { id: "appearance", label: "Appearance & Reading", icon: Palette },
  { id: "sync", label: "Sync & Feeds", icon: Rss },
  { id: "statistics", label: "Statistics", icon: BarChart3 },
  { id: "filters", label: "Filters & Saved Searches", icon: Filter },
  { id: "account", label: "Account & Devices", icon: UserRound },
  { id: "advanced", label: "Advanced", icon: SlidersHorizontal },
  { id: "about", label: "About", icon: Info },
] as const satisfies readonly { id: string; label: string; icon: LucideIcon }[];

export type SettingsGroupId = (typeof SETTINGS_GROUPS)[number]["id"];
type SettingsGroup = (typeof SETTINGS_GROUPS)[number];
const FIRST_GROUP: SettingsGroup = SETTINGS_GROUPS[0];

const groupById = (id: string | undefined) => SETTINGS_GROUPS.find((g) => g.id === id);

/** The heading level of a Section: 2 under a page's h1 (narrow), 3 under the group's h2 beside the rail (wide). */
const SectionLevel = createContext<2 | 3>(2);

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  const Heading = useContext(SectionLevel) === 3 ? "h3" : "h2";
  return (
    <section aria-labelledby={id} className="border-b border-line py-5">
      <Heading id={id} className="mb-3 text-lg font-bold">
        {title}
      </Heading>
      <div className="flex flex-col gap-5">{children}</div>
    </section>
  );
}

/** Keys the screen draws itself (or does not honor yet), so the generic renderer skips them. */
const SPECIAL = new Set(["ui.mark_read_on_scroll", "ui.font_ui"]);

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

/** The settings of one server group shown on the Settings screen, in the server's order. */
function settingsOf(settings: SettingMeta[], group: SettingGroup): SettingMeta[] {
  return settings.filter((s) => s.group === group && s.surface === "settings" && !SPECIAL.has(s.key) && s.kind !== "json");
}

function SettingRows({ list, all }: { list: SettingMeta[]; all: SettingMeta[] }) {
  const imgWatch = `${all.find((s) => s.key === "imgproxy.cache_mb")?.value}|${all.find((s) => s.key === "imgproxy.mode")?.value}`;
  const extra = (s: SettingMeta): ReactNode =>
    s.key === "library.auto_read_days" ? <AutoReadPanel days={Number(s.value) || 0} /> : s.key === "imgproxy.cache_mb" ? <ImageCachePanel watch={imgWatch} /> : null;
  return (
    <>
      {list.map((s) => (
        <div key={s.key} className="flex flex-col gap-3">
          <SettingField meta={s} presets={PRESETS[s.key]} />
          {extra(s)}
        </div>
      ))}
    </>
  );
}

/**
 * The server-driven part of a group page, with its own loading and error states. Each entry is one server group;
 * a `title` draws it as a titled section, no title draws its settings straight under the page heading (for a group
 * whose name the page heading already says). A group with nothing to show is left out; `empty` is said when every
 * one of them is empty (a page with no other content). `inline` draws the rows bare, for use inside a Section.
 * A failed refetch keeps the settings already loaded on screen under the error, as the single page did.
 */
function ServerPart({ groups, empty, inline = false }: { groups: { id: SettingGroup; title?: string }[]; empty?: string; inline?: boolean }) {
  const settings = useSettings();
  if (settings.isPending) return <Skeleton rows={3} label="Loading settings" />;
  const error = settings.isError ? (
    <div className={inline ? undefined : "py-5"}>
      <Notice tone="error">
        Couldn't load your settings. {errorMessage(settings.error)}{" "}
        <Button variant="link" onClick={() => void settings.refetch()}>
          Try again
        </Button>
      </Notice>
    </div>
  ) : null;
  if (!settings.data) return error;
  const all = settings.data.settings;
  const parts = groups.map((g) => ({ ...g, list: settingsOf(all, g.id) })).filter((g) => g.list.length > 0);
  return (
    <>
      {error}
      {parts.length === 0 && empty ? <p className="py-5 text-sm text-fg2">{empty}</p> : null}
      {parts.map((g) =>
        g.title ? (
          <Section key={g.id} title={g.title}>
            <SettingRows list={g.list} all={all} />
          </Section>
        ) : inline ? (
          <SettingRows key={g.id} list={g.list} all={all} />
        ) : (
          <div key={g.id} className="flex flex-col gap-5 border-b border-line py-5">
            <SettingRows list={g.list} all={all} />
          </div>
        ),
      )}
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
        For a font designed for clear letter shapes, choose Atkinson Hyperlegible Next ("Easy to read") as the Reading font under Appearance.
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

/* ---------- The seven group pages ---------- */

function AppearancePage() {
  const p = useStore(prefsStore);
  const dp = useDevicePrefs();
  const settings = useSettings();
  const switchId = useId();
  const scrollHelp = settings.data?.settings.find((s) => s.key === "ui.mark_read_on_scroll")?.description;
  return (
    <>
      <Section title="Appearance">
        <p className="text-sm text-fg2">Saved for this device. The Aa button above any list or article has the same theme, font and text size.</p>
        <ThemePicker />
        <FontSelect preview="large" />
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

      <ServerPart groups={[{ id: "reading", title: "Reading" }]} />
    </>
  );
}

function SyncPage() {
  return (
    <ServerPart
      groups={[
        { id: "sync", title: "Sync" },
        { id: "library", title: "Library" },
        { id: "images", title: "Images" },
      ]}
      empty="No sync or feed settings to change yet."
    />
  );
}

function StatisticsPage() {
  return (
    <>
      <ServerPart groups={[{ id: "stats" }]} />
      {/* A fixed slot after the server part, whether settings are loading, failed or loaded: it never remounts, so an open dialog survives. */}
      <Section title="Your statistics data">
        <StatsDataSection defaultRange={loadRange()} hideTitle />
      </Section>
    </>
  );
}

function FiltersPage() {
  return (
    <>
      <Section title="Filters">
        <FiltersSection />
      </Section>
      <Section title="Saved searches">
        <SavedSearchesSection />
      </Section>
    </>
  );
}

function AccountPage() {
  return (
    <>
      <Section title="Devices">
        <DevicesSection />
      </Section>
      <Section title="Account">
        <ServerPart groups={[{ id: "account" }]} inline />
        <AccountActions />
      </Section>
    </>
  );
}

function AdvancedPage() {
  return (
    <>
      <p className="pt-5 text-sm text-fg2">Most people never need to change these.</p>
      <ServerPart groups={[{ id: "advanced" }]} empty="No advanced settings to change yet." />
    </>
  );
}

function AboutPage() {
  return (
    <Section title="About Kipple">
      <AboutSection />
    </Section>
  );
}

const PAGES: Record<SettingsGroupId, () => ReactNode> = {
  appearance: AppearancePage,
  sync: SyncPage,
  statistics: StatisticsPage,
  filters: FiltersPage,
  account: AccountPage,
  advanced: AdvancedPage,
  about: AboutPage,
};

/* ---------- Group list previews ---------- */

/** The one-line current value under a group in the narrow list, for the groups where it is cheap to say. */
function useGroupPreviews(): Partial<Record<SettingsGroupId, string>> {
  const t = useStore(themeStore);
  const p = useStore(prefsStore);
  const settings = useSettings();
  const value = (key: string) => settings.data?.settings.find((s) => s.key === key)?.value;
  const theme = t.mode === "fixed" ? schemeById(t.fixed).name : `${schemeById(t.day).name} / ${schemeById(t.night).name}`;
  const font = fontById(p.font);
  const out: Partial<Record<SettingsGroupId, string>> = {
    appearance: `${theme} · ${font.id === "default" ? "Default font" : font.label}`,
  };
  const interval = value("refresh.interval_minutes");
  if (typeof interval === "number" && interval > 0) out.sync = everyLabel(interval);
  const stats = value("stats.enabled");
  if (typeof stats === "boolean") out.statistics = stats ? "Recording on" : "Recording off";
  return out;
}

/* ---------- Navigation ---------- */

/** Wide: the rail of groups beside the page, with the app sidebar's own item look. */
function Rail({ shown }: { shown: SettingsGroupId }) {
  return (
    <nav aria-label="Settings sections" className="w-60 shrink-0 overflow-y-auto border-r border-line px-2 py-3">
      <ul className="flex flex-col gap-1">
        {SETTINGS_GROUPS.map(({ id, label, icon: Icon }) => (
          <li key={id}>
            {/* Active by the group shown, not the URL: a bare /settings shows the first group. */}
            <Link to={`/settings/${id}`} aria-current={shown === id ? "page" : undefined} className={navItemClass({ isActive: shown === id })}>
              <Icon aria-hidden="true" className="size-5 shrink-0" />
              {label}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** Narrow: the list of groups, each with its current value where that is cheap. Focus returns to the group just left. */
function GroupList({ returnTo }: { returnTo: SettingsGroupId | undefined }) {
  const previews = useGroupPreviews();
  return (
    <nav aria-label="Settings sections" className="mx-auto w-full max-w-[720px] px-4 py-3">
      <ul className="flex flex-col">
        {SETTINGS_GROUPS.map(({ id, label, icon: Icon }) => (
          <li key={id} className="border-b border-line last:border-b-0">
            <Link
              to={`/settings/${id}`}
              state={{ fromSettingsList: true }}
              data-route-focus={returnTo === id ? "" : undefined}
              className="-mx-2 flex min-h-14 items-center gap-3 rounded-lg px-2 py-2 hover:bg-selection"
            >
              <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-surface">
                <Icon aria-hidden="true" className="size-5 text-accent" />
              </span>
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="text-base font-medium">{label}</span>
                {previews[id] ? <span className="truncate text-sm text-fg2">{previews[id]}</span> : null}
              </span>
              <ChevronRight aria-hidden="true" className="size-5 shrink-0 text-fg2" />
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** One group's page. Wide: its heading is an h2 under "Settings", and it takes focus when you move between groups. */
function GroupPage({ group, wide, focusHeading }: { group: SettingsGroup; wide: boolean; focusHeading: boolean }) {
  const Page = PAGES[group.id];
  return (
    <div className="mx-auto w-full max-w-[720px] px-4 pb-10">
      {wide ? (
        <h2 tabIndex={-1} data-route-focus={focusHeading ? "" : undefined} className="pt-5 text-xl font-bold outline-none">
          {group.label}
        </h2>
      ) : null}
      <SectionLevel.Provider value={wide ? 3 : 2}>
        <Page />
      </SectionLevel.Provider>
    </div>
  );
}

export function SettingsScreen() {
  const wide = useWide();
  const navigate = useNavigate();
  const location = useLocation();
  const match = useMatch("/settings/:group");
  const current = groupById(match?.params.group);
  // The group on screen: the URL's, or on a wide screen with a bare /settings the first one (shown in place, not by
  // a redirect, so the /settings history entry is never rewritten under a back button that expects the list there).
  const shown = current ?? (wide ? FIRST_GROUP : undefined);

  // The group shown before the last navigation (state adjusted during render, so it is already right in the render
  // the shell's focus effect reads). Arrival focus then goes to the new group's heading (wide), or back to the row
  // of the group just left (narrow list). Arriving from another screen, or from the list, leaves it to the h1.
  const [trail, setTrail] = useState({ path: location.pathname, shown: shown?.id, from: undefined as SettingsGroupId | undefined });
  if (trail.path !== location.pathname) setTrail({ path: location.pathname, shown: shown?.id, from: trail.shown });
  else if (trail.shown !== shown?.id) setTrail({ ...trail, shown: shown?.id });
  const cameFrom = trail.from;

  const fromList = (location.state as { fromSettingsList?: boolean } | null)?.fromSettingsList === true;
  const back = () => {
    if (fromList) navigate(-1);
    else navigate("/settings", { replace: true });
  };

  const drilled = !wide && current;
  const page = (group: SettingsGroup) => <GroupPage group={group} wide={wide} focusHeading={wide && cameFrom !== undefined && cameFrom !== group.id} />;

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line">
        {drilled ? (
          <div className="mx-auto flex w-full max-w-[720px] items-center gap-1 px-2 py-1">
            <Button variant="ghost" size="icon" onClick={back} aria-label="Back to Settings" title="Back to Settings">
              <ChevronLeft aria-hidden="true" />
            </Button>
            <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
              {drilled.label}
            </h1>
          </div>
        ) : (
          <h1 className={cn("w-full px-4 pt-2 pb-2 text-xl font-bold", !wide && "mx-auto max-w-[720px]")} tabIndex={-1} data-route-heading>
            Settings
          </h1>
        )}
      </header>
      <div className="flex min-h-0 flex-1">
        {shown && wide ? <Rail shown={shown.id} /> : null}
        {/* Keyed by group so each page starts at the top and with fresh state. */}
        <div key={shown?.id ?? "list"} className="min-h-0 flex-1 overflow-y-auto">
          <Routes>
            <Route index element={wide ? page(FIRST_GROUP) : <GroupList returnTo={cameFrom} />} />
            <Route path=":group" element={current ? page(current) : <Navigate to="/settings" replace />} />
            <Route path="*" element={<Navigate to="/settings" replace />} />
          </Routes>
        </div>
      </div>
    </div>
  );
}