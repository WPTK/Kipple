# Kipple web

React 19, TypeScript (strict), Vite 8, Tailwind v4, TanStack Query and Virtual, React Router. The build
(`web/dist`) is embedded into the Go binary by `web/embed.go`.

## Stack notes

- **Components:** shadcn-style, hand-written on the scheme tokens, using **Radix** primitives (the `radix-ui`
  package: Collapsible, Dialog, DropdownMenu, Popover). No shadcn CLI, no generated files.
- **Router:** React Router (`react-router`), plain `BrowserRouter`. Article routes are `/i/:id?from=<scope>`;
  opening one is a push, prev/next is a replace, so back is always one step to the list.
- **Data:** TanStack Query (lists are snapshots: `staleTime: Infinity`, patched in place by mutations and SSE),
  TanStack Virtual for the list.
- **TypeScript is 6.0.x**, not 7: `typescript-eslint` supports up to 6.0.
- **Fonts** are self-hosted through `@fontsource` (all OFL): Literata, Vollkorn, Source Serif 4, Inter, Manrope,
  Source Sans 3 and JetBrains Mono (variable), Gentium Book Plus, Arvo and Source Code Pro (static), plus Atkinson
  Hyperlegible Next labelled "Easy to read". `src/lib/fonts.ts` is the list. **Charter is not vendored**: Butterick's
  release has to be downloaded from his site and its licence text could not be confirmed from here, so Charter is
  offered only where the device has it (macOS and iOS ship it), like New York, SF Pro, SF Mono, Georgia and Menlo.
- **`internal/web` serves `/assets/*` (immutable), `/_status` and `/_status.js`, each top-level file of the build
  at `/<name>` (what Vite copies from `public/`: the manifest and icons, plus the built `sw.js` from `sw/`;
  revalidated on every use), and `index.html` for every other path.** Hashed generated files (theme boot script,
  fonts) go under `assets/`.

## Layout

```
src/api/      fetch client (X-Kipple-Client, 401), types, TanStack Query hooks, SSE + fallback polling
src/layouts/  the five list layouts behind the ListLayout interface (Editorial, Cards, Compact, Inbox, Email - Compact)
src/gestures/ row swipe, long press, swipe back, pull to refresh (pointer events for row swipe and swipe back, touch events for pull to refresh; no gesture library)
src/screens/  list pane, article pane, feeds (add, edit, folders, OPML), feed health, search, stats and Wrapped, settings, login
src/ui/       button, segmented control, kit.tsx (modal, field, switch, stepper, disclosure, notices), FavStar, ResizeHandle (window splitter), UnreadCount
src/shell/    app shell (tab bar / sidebar, landmarks, live region, toasts)
src/theme/    schemes.json, CSS + boot script generators, picker (see src/theme/README.md)
src/lib/      keyboard, per-device prefs (layout, order, density, font, text size), undo, formatting, safe HTML
src/test/     Vitest setup and API mocks
sw/           service worker source (built to /sw.js)
public/       manifest and icons, copied to the build root
```

## Lists: layouts, gestures, keys, device prefs, undo

**Layouts.** Five, chosen from the layout menu in the list header: **Editorial** (id `magazine`; default; wide screens get a
list plus reader pane), **Cards** (a grid of 1 to 3 columns from the list's own width: under 600 px 1, under 900 px 2,
else 3; on a wide screen an open article shows in the reader pane with the list beside it as one column), **Compact**
(one dense line per article), **Inbox** (mail-style rows with an optional trailing thumbnail) and **Email - Compact**
(id `headlines`; titles only). The choice is per device, with a per-feed and
per-folder override (feed beats folder beats device default). `c` toggles Compact for the session only.

**Device prefs** (`src/lib/devicePrefs.ts`, `src/lib/prefs.ts`, `src/theme/`): layout, overrides, order, Inbox
thumbnails, the swipe peek, widths, density, font, text size, theme, motion, spacing, listen and the rest are
the server's **device profile** (docs/design.md 7.1c). `localStorage` (`kipple.device.v1`, `kipple.prefs.v1`,
`kipple.theme.v1`) is only the instant-paint cache, so the theme boot script still avoids a flash. See
"Device profile sync" below.

