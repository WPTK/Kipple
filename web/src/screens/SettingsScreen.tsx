import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { createContext, useContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { buttonVariants } from "@/ui/button";
import { NotFoundBlock } from "@/ui/NotFoundBlock";
import { Link, Route, Routes, useLocation, useMatch } from "react-router";
import { BarChart3, ChevronRight, Filter, Info, Palette, Rss, SlidersHorizontal, UserRound, type LucideIcon } from "lucide-react";
import { useSettings, type SettingGroup, type SettingMeta } from "@/api/admin";
import { AUTO_READ_PRESETS, autoReadLabel } from "@/api/autoRead";
import { errorMessage } from "@/api/client";
import { cn } from "@/lib/cn";
import { everyLabel } from "@/lib/interval";
import {
  ARTICLE_WIDTHS,
  ARTICLE_WIDTH_LABELS,
  LAYOUT_HINTS,
  LAYOUT_IDS,
  LAYOUT_LABELS,
  PAPER_NAME_MAX,
  UNREAD_BADGES,
  cleanPaperName,
  updateDevicePrefs,
  useDevicePrefs,
  type ArticleWidth,
  type LinkTarget,
  type UnreadBadge,
} from "@/lib/devicePrefs";
import { DEFAULT_PAPER_NAME } from "@/layouts/gazette";
import { resolveLinkTarget } from "@/lib/links";
import { MOTIONS, MOTION_LABELS, RATES, prefsStore, updatePrefs, type Motion } from "@/lib/prefs";
import { listVoices, speechSupported } from "@/lib/speech";
import { useStore } from "@/lib/store";
import { useWide } from "@/lib/useMedia";
import { ThemePicker } from "@/theme/ThemePicker";
import { navItemClass } from "@/ui/navItem";
import { Segmented } from "@/ui/segmented";
import { BackButton } from "@/ui/BackButton";
import { Button } from "@/ui/button";
import { Disclosure, FieldError, Notice, RadioCard, Skeleton, Switch, inputCls } from "@/ui/kit";
import { DensityControl, FontSelect, SpacingControl, TextSizeControl } from "./AppearanceControls";
import { DevicesSection } from "./DevicesSection";
import { ApplyRetentionNow } from "./RetentionDialog";
import { SyncAppsSection } from "./SyncAppsSection";
import { FiltersSection } from "./filters/FiltersSection";
import { AboutSection } from "./AboutSection";
import { BackupSection, SignInSection, useAccount } from "./AccountSection";
import { ConnectionSection } from "./ConnectionSection";
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
  { id: "appearance", label: "Appearance", icon: Palette },
  { id: "sync", label: "Sync & Feeds", icon: Rss },
  { id: "statistics", label: "Stats", icon: BarChart3 },
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

/**
 * Where a setting is kept: "This device" (only this browser) or "All devices" (the account, on the server). One pair of
 * words, shown once per section, or on a single row where one section holds both.
 */
type Scope = "device" | "all";
const SCOPE_LABELS: Record<Scope, string> = { device: "This device", all: "All devices" };
function ScopeMark({ scope }: { scope: Scope }) {
  return <span className="inline-block self-start rounded-full border border-line px-2 py-0.5 text-xs text-fg2">{SCOPE_LABELS[scope]}</span>;
}

function Section({ title, scope, children }: { title: string; scope?: Scope; children: ReactNode }) {
  const id = useId();
  const Heading = useContext(SectionLevel) === 3 ? "h3" : "h2";
  return (
    <section aria-labelledby={id} className="border-b border-line py-5">
      <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1">
        <Heading id={id} className="text-lg font-bold">
          {title}
        </Heading>
        {scope ? <ScopeMark scope={scope} /> : null}
      </div>
      <div className="flex flex-col gap-5">{children}</div>
    </section>
  );
}

/** Every number setting is a row of presets, and "Custom" opens the stepper for a value that is not one of them. */
export const PRESETS: Record<string, readonly { value: number; label: string }[]> = {
  "refresh.interval_minutes": [15, 30, 60, 120, 360, 720, 1440].map((value) => ({ value, label: everyLabel(value) })),
  "retention.restore_days": [0, 7, 30, 90, 180].map((value) => ({ value, label: value === 0 ? "Off" : `${value} days` })),
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
          <span className="text-xs text-fg2">Leave empty to use the saved value. Type a number of days to see how many articles it would mark read. Nothing is saved, and articles are only marked using the saved setting.</span>
        </label>
      </Disclosure>
    </div>
  );
}