**Gestures (touch).** Swipes are touch-only and are off on a multi-column Cards grid. Swipe a row right to toggle read
or unread (in Muted a right swipe means Restore); a partial left swipe rests open on Star and More, and a full left swipe
stars. Long-press (500 ms) or the More button opens the row menu (read, star, mark above, mark below, open original,
copy link, share, "Mute similar..."; muted rows show Star, Restore and Edit rule). Swipe right on an article, starting
away from the left edge (which iOS keeps for its own back gesture), to go back to the list you came from; that works in
the phone layout only and is blocked on controls, media, horizontal scrollers, pinch zoom and text selection. Pull down
at the top of a list to refresh (not in search). The first-run swipe peek shows only on touch in a single-column list.
All of these have a visible button or a key, none is the only way to do something, and `prefers-reduced-motion` or the
in-app Reduce motion setting turns the animation off (rows leave instantly).

**Keys** (single keys can be switched off in Settings; `?` and `Esc` always work; `?` opens the searchable overlay): `j`/`k` next
and previous, `Enter` open, `o` original, `v` background tab (lists only), `m` toggle read, `s` star, `x` select, `z` undo (last 60 s, 2 min for bulk marks),
`f` full text, `r` refresh, `[`/`]` previous or next feed (or folder), `{`/`}` mark above or below, `A` mark
all, `c` Compact, `u` or `Esc` back to the list, `Shift+G` bottom, `g g` or `Home` top, `/` search, `g` then `i`, `a`,
`s`, `f` or `,` to jump to Unread, All, Starred, Feeds or Settings. Ctrl, Cmd and Alt are never bound.

**Undo.** Every read, unread and bulk mark pushes an undo entry; a star pushes one only when made by a full swipe (the star button, `s` and the menus toggle it directly). The toast (one slot, merged text such as
"4 articles marked read", 15 s, paused while hovered or focused) has a real Undo button; every toast uses one themed
surface (`--kp-toast-bg`: 18% accent into the scheme's surface, accent border, real shadow; `npm run contrast`
checks it in every theme). Info and help toasts stay 8 s, a toast with an action 15 s, an error until dismissed, and hovering or focusing any
toast holds it; `z` and the header menu do
the same. Rows leaving the Unread list collapse over
180 ms and the first still-visible row stays put on screen (`src/lib/collapse.ts`).

**Live updates.** The list is a snapshot (SSE never refetches it). New arrivals show an "n new articles" pill,
counted only for feeds in the current list (a feed, a folder's feeds, or everything; never on Starred, Muted, search or
an oldest-first list); the pill is always mounted and moves on transform and opacity only, so it never reflows the list.
The app owns event-stream reconnection. It closes the `EventSource` on every error, so the browser's own retry (every
`retry: 3000` ms with no backoff, forever) never runs, and schedules exactly one attempt per backoff step (1 s doubling
to 30 s with jitter). The backoff restarts only after a delivered message or a stream that stayed open 10 s. While the
stream is down (a proxy 502 during a deploy) it polls `/api/status`, resyncs when it is back, and a heartbeat watchdog
detects a hung stream. Tests: `api/events.hook.test.tsx`.

**Bulk read API.** `POST /api/items/mark-read` with `{scope:{view, feed_id|folder_id|all:true, q?, fallback?}, bound?:{order: "date"|"oldest",
side, anchor:{sort_at,id}, inclusive:false}, max_id, read:true, reason}`, answered by `{changed, restored, count,
undoable, ledger_ids?}` (docs/design.md 7.1). Undoing sends `{ids, ledger_ids?, read:false, reason:"bulk"}`. Toasts and announcements go through the shell's persistent live regions.

## Settings, feed management, health, account (step F3)

**Settings** (`SettingsScreen`) draws the server's `GET /api/settings` metadata with one generic renderer
(`SettingField`): a switch for `bool`, segmented buttons (up to four options) or a select for `enum`, a stepper for
`int` (no sliders), a text box for `text`; `json` is never shown. Changes are a `PATCH /api/settings` with an
optimistic update that rolls back on error; a 400's `keys` and `issues` show under the control; "Reset to default"
sends `null`; changes that `lib/settingGuards.ts` flags (a lower image cache, a lower retention) ask for confirmation first, and an enum can offer a preset row with "Custom". The screen is master-detail, six groups each at `/settings/<group>`: Appearance & Reading
(`appearance`: Appearance, Accessibility, Lists and reading, Keyboard, and the server's Reading group), Sync & Feeds
(`sync`: the Sync, Library and Images groups), Statistics (`statistics`: the Statistics group and Your statistics data),
Filters & Saved Searches (`filters`), Account & Devices (`account`: Devices, and Account with the Account group) and
Advanced (`advanced`: the Advanced group). At 900 px and up a rail of the groups sits beside the chosen group and a bare
`/settings` opens the first; below that `/settings` is the list of groups (with the current theme and font, check
interval and statistics state under theirs) and a group opens as its own page with a back button. Each group page has
its own loading and error state for the server settings. Keys the screen draws itself
(`ui.mark_read_on_scroll` sits in Accessibility; the one font choice, `ui.font_body`, is drawn as "Reading font") are left out of the generic list. `fetch.fulltext_all` (fetch the full article for every
feed, with its note about bandwidth and refresh time) appears under Library like any other bool; while it is on, the
feed editor shows that feed's own switch as "On for all feeds". The screen is capped at 720 px wide.

**Reading appearance** (the "Aa" button in every list header, Search included, and the article toolbar, and Settings > Appearance):
theme (including "Match my device" (Follow system in Settings) and "On a schedule", both with any day and night
pair; the schedule switches at "Night starts" (default 21:00) and "Day starts" (default 07:00) on the device's
clock, whatever the OS says), font, text size, one Density choice with an
"Adjust separately" disclosure, and "Highlight keywords" (the Aa menu; Settings > Appearance has theme, the Reading
font with a sample paragraph, text size and Density; the setup wizard's step 4, "Look and feel", has the theme pair and
the Reading font). The font is THE font: it applies at once to lists, the reader and the sidebar
(`--kp-app-font`, and `--kp-reading-font` for articles); Settings, Manage feeds, Health and every menu, popover and
dialog keep the system UI font (`.ui-font` and the role selectors in `index.css`). "Default" leaves lists and chrome in
the system font and articles in the reading serif. Segmented controls are pressed-style buttons over hidden native
radios (arrow keys work); choosing one holds the control where it was on screen even when the page above reflows
(text size scales every rem), which is what used to fling Settings around. Everything is stored per device
(`prefs.ts`, `devicePrefs.ts`, `theme/`) and syncs to the server's device profile (see Device profile sync); the
server's `ui.*` metadata also supplies the labels.

**Device settings** (Settings > Lists and reading, all per device): Layout (default; Editorial, Cards, Compact, Inbox,
Email - Compact; the stored ids are still `magazine` and `headlines`), Article width (Narrow, Medium, Wide, Full;
`--kp-col`), Open links in (New tab or Same tab; the default is Same tab on iPhone and iPad, where a link an installed
app claims otherwise leaves an about:blank tab, New tab elsewhere; `lib/links.ts`), Unread badge (Count capped at
99+, Dot only, Off; tab bar and sidebar), Thumbnails in Inbox (Auto or Off) and "Show swipe tips again" (resets the first-run peek). The sidebar and the list column of the reader pane are resizable (drag the
edge, arrow keys, double-click to reset) and remember their widths. Single-key shortcuts (under Settings > Keyboard) default to off on a touch-first
device (coarse pointer and no fine pointer; a hardware key press switches the default on) and stay a choice you can
change. Text spacing (WCAG 1.4.12: Less, Default, More) is in Accessibility, not in the Aa menu.

**Layouts.** Editorial is image-forward (a large lead image, a big title and excerpt, more whitespace; in a wide list
the image sits beside the text). Inbox is text-first (sender, subject, snippet, time, small optional thumbnail).
The layout menu is one list: the radio is the choice (a feed or folder's own override on those lists, the device default
elsewhere) and the star beside each layout makes it the device default. On a wide screen an open article always keeps
its list beside it, so switching layouts (Cards included, as a single column) never drops either; a grid list with
nothing open fills the width. The list header's controls are capped at 25rem and left-aligned in every layout, and
on a wide screen Settings has a gear beside them.

**Read state in Unread.** A row marked read on purpose (menu, key, the article toolbar; not by opening it, and not by a
swipe, which already removes its row) leaves the Unread list after 1.5 s with the undo toast still up; the article
that is open leaves when you move off it. Undo, or marking it unread again, cancels that. Marking an article unread
marks the cached Unread lists stale so it is there the next time one is shown. Menu items and toolbar buttons are named
by their action ("Mark as read", "Mark as unread") and the article header shows the state.

**Feeds** (`FeedsScreen`, `screens/feeds/`): add (address, optional title and folder; the exists, choose and ok
flows, then the first-fetch result), edit (title, address, folder, layout on this device, interval, retention, full
text, enabled; Advanced: duplicate detection, user agent, login, ignore HTTP cache, no HTTP/2; "Unsafe options" for
insecure TLS and private network, with warnings), delete with the starred count and "delete starred too", refresh
now, folders (create, rename, delete, layout override), OPML import
(with `mark_read_older_than_days` and a result summary) and export (a plain download link). **Changing the feed URL**
is the address field: `PATCH /api/feeds/{id}` with `url`.

**Reordering, favorites and bulk actions.** Grab a feed or folder anywhere on its row and drag (mouse: past 6 px; touch:
press and hold, or touch the grip at once; `lib/dnd.ts`); the grip also moves with the arrow keys, and "Show move
buttons" in the menu adds Move up and Move down buttons. A drop is one `POST /api/reorder` (a feed dropped in another
folder moves there), painted at once and confirmed with "Saved". The star on a folder or feed pins it to Favorites at the
top of the sidebar and Manage feeds (drag those to order them). Favorites live in the server setting
`library.favorites` (at most 500 `{t, id}` items); a save the server refuses is taken back and shown as an error. "Select" adds checkboxes (shift-click ranges, a checkbox per folder) and a bar with Move to folder (one
reorder call) and Delete (a confirm with the count, the total starred articles, "delete starred too", progress, and a
per-feed error list). The sidebar's folders collapse (remembered per device). Feed health is reached from the Feeds
menu (it is no longer in the sidebar).

**Feed health** (`/health`): a sortable, filterable table on wide screens and cards on phones, plain-English
statuses (`lib/feedStatus.ts`), the one-tap "Update to new URL" for a pending permanent redirect, the 14-day fetch
log with "Mark this fetch read", refresh now, turn on or off, reset the trimmed-unread count, and the bootstrap
`warnings` (also shown once per session as a banner above every screen).

**Account and backup**: the signed-in user, the version and (with Cloudflare Access validation on) the verified
Access email; change or set the web password, or remove it (offered only through a verified Access sign-in, and
asking for the current password; docs/design.md 7.0); generate an API password (shown once, with Copy and the Reeder or
NetNewsWire server URL), export a backup (build, confirm the returned `warning` and `contents`, then a real download
link so the browser's save dialog picks the place; 409, 507 and 413 have their own messages), apply retention now,
sign out.

## Device profile sync, Devices, Filters, Muted, Highlights (step F4)

**Device profile sync** (`lib/deviceSync.ts`). The bootstrap carries `device:{id,name,profile,merged}`. On load the
effective values (`merged`) replace the local cache (the server wins); unsent changes from a reload or a failed save
are put back on top. The first run on a browser with an empty profile and old `kipple.*` values sends them up once
(`kipple.deviceSync.v1` remembers it). Every write goes through one `PATCH /api/device`, debounced 500 ms: the patch is
recomputed from the local state against what the server last confirmed, so a burst is batched and the latest value of
each key wins, and values equal to the confirmed ones are never sent. A failure keeps the local value and shows
"Couldn't save your settings" with Retry (`shell/SaveStatus.tsx`; also retried when the network returns and on the next
change). A 400 names the refused keys: they keep their local value, are not sent again until they change, and the rest
is resent. Mapping: `ui.theme` is `system` for the day/night pair (`ui.theme_day` and `ui.theme_night`; with `ui.theme_schedule`
true it switches at `ui.theme_night_start` and `ui.theme_day_start` instead of following the OS) or a scheme id;
`ui.font_body` is the font's id (`fonts.ts`; the server also reads a display name such as "Inter" as its id); `ui.list_density` and
`ui.reading_density` take the step names (the server reads the first-draft `compact` and `comfortable` as Snug and Standard); everything else is `client.*`. Theme ids are
the server's, names and colors come from `schemes.json` (`theme/serverThemes.ts`). "Highlight keywords" syncs as `client.highlight_keywords`; only the layout to return to from "Titles only" has no profile key and stays on the device.

**Settings > Devices** (`screens/DevicesSection.tsx`): this device's name (`PUT /api/device/name`), every device with
when it was last seen and how many settings it has of its own, Copy its settings here, Forget, "Use this device's
settings as the default for new devices" (`make-default`) and Reset this device to defaults (`copy-from/defaults`),
each behind a confirm.

**Settings > Filters** (`screens/filters/`, `api/filters.ts`): every rule with scope, action, hits, last hit, muted
count and an on/off switch (PATCH). The editor (one instance, mounted in the shell, opened from Settings, a row menu or a
muted article) has name, scope (everywhere, a folder, a feed), words or a regular expression, terms as chips (limits from
`internal/filter`), the parts to look in, options with the inverted labels (Ignore capitalization is `case_sensitive`
false, Ignore accents is `fold_diacritics`, Match whole words only), "Act when it does NOT match", the four actions with
plain descriptions, and a live preview (`POST /api/filters/preview`, 600 ms after the last change, count, warnings, up to
20 sample cards, "Include already-read articles"). Save can also apply the rule to stored articles; the run shows as a
progress bar under the top of the screen from `run.*` events. Deleting offers restore as read (the default), restore as
unread (explicit) or leave muted (`DELETE ?unmute=`). `400 bad_filter` shows next to the field it names. Rule order is
cosmetic on the server (precedence is set-based), so there is no drag ordering.

**Muted** (sidebar, Feeds screen, a pill in list headers): `view=muted`, "Muted by <rule>" on each row with Restore
(mark unread, which un-mutes; no undo, because a mark-read cannot put it back under its rule) and Edit rule, the same in
the row menu and on an opened article. `Mute similar...` in the row menu and the article toolbar starts a rule from the
article: its feed, the title's keywords and the author as one-tap suggestions. Events: `items.state` muted, `counts.muted`,
`filters.changed`.

**Highlights** (`lib/highlight.ts`, `lib/useHighlights.tsx`): bootstrap `highlights` are matched with the engine's rules
(whole words, case, accents, phrases, unspaced scripts) in list titles and excerpts, the article title, author and body.
The body is marked by splitting sanitized text nodes into `<mark class="kp-hl">`, never from HTML built from a term;
links, code, pre, buttons, embeds and existing marks are skipped, and at most 300 marks are made. A phrase split by inline
markup is not marked. "Highlight keywords" in the Aa menu turns it off. Colors: `--kp-hl-bg` (20% star into the page
background) with the theme's text and a star underline, checked in every theme by `npm run contrast`.

## Search, saved searches, auto-read, image cache (step F5)

**Search** (`SearchScreen`, `lib/searchTerms.ts`, `lib/searchPrefs.ts`). `q` goes to the server untrimmed. `typing=1` is sent only while the
user is typing (250 ms debounce); Enter, a saved-search run and a URL load are submitted searches without it (`Scope.typing`, part of the scope key,
never sent otherwise). `fallback:true` shows the quiet banner "No exact matches: showing partial matches" and is echoed as `scope.fallback` in
"Mark all results as read" (`Scope.fallback` is not part of the key; off while typing). `422 search_too_broad` shows the server message inline (no
retry); a `400 bad_cursor` on a later page restarts the search (at most twice; other 400s show their message). The ordering (Relevance, Newest, Oldest) is per device in the device profile (`client.search_order`; the old `kipple.searchOrder.v1` value migrates once). Relevance shows one "Best matches first" header instead of day headers and disables
mark above/below. Query words are drawn with the highlight module: a client copy of the server parser (phrases, `-x`, `NOT x`, `title:`, `author:`,
`x*`) with a rough stem (`approxStem`), marks from the start of a word to its end; in fallback mode the words the fallback used.

**Saved searches** (`api/savedSearches.ts`, `SavedSearchesNav`, `SavedSearchesSection`). Sidebar and Feeds screen list them (collapsible, remembered
on the device; hidden when none) from `GET /api/saved-searches?counts=0`, with the counts from a second request; `999+` when capped, a dash when
`unread` is null. A run navigates to `/search?q=&feed|folder|view=&order=&ss=<id>`. "Save this search" creates (or updates the run one). Settings >
Saved searches: edit, delete (confirm), reorder (drag, grip arrows, buttons). `saved_searches.changed`, `folder.changed` and `feed.changed` refetch; `counts` events
refresh the counts at most every 10 s.

**Auto-read** (`api/autoRead.ts`, `AutoReadCatchUp`). The global control is drawn from the settings metadata with presets (Off, 30, 60, 90, 180, 365,
Custom); the feed editor has "Mark as read after" (global / Off / N days). Changing either marks nothing. Preview (optionally a what-if `days`) lists
the total and feeds; "Mark N older articles as read now" asks first above 100 (`confirm:true`), handles 409 `confirm_required` and `busy`, and says
there is no Undo. Progress comes from the `auto_read` run in `liveStore`.

**Images**. `imgproxy.mode` and `imgproxy.cache_mb` (presets 256 MB, 512 MB, 1 GB, 2 GB, Off, Custom) come from the metadata; `ImageCachePanel`
shows `GET /api/imgcache` (bar, files, hit rate since restart, low-disk warning; refetched each time Settings opens and after a change) and Clear
(confirm, `POST /api/imgcache/clear`). Health shows `imgcache_bytes`.

**Devices**. `GET /api/device` with `id: ""` (the unsaved default device) sets sync status `unsaved`: local prefs are kept, nothing is PATCHed, and
Settings > Devices explains it and hides the actions that would 404.

## Accessibility

`ACCESSIBILITY.md` is the checklist. Settings > Accessibility holds Text spacing (the WCAG 1.4.12 control: "Adds
extra space between letters, words and lines"), a pointer to the Easy to read font (the Reading font under Appearance), Reduce motion (follow system, on, off), Mark as read while scrolling (off by default), Listen to articles (voice and
speed), plus Larger buttons and Titles only in lists. The OS settings for contrast, forced colors, text size and
motion are followed without any setting.

## Scripts

| Command | What |
|---|---|
| `npm run dev` | Vite on 127.0.0.1:5173, proxying `/api`, `/img`, `/healthz` to 127.0.0.1:7080 (`KIPPLE_DEV_BACKEND` points the proxy at another local server) |
| `npm run seed` | Build and run a throwaway local Kipple with a few real feeds imported (below) |
| `npm run preview` | `vite preview` of the production build |
| `npm run typecheck` | `tsc --noEmit` |
| `npm run test:watch` | Vitest in watch mode |
| `npm run lint` | ESLint (typescript-eslint, react-hooks) |
| `npm test` | Vitest + Testing Library + axe |
| `npm run test:coverage` | Vitest with v8 coverage (what CI runs; visibility only, not a gate) |
| `npm run build` | `tsc --noEmit` then `vite build` |
| `npm run contrast` | Recompute WCAG and color-blind checks for every theme |

## Local development

Use `127.0.0.1`, never `localhost` (it resolves to `::1` first on this machine and stalls).

**Fast path (two terminals):**

```
cd web
npm install --cache <some-dir>   # if the shared npm cache throws EPERM, point --cache at a private directory
npm run seed                     # terminal 1: Kipple on 127.0.0.1:7080, feeds fetching in the background
npm run dev                      # terminal 2: http://127.0.0.1:5173
```

Sign in as `dev` with `dev-password-only-for-local-testing`. Those credentials belong to the throwaway data
directory only (`%TEMP%\kipple-dev`, override with `KIPPLE_DEV_DATA`; `KIPPLE_DEV_PORT` changes the port; `KIPPLE_DEV_HOST` binds another local address, such as this machine's Tailscale address, to look at a build on a phone (never a public address: the dev account's password is fixed and public); `npm run seed -- --keep` reuses it).
Without `--keep` the directory is deleted and recreated, but only if the script made it (it leaves a
`.kipple-dev-seed` file) or it is empty; any other `KIPPLE_DEV_DATA` directory is refused unless you add `-- --force`,
and the temp directory, home directory or a drive root are always refused. Needs Go on PATH and network access for the feeds.

**By hand**, without the script:

```
set KIPPLE_ADDR=127.0.0.1:7080
set KIPPLE_DATA=%TEMP%\kipple-dev
set KIPPLE_USERNAME=dev
set KIPPLE_PASSWORD=<any local test password>
go run ./cmd/kipple serve
go run ./cmd/kipple import feeds.opml    # any OPML; works while serve runs
```

Vite's proxy keeps the browser origin at 127.0.0.1:5173, so the same-origin checks (`Sec-Fetch-Site`,
`Origin`, `X-Kipple-Client`) pass. The cookie is not `Secure` over http.

To try the production bundle through Go: `npm run build`, then `go run ./cmd/kipple serve` and open `/`.
Without a build, `/` serves the status page, which is always at `/_status`.

## Tests

`npm test` runs every `src/**/*.test.ts(x)` suite (Vitest, jsdom, Testing Library, axe; 75 files at the time of writing), covering the reader and gesture
fixes, theme resolution (follow-system pair, custom pair, the schedule, boot-script parity), stats and Wrapped,
passwordless sign-in, the API client (401, cursor paging, pwa
header), the SSE reducer and fallback polling, keyboard rules, undo, filters, devices, muted, highlights, device sync and
search, with axe on the login, list, article, settings, feeds and search screens. jsdom has no layout, so `src/test/setup.ts` stubs sizes for the
virtualizer. Colour contrast is checked by `npm run contrast` and `theme.test.ts` (axe cannot in jsdom).