/**
 * The settings of one server group shown on the Settings screen, in the server's order. A device-scoped key is only the
 * account default for devices; this device's own value has its own control (the theme picker, spacing, mark as read), so
 * listing the key here would show every one of those twice.
 */
function settingsOf(settings: SettingMeta[], group: SettingGroup): SettingMeta[] {
  return settings.filter((s) => s.group === group && s.surface === "settings" && s.scope !== "device" && s.kind !== "json");
}

function SettingRows({ list, all, rowScope }: { list: SettingMeta[]; all: SettingMeta[]; rowScope?: Scope }) {
  const imgWatch = `${all.find((s) => s.key === "imgproxy.cache_mb")?.value}|${all.find((s) => s.key === "imgproxy.mode")?.value}`;
  const extra = (s: SettingMeta): ReactNode =>
    s.key === "library.auto_read_days" ? <AutoReadPanel days={Number(s.value) || 0} /> : s.key === "imgproxy.cache_mb" ? <ImageCachePanel watch={imgWatch} /> : s.key === "retention.default" ? <ApplyRetentionNow /> : null;
  return (
    <>
      {list.map((s) => (
        <div key={s.key} className="flex flex-col gap-3">
          {rowScope ? <ScopeMark scope={rowScope} /> : null}
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
function ServerPart({
  groups,
  empty,
  inline = false,
  scope,
  rowScope,
}: {
  groups: { id: SettingGroup; title?: string }[];
  empty?: string;
  inline?: boolean;
  /** Where every setting shown here is kept, said once per section. */
  scope?: Scope;
  /** Where each setting is kept, said on its own row (a section that also holds settings of the other kind). */
  rowScope?: Scope;
}) {
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
          <Section key={g.id} title={g.title} scope={scope}>
            <SettingRows list={g.list} all={all} rowScope={rowScope} />
          </Section>
        ) : inline ? (
          <SettingRows key={g.id} list={g.list} all={all} rowScope={rowScope} />
        ) : (
          <div key={g.id} className="flex flex-col gap-5 border-b border-line py-5">
            {scope ? <ScopeMark scope={scope} /> : null}
            <SettingRows list={g.list} all={all} rowScope={rowScope} />
          </div>
        ),
      )}
    </>
  );
}

/**
 * The Gazette's name on this device. Blank prints the default name. Only a name the profile key accepts is saved
 * (cleanPaperName, the server's rule); anything else stays in the field with the reason, and the saved name is kept.
 */
function PaperNameField() {
  const dp = useDevicePrefs();
  const id = useId();
  const [refused, setRefused] = useState<string | null>(null);
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-semibold">
        Name of the Gazette
      </label>
      <input
        id={id}
        type="text"
        value={refused ?? dp.paperName}
        maxLength={PAPER_NAME_MAX}
        placeholder={DEFAULT_PAPER_NAME}
        autoComplete="off"
        onChange={(e) => {
          const v = e.target.value;
          if (cleanPaperName(v) !== v) return setRefused(v);
          setRefused(null);
          updateDevicePrefs({ paperName: v });
        }}
        aria-invalid={refused !== null || undefined}
        aria-describedby={refused !== null ? `${id}-error ${id}-hint` : `${id}-hint`}
        className={`${inputCls} max-w-sm`}
      />
      {refused !== null ? (
        <FieldError id={`${id}-error`} className="text-xs">
          The name must fit on one line: no tabs, line breaks or other control characters. It was not saved.
        </FieldError>
      ) : null}
      <span id={`${id}-hint`} className="text-xs text-fg2">
        Printed at the top of the Gazette layout&apos;s front page and on every page after it. Leave it empty for {DEFAULT_PAPER_NAME}.
      </span>
    </div>
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
  const supported = speechSupported();
  return (
    <Section title="Accessibility" scope="device">
      <p className="text-sm text-fg2">Kipple follows your device's text, motion and contrast settings. These are extra controls.</p>
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
    </Section>
  );
}

/* ---------- The seven group pages ---------- */

function AppearancePage() {
  const dp = useDevicePrefs();
  const settings = useSettings();
  const scrollHelp = settings.data?.settings.find((s) => s.key === "ui.mark_read_on_scroll")?.description;
  return (
    <>
      <Section title="Reading appearance" scope="device">
        <p className="text-sm text-fg2">The Aa button above any list or article has the same theme, font and text size.</p>
        <ThemePicker />
        <FontSelect preview="large" />
        <TextSizeControl />
        <DensityControl />
      </Section>

      <Section title="Lists" scope="device">
        <fieldset className="min-w-0" aria-describedby="layout-hint">
          <legend className="text-sm font-semibold">Layout</legend>
          <p id="layout-hint" className="mb-2 text-xs text-fg2">Each feed and folder can override it from the Show and sort button in its list.</p>
          <div className="flex flex-col gap-2">
            {LAYOUT_IDS.map((id) => (
              <RadioCard key={id} name="default-layout" value={id} checked={dp.layout === id} onSelect={() => updateDevicePrefs({ layout: id })} title={LAYOUT_LABELS[id]} help={LAYOUT_HINTS[id]} />
            ))}
          </div>
        </fieldset>
        <Segmented<"show" | "hide">
          legend="Pictures in lists"
          hint="Thumbnails in the Inbox and images in the Digest. Editorial and Cards always show pictures."
          value={dp.listImages ? "show" : "hide"}
          onChange={(v) => updateDevicePrefs({ listImages: v === "show" })}
          options={[
            { value: "show", label: "Show" },
            { value: "hide", label: "Hide" },
          ]}
        />
        <Segmented<UnreadBadge>
          legend="Unread badge"
          hint="On the Articles tab and in the sidebar. Counts show up to 99+."
          value={dp.unreadBadge}
          onChange={(unreadBadge) => updateDevicePrefs({ unreadBadge })}
          options={UNREAD_BADGES.map((b) => ({ value: b, label: b === "count" ? "Count" : b === "dot" ? "Dot only" : "Off" }))}
        />
        <PaperNameField />
        <div>
          <Button onClick={() => updateDevicePrefs({ peekSeen: false })} className="self-start">
            Show swipe tips again
          </Button>
          <p className="mt-1 text-xs text-fg2">Swipe a row right to mark it read or unread, left to star it or see more. Every swipe has a button.</p>
        </div>
        <Link to="/feeds" className="inline-flex min-h-11 items-center self-start text-sm text-link underline underline-offset-2">
          Manage feeds and folders
        </Link>
      </Section>

      <Section title="Articles" scope="device">
        <Segmented<ArticleWidth>
          legend="Article width"
          hint="How wide the text of an article is. Full uses the whole pane."
          value={dp.articleWidth}
          onChange={(articleWidth) => updateDevicePrefs({ articleWidth })}
          options={ARTICLE_WIDTHS.map((w) => ({ value: w, label: ARTICLE_WIDTH_LABELS[w] }))}
        />
        <Segmented<LinkTarget>
          legend="Open links in"
          hint="Same tab lets a link that an installed app claims open in the app, and Back returns here."
          value={resolveLinkTarget(dp.linkTarget)}
          onChange={(linkTarget) => updateDevicePrefs({ linkTarget })}
          options={[
            { value: "new", label: "New tab" },
            { value: "same", label: "Same tab" },
          ]}
        />
        <ServerPart groups={[{ id: "reading" }]} inline rowScope="all" />
      </Section>

      <AccessibilitySection scrollHelp={scrollHelp} />
    </>
  );
}

function SyncPage() {
  return (
    <>
      <ServerPart
        scope="all"
        groups={[
          { id: "sync", title: "Checking feeds" },
          { id: "library", title: "Storage" },
          { id: "images", title: "Image cache" },
        ]}
        empty="No sync or feed settings to change yet."
      />
      <div className="py-3">
        <Link to="/health" className="inline-flex min-h-11 items-center text-sm text-link underline underline-offset-2">
          Feed health
        </Link>
      </div>
    </>
  );
}

function StatisticsPage() {
  return (
    <>
      <ServerPart scope="all" groups={[{ id: "stats" }, { id: "account" }]} />
      {/* A fixed slot after the server part, whether settings are loading, failed or loaded: it never remounts, so an open dialog survives. */}
      <Section title="Your stats data" scope="all">
        <StatsDataSection defaultRange={loadRange()} />
      </Section>
    </>
  );
}

function FiltersPage() {
  return (
    <>
      <Section title="Filters" scope="all">
        <FiltersSection />
      </Section>
      <Section title="Saved searches" scope="all">
        <SavedSearchesSection />
      </Section>
    </>
  );
}

function AccountPage() {
  const { hasPassword } = useAccount();
  return (
    <>
      <Section title="Devices">
        <DevicesSection />
      </Section>
      <Section title="Sign-in">
        <SignInSection />
      </Section>
      <Section title="Sync apps (Google Reader compatible)">
        <SyncAppsSection hasPassword={hasPassword} />
      </Section>
      <Section title="Backup and data" scope="all">
        <BackupSection />
      </Section>
    </>
  );
}

function AdvancedPage() {
  return (
    <>
      <p className="pt-5 text-sm text-fg2">Most people never need to change these.</p>
      <ServerPart scope="all" groups={[{ id: "advanced" }]} empty="No advanced settings to change yet." />
      <Section title="Address and access" scope="all">
        <ConnectionSection />
      </Section>
    </>
  );
}

function AboutPage() {
  return (
    <div className="flex flex-col gap-5 py-5">
      <AboutSection />
    </div>
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
  return (
    <nav aria-label="Settings sections" className="mx-auto w-full max-w-[720px] px-4 py-3">
      <ul className="flex flex-col">
        {SETTINGS_GROUPS.map(({ id, label, icon: Icon }) => (
          <li key={id} className="border-b border-line last:border-b-0">
            <Link
              to={`/settings/${id}`}
              data-route-focus={returnTo === id ? "" : undefined}
              className="-mx-2 flex min-h-14 items-center gap-3 rounded-lg px-2 py-2 hover:bg-selection"
            >
              <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-surface">
                <Icon aria-hidden="true" className="size-5 text-accent" />
              </span>
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="text-base font-medium">{label}</span>
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
  const location = useLocation();
  const match = useMatch("/settings/:group");
  const current = groupById(match?.params.group);
  // The group on screen: the URL's, or on a wide screen with a bare /settings the first one (shown in place, not by
  // a redirect, so the /settings history entry is never rewritten under a back button that expects the list there).
  const shown = current ?? (wide ? FIRST_GROUP : undefined);
  const unknownPage = (
    <NotFoundBlock
      noun="settings page"
      why="Kipple has no settings page at this address."
      action={
        <Link to="/settings" replace className={buttonVariants({ variant: "solid" })}>
          Go to Settings
        </Link>
      }
    />
  );

  // The group shown before the last navigation (state adjusted during render, so it is already right in the render
  // the shell's focus effect reads). Arrival focus then goes to the new group's heading (wide), or back to the row
  // of the group just left (narrow list). Arriving from another screen, or from the list, leaves it to the h1.
  const [trail, setTrail] = useState({ path: location.pathname, shown: shown?.id, from: undefined as SettingsGroupId | undefined });
  if (trail.path !== location.pathname) setTrail({ path: location.pathname, shown: shown?.id, from: trail.shown });
  else if (trail.shown !== shown?.id) setTrail({ ...trail, shown: shown?.id });
  const cameFrom = trail.from;

  const drilled = !wide && current;
  // The tab names the page on screen: the group when one is shown (wide always shows one), else the list.
  useDocumentTitle(shown && (wide || current) ? `${shown.label} - Settings` : "Settings");
  const page = (group: SettingsGroup) => <GroupPage group={group} wide={wide} focusHeading={wide && cameFrom !== undefined && cameFrom !== group.id} />;

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line">
        {drilled ? (
          <div className="mx-auto flex w-full max-w-[720px] items-center gap-1 px-2 py-1">
            <BackButton to="/settings" ariaLabel="Back to Settings" />
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
            <Route path=":group" element={current ? page(current) : unknownPage} />
            <Route path="*" element={unknownPage} />
          </Routes>
        </div>
      </div>
    </div>
  );
}