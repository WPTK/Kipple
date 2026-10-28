# UI decisions

The current UI rulings from the owner, newest rule wins where sections differ. This file is the authority; the dated
process log (parking lot, work order, working agreement) is in the history repo. Layout ids never change; labels are
what people see.

## Accessibility

- WCAG deviations are fine as defaults **provided an option exists to enable the accessible behavior**,
  and enabling any option must never break anything else.
- Implement everything the research file lists for **phase 2 and phase 3**, including read-aloud,
  `prefers-contrast`, reduced motion, text scaling, focus and semantics, live region for new items.
- More **spacing** options than the three presets (TODO, resolved: Dense, Snug, Standard, Relaxed and Airy steps, see
  Round 2).
- Font: add **Atkinson Hyperlegible Next** as a font option, labelled "Easy to read". OpenDyslexic is
  dropped (weak evidence). Font file size is not a concern.
- The proposed six-control Accessibility settings section is approved. Look for further opportunities in it.

## Colors

- the owner loves the eight schemes and their names: **Paper, Linen, Parchment, Fern→(renamed), Cocoa, Graphite,
  Midnight, Signal**. Alternate names are not wanted.
- **Fern is renamed.** Theme: the green pages of a phone book (municipal and government listings) or other
  paper. Candidates: Ledger, Directory, Gazette, Bond. (Resolved in round 2: Directory.) The green itself should be "a
  little softer" than the proposal, but only a little.
- **Cocoa** is too dark as a background: rework (lighter warm brown).
- Keep **both Paper and Linen**. Add a **gray** theme with a paper-themed name (candidate: Newsprint).
- Add more schemes: some accessibility-friendly (color-blind-safe accents, cream/low-glare, high-contrast
  dark), some purely pretty and legible. Paper-themed names preferred. (Resolved: 20 schemes shipped, see Round 2 and `web/src/theme/schemes.json`.)
- **Follow device** (once called Follow system) stays. Default pair when no custom pair is chosen: Paper (day) / Midnight (night)
  (the owner wrote "paper/onyx"; Onyx was Midnight's alternate name, so Midnight is assumed. Confirm). An
  option lets the owner pick any custom day theme and night theme for follow-system.
- Midnight text `#e0e0e0` is bright enough. **No image dimming** in dark themes.
- No Kipple identity/accent color now. Parking lot: a design system, a static demo site, and pull-and-run
  image for others (Kipple stays single-user).

## Gestures and navigation

- Must be iOS compliant in the least annoying way (nothing starting in the left ~24 px; no fighting Safari's
  back gesture; every gesture has a button).
- Haptics not needed; visual snap plus **undo toast** is approved.
- **Row swipe:** swipe right toggles read and unread; swipe left reveals Star and More; a full swipe left stars. Each commits with undo. (Star also has a row button and the row menu.)
- **Article swipe: swipe back to the previous screen** (the article list you came from, not always the feed
  list), i.e. drill-in navigation stack. Keep next/previous **buttons**.
- Keys: **no single-key shortcuts** (owner decision, final). Keyboard use is Tab order, the list's one Tab stop,
  arrow keys moving focus between rows (Home, End, PageUp and PageDown too), Enter and Space to activate, Escape to
  close and return focus. A control that turns off while it may have focus is aria-disabled, never disabled.
- First-run **peek animation** teaches swipe reveal.
- Mark-read-on-scroll: keep as a setting, **off by default**.

## Standard controls (#286)

- **Yes or no in a form or dialog is a checkbox** (`Checkbox` in `web/src/ui/kit.tsx`): the choice is applied by Save, Add,
  Export or Continue, as on iOS, Android and the web. A **switch** (`Switch`) is only for a setting that takes effect the
  moment it is flipped (Settings, the Filters list). The two never share a form. `web/src/ui/checkbox.test.tsx` scans every
  source file and allows `<Switch>` only in a short list of instant-apply places. A checkbox row is one label (box and text,
  44 px tall, 56 px with Large targets); `web/uat/controls.mjs` measures it.
- **Exempt: selecting rows in a list** (`setup/FeedsStep.tsx`, `screens/FeedsScreen.tsx`, `screens/HealthScreen.tsx`). A bare
  checkbox on a list row picks that row for a bulk action; it is not a labelled yes/no option, so it stays a native input in
  the row and is not a `Checkbox`.
- Wording follows the control: "Tick" and "Untick" for a checkbox, "Turn on" only for a switch. A checkbox label is an action
  phrase that reads as the thing you do when it is ticked ("Pause this feed", "Use this filter"), not a state ("Enabled",
  "Filter is on"); the owner ruled this 2026-10-19.
- A choice among a few named steps stays a segmented control (native radios); a choice that wants a sentence each is a
  list of radio cards (`RadioCard` in `web/src/ui/kit.tsx`; `RadioRow` for a short plain choice). `checkbox.test.tsx` also fails a hand-written
  `<input type="checkbox">` or `<input type="radio">` outside the exempt list rows, the segmented control and the theme swatches;
  `web/uat/controls.mjs` measures the card (K7).
- **A toggle's state never rests on color alone** (#563). A button with `aria-pressed` also changes its glyph or shows a mark: the
  star fills, Oldest first swaps its arrow, a suggestion chip gains a check, Full text swaps the plain page for a page with a check
  mark. Its name stays the same (the state is `aria-pressed`, not a renamed label). A tint may back the glyph up; it is never
  the only cue.

## Layout

- **Magazine is the default**; compact list and cards also ship. the owner likes the proposed layouts and wants
  other options explored, notably an **email-style ("Inbox") layout** (TODO research, resolved: Inbox shipped; the layouts are
  Editorial (id `magazine`, this decision's "Magazine"), Cards, Compact, Inbox, Headlines (id `headlines`),
  the Gazette and the Digest).
- Lead images: **proxied and cached** by Kipple (sites dislike hotlinking), with a **bounded cache** that
  cannot grow out of hand (size cap setting with LRU eviction on the data volume). This replaces the
  design's "no disk cache in phase 2".

## design.md audit answers

1. Appearance settings are **per device**.
2. Default sort **newest first** (oldest-first available).
3. Mark above/below: best practice, defined against the list's own order (TODO API, resolved: the `bound` scope of mark-read).
4. Keymap from the answers above plus common conventions.
5. Image privacy and embeds: open but safe (`Referrer-Policy`, CSP, sandboxed embeds, TODO, resolved: referrer meta tag and click-to-load embeds).
6. **Export backup** in the UI (saves a file wherever the owner wants); for testing, an off-box copy is fine.
7. Rollback cost accepted (restore the pre-migration snapshot).
8. **Keyword mute filters: yes**, and other filtering features in the same family (TODO brainstorm, resolved in round 2).
9. Pending defaults and search: follow industry best practice (FTS5 stemming with prefix fallback).
10. Offline/PWA scope: read and interact with what is already downloaded; show an "offline" message at
    launch; queue actions and sync when back online. (Shipped in phase 3, design §7.9: manifest,
    service worker, an offline notice, and a queue for star and read changes. Prefetch keeps only the first page of Unread.)


## Round 2 answers (2026-09-25, later)

Authoritative over the round-1 and round-2 research files where they differ. the owner: "Go ahead and begin
implementing anything. Commit and PR as needed without asking." (the Kipple server deploys still need his go-ahead.)

### Colors
- Green theme is **Directory** (Fern retired). Gray is **Newsprint**. Keep **both Cocoas** (Kraft and Mid).
- Keep **Teletype and Lamplight**. Fix **Signal's danger** color (deuteranopia). **Carbon and Fountain must be
  visually further apart** (Carbon: neutral cool gray-black; Fountain: clearly navy with cream text).
- Follow-system: default Paper (day) and Midnight (night). The custom day and night pickers are **not**
  restricted by kind: any scheme may be either.
- Theme picker: default short list, the rest collapsed under "More themes" (Hick's law); accessibility group
  collapsed. (This is the Settings picker; the Aa menu has a compact grouped select of every theme.)
- New themes added in round 2 stay: Foolscap, Tracing, Carbon, Lamplight, Inkwell, Teletype, Airmail,
  Stationery, Tissue, Fountain.

### Layouts
- Ship **all five**: Magazine (default), Cards, Compact, Inbox, Headlines (shipped; Magazine is labelled
  Editorial and Headlines, ids unchanged). Columns, Reader list and Expanded
  stream were dropped (see "Additional layouts (#39)" below).
- Layout is choosable **per feed and per folder** (override), plus a global/device default. UI in phase 2.
- A folder's layout override applies to its subfolders and their feeds too; the nearest override up the tree wins
  (feed, then its folder, then each folder above, then the device default).
- Spacing: the owner doubts sliders. Use **named steps in a segmented picker with a live preview**
  (Dense / Snug / Standard / Relaxed / Airy). One "Spacing" choice drives both list rows and reading text (Cards and Gazette keep their own list spacing, and the help text says so);
  an "Adjust separately" disclosure splits them. No sliders, no "link" toggle. Must not look messy.

### Folders (nested, #209)
- The sidebar shows folders as a **tree** (WAI-ARIA navigation tree: one tab stop, arrow keys move, Right/Left expand
  and collapse or step into and out of a folder, Enter or Space opens the list, with focus left on the tree and the
  new list announced). Each level indents 12 px, less past the fourth so deep trees fit a phone. A folder's subfolders come before its own feeds. A branch with no feed anywhere
  inside is hidden, as an empty folder always was. Collapsed folders are remembered per device.
- Every folder badge counts its **whole subtree**, the same number its list shows.
- A favorited folder shows its subtree in Favorites.
- Manage Feeds: in Edit, dropping a folder on the middle of another folder's row puts it inside; the top or bottom
  edge puts it before or after. **Move to…** in each folder's menu is the keyboard, touch and phone path (pick the
  new parent, or Top level). **New subfolder** in the same menu; **New folder** asks where (Top level by default).
- One folder picker everywhere (add feed, edit feed, bulk move, move folder, new folder, filters, saved searches): a
  native select listing folders in tree order, **each labelled by its full path** (`Tech › Apple`). The move picker
  leaves out the folder itself, everything inside it, the default folder and anywhere too deep.
- Paths on screen use ` › `, not `/` (a folder name may contain a slash). A subfolder's list shows the path of the
  folders above it over its title.
  The OPML import report shows the paths the server sends, which are `/`-joined like Reader API labels; a folder the
  file merged into one with the same full path is shown as both chains joined with ` › `.
- In the sidebar trees the item is the only tab stop: the chevron and the star are pointer targets there. From the
  keyboard, favorites are added and taken out on the Feeds screen.
- A subfolder's or feed's layout menu names where its layout comes from: "Same as Tech (Cards)", or "Same as Settings (Cards)" when no folder sets it: the one phrase every inherited value uses, in the editor's schedule fields too.
- Deleting a folder says how many subfolders are deleted with it and that its feeds move to the default folder.
- OPML import has an opt-in switch, off by default: "Move feeds that already exist into the file's folders".

### Gestures and keys
- **Escape closes the open article** and returns to the list at every screen width: focus goes to the article's row and
  the address returns to the list.
- **Swipe directions match iOS Mail everywhere** (this supersedes round 1's "read left, unread right"):
  swipe right (leading) = toggle read/unread; swipe left (trailing) reveals Star and More (the row menu), and a
  full swipe left stars. A full swipe commits with undo. Star also has a row button and the long-press menu.
- Undo toast lasts **15 seconds** from when the change is saved (merged toast). The Undo button undoes the newest change, up to 20
  changes back within 60 s (bulk marks 2 minutes), waiting for one that is still saving; the full model is
  docs/design.md §7.11.
  Marking read in the Unread view: row dims in place (shipped differently: a row marked read on purpose dims,
  then leaves the Unread list after 1.5 s with the undo toast up; the open article leaves when you
  move off it; a swiped row leaves at once; undo or mark unread cancels it). Mark all marks only items present when the list loaded. Mark
  above/below approved (anchor excluded, disabled for relevance-sorted search).

### Backend features
- Filters family **F1 to F7 in phase 2** (mute, mark read, auto-star, only-show-matching, highlights, saved
  searches, auto-read after N days, reading-time filter, per-feed view/order). (Shipped in phase 2: mute,
  mark read, star, highlight, saved searches and auto-read. The rest, only-show-matching, the reading-time
  filter UI and per-feed view and order, shipped with #38; see "Filters follow-ups" below.) Quiet hours rejected.
- Image cache default cap **1 GiB** (check the Kipple server free disk before deploy). Default mode: **all images**
  (inline too) through Kipple, matching common RSS-reader practice; enables the strict CSP.
- Backup export, `kipple restore`, `kipple password`; ship them (and take an off-box export) before the
  0004/0005 migrations reach the live DB. Implementation order: `backend-additions-round2.md` §11.

### Filters follow-ups (#38)
Choices made where the decisions above were silent; the owner may overrule any of them.
- **Only show matching** is a choice under "What it does", stored as an inverted Mute (no new action, no schema
  change). For Mute the "Act when it does NOT match" option is that choice, so the checkbox is offered for Mark as
  read and Star only.
- **Article length filter:** three lengths, "5 min or less", "6 to 15 min" and "Over 15 min", from the Article length button (a timer) in the
  list header, with a chip under the view pills that clears it. It lives in the list's address: the view pills,
  previous and next feed, and an opened article keep it; another list starts without it; it is not saved. Mark all as
  read marks only what it shows.
- **Per-feed view** means the view a feed or folder opens in (Unread or All), not the layout (already per feed). No
  device default view: Unread unless the feed or a folder above it says otherwise. Previous and next feed keep the
  current view.
- **Per-feed order** resolves like the layout: feed, then the nearest folder up the tree, then the device default. The
  list header's oldest-first toggle acts at the list's level (the feed or folder on its list, the device on Unread,
  All, Starred and Muted), and toggling back to the inherited order removes the override. The menu's Order radio is
  the explicit form: picking Newest first or Oldest first there keeps that override even when it equals what the list
  would inherit (so a later change above does not move it), and its first choice removes it.
- **Group by** (#39) is the headers a list is cut under: Day (one per local day, the default), Week (Today, Yesterday,
  This week, Earlier; the week starts on Monday) or None. It is a fourth field of the same override, resolved like the
  order, and a radio group in the Show and sort menu (and a select in the feed and folder editors) whose first choice names
  what the list inherits. Headers keep the list's order and are level 2 headings; the Gazette and a search ignore the
  choice. The menu keeps the Group by choices in every layout and search, greyed with the reason where the list sets its own (see "Pictures, order and the layout menu").
- Layout, order, grouping and view of one list are one object per feed or folder in the device profile
  (`client.list_overrides`), set from the list header's options menu (its button reads "Show and sort, Cards layout")
  and from the feed and folder editors, whose first choice names what the list inherits, as the menu does.
### Mute similar (#320, 2026-10-07)
- **Mute similar...** opens the new-filter editor on the article's feed with no words chosen: the rule contains only
  what the reader taps. The editor offers the title's distinctive words and the author as toggle buttons (a second tap
  takes a word out again); Save stays disabled until a word is in. Suggestions skip words under four letters, bare
  numbers and a list of English function words, very common verbs and adverbs, and headline filler such as best, apps
  and alternatives (`web/src/lib/stopwords.ts`). Words a reader may want to mute (free, deals, review, video, live) are
  not on the list. The list is English only: other languages rely on the length, digit and script rules (unspaced
  scripts are cut into short pieces).
- Saving a mute rule from **Mute similar...** also mutes the article it was started from, read or unread, when the rule
  as saved matches it (#374): the editor sends the article's id and the server mutes it in the same transaction, with
  the same write as Apply, so Restore, mark unread, star and delete-with-unmute undo it like any mute. Only that article
  changes; "Apply to existing articles" still covers the rest of the feed. A starred article is not muted (star beats
  mute). A muted article leaves every list except Muted at once, Unread included, as if it had arrived muted; while
  it stays open, Next and Previous go on from where it was in the list. Undo never un-mutes: undoing an
  earlier mark read or unstar leaves an article muted since as it is (the server decides, design §7.11). The save
  toast says what happened in one sentence, and when nothing was muted it says why: the rule does not mute, it is
  turned off, the article no longer exists (the rule is still saved), it is starred, its feed is archived, the rule no
  longer matches it, another filter already muted it (it stays with that filter), or a star filter matches it.
- **Restore** in Muted puts back the read state an article had before it was muted (#374 review): an article read
  before the mute stays read, an unread one is unread again, and the announcement says which. One rule for every muted
  article, the same one delete-with-unmute=unread uses for a whole filter.

## Stats round (phase 4 pre-meeting, 2026-09-26)

Decisions are recorded in full in the private history repository. Summary of what shapes the build:

- **Focus:** sources and pruning (most read, time per source, never opened) with habits at the top (summary strip,
  daily activity, streaks, weekday-by-hour heatmap, behavior facts). Your year is a simple yearly summary with an opt-in
  share sheet, aggregates only by default. No goals, targets, badges, comparisons with other people or directives.
- **Reading:** every open is recorded; views count an item as read at 10 s active time, or 25% scroll with at least 3 s active time (a scroll alone stopped counting with issue #120). List-preview
  opens count. Reading stats (open, read time, scroll, open original, share) are web-only; Reader API clients are not tracked for reading, though their stars are recorded.
- **Screen:** one Stats nav entry, phone first. Range Week/Month/Year/Monthly/All (default Month); tapping a summary tile shows its previous-period value (none for Monthly and All, and none when the earlier period starts before recording did). Items/Minutes toggle with
  folder rollup; average read length, quick-bounce rate, open-original rate, read rate per feed, most-starred feeds;
  never opened. A feed's row opens its drill-down sheet.
- **Settings:** first day of week (Sunday or Monday, default Sunday); stats on/off (off stops recording and hides the
  screen, keeps data); Your year on/off; delete a range; delete all (typed confirmation).
- **Export:** raw events as CSV, JSON or JSON Lines; the summary is JSON only (a summary is several tables, so a CSV or JSON Lines summary was not built). Range, a toggle to leave out article titles and links (feed and folder names, times and the time zone stay), and a data dictionary. Export and delete stay available with statistics off. They live in Settings > Stats only; the Stats screen links there.
- **Sender:** 15 s flush, visibilitychange primary and pagehide backup, 2 minute idle cutoff, a random id on every event
  (unique index) for dedup, offline events queued, losses accepted.
- **Delivery:** alpha.4 sender and settings, alpha.5 screen, alpha.6 export and data controls, alpha.7 Your year.

### Stats follow-up (2026-10-06, issues #37 and #36)

Decided in a design discussion. #37 keeps the roadmap label; #36 is closed (below).

- **Reading pace: dropped.** Words per minute would need a word count per item that trimming removes. Average read
  length per source already covers "how long articles take".
- **Period comparison: both levels.** Items, minutes and active days against the previous equal period, as deltas on the
  summary strip, plus per-feed deltas in the drill-down to show feeds started or abandoned.
- **Read rate per feed: built for pruning.** The share of a feed's new items that were opened. Retention trims the items
  table, so the published count per feed per day has to be recorded at fetch time, in a table that survives trims
  (`feed_daily_new`, migration 0017). The rate covers only complete days (today is left out) with complete reads and counted
  arrivals, and counts only reads of the items that arrived in them.
- **Screen: design A.** One Stats screen. Adds comparison, a Monthly range with bars covering every month on record
  (not capped at 12), and a source row that opens a per-feed drill-down sheet. Design B (a separate Feeds page) is the
  fallback if the sheet outgrows the screen; promote the sheet then.
- **Comparison has no toggle.** Tiles show plain numbers by default. Tapping a tile shows that stat's previous-period
  value and change (for example "+18% from 120") and says which period it was compared with; tapping again hides it.
  A settings-style on/off control inside content was rejected as non-standard (issue #286 holds the wider UI audit).
- **Read rate shows as a percentage.** The counts ("31 of 50 new") appear on tap. A feed with no recorded days shows a
  dash, never 0%. Per-feed deltas appear only in the drill-down sheet, not in the Sources list. Folder rows show no
  rate: Sources lists only feeds with activity, so a folder's quiet feeds would be missing from its new items.
- **Build order.** The published-counts table (migration 0017, `feed_daily_new`) goes first, because its history cannot
  be recovered later. Comparison, Monthly, the drill-down and read rate follow in separate PRs.
- **Reader API reads (#36): closed as not planned.** Kipple measures reading time only in its own web app. A sync client
  sends no read time or scroll, so its reads can never meet the read definition (10 s active, or 25% scroll with 3 s),
  and no request log can change that. The Stats screen and the docs say: "Statistics count reading in the Kipple web app.
  Reading in other apps isn't measured." (a `changes/` entry rides with the stats build). Reopen only if a client starts
  sending read time. No read-state write gets the Recorder, and `TestMarkReadNeverTouchesRecorder` stays as is.

---

## Additional layouts (#39, 2026-10-06)

Design discussion with the owner. The Gazette and the Inbox look below shipped.

### Rules for every new layout
- Every layout has the per-feed and per-folder override and a defined iPhone behavior.
- These layouts arrange many articles at once, so they need a list-level page contract beside the one-row-per-item
  contract. The owner does not want the existing row contract to limit the designs.
- Colors come from the active theme. The unread accent is the theme's accent, never a fixed blue.

### 1. Newspaper: "The Gazette"
- A front page and numbered inner pages, laid out like a printed paper. Fixed rules decide the layout from the content,
  so the same articles always give the same page, and different days look different. No AI, no randomness.
- **Name:** "The Gazette" by default, with a Settings text field to rename it (per device, like other appearance settings).
- **Lead:** the newest article with an image from a pinned source. A pin is a Favorite (feed or folder), the same list as
  the top of the sidebar; there is no separate pin list. No pin icon is drawn on the page. In a single feed or folder
  list there is no pin, so the lead is the newest article with an image in that list.
- **Front-page types**, first match wins: (1) co-leads: two pinned feeds both have images, side by side; (2) lead with
  image: about 26 px headline over the picture, a second column of stories, a row of columns; (3) big headline: the
  pinned feed has no image, so the headline grows to about 40 px and runs the full width with a longer standfirst, and
  with nothing from a pinned feed the longest recent headline leads; (4) quiet day: fewer than about 6 stories, two
  columns and no briefs; (5) busy day: a photo row (at least three more stories with images) and four columns;
  (6) text-only day: no images anywhere, a typographic page. A "picture day" type for very wide images was dropped:
  Kipple does not store image sizes, and adding them needs a migration and a fetch at ingest.
- **Lead window:** the newest image article from a Favorite among the first 100 articles loaded; none means a no-lead type.
- **Order:** always newest first; the per-device sort order is ignored in this layout. The menu's Order choices and the header's Oldest first button stay, greyed with that reason.
- **Reading:** a story marked read fades in place and the page does not reflow: its picture fades and its text turns to
  the secondary color, keeping its weight and size; the page re-plans on refresh or when you
  leave and return. A page's contents are fixed once planned, so loading more never reflows earlier pages.
- **Paging:** all loaded pages stack in one scroll with a page rule and a small header between them.
- **Columns:** two for a few stories up to four for many. Stories that do not fit become headline-only "In brief" lines.
  Titles are bold whether read or not; read ones fade, as in Reading.
- **Inner pages (2, 3, ...):** no masthead; a small header line (name, page number, date), a section header, columns,
  and at most one feature with a picture per section. The last page is briefs only and ends the paper: "That's the
  Gazette."
- **DOM budget:** the Gazette is the one layout that draws every loaded article (no virtualized rows). Node budget UAT (`uat/node-budget.mjs`): Gazette 20 nodes per article (decided; about 15 measured, 25% headroom); the 1,400 cap applies to every other layout.
- **Sections:** the next level down from the list's scope. A list of everything or a parent folder groups by folder; a
  single folder groups by feed name.
- **Finite:** the paper ends. The last page appears once all of the list's articles are loaded.
- **iPhone:** one column in the same order (lead, next stories, then sections), a compact masthead and no folio row.

### 2. Inbox and Headlines: mail-client look
- **Inbox** takes a full-density mail look: a round favicon avatar, source name and time on the first line, the title in
  the theme accent while unread (also bold, with a bar or dot at the left edge), a one-line preview, a pale accent fill
  for the selected row. On hover the time gives way to star, mark read and menu. Date groups (Today, Yesterday, then
  older), an Unread / All tab strip with a Filter control, and a reader pane with a large title, an avatar header and
  an icon toolbar.
- **Headlines** becomes the single-line density of the same look: source, title and time in one row, like a table.
- No other product is named in the UI or docs.

### Not built, not planned
Source rows, a triage stack, a daily edition, Columns, Reader list and Expanded stream were designed and dropped. Nothing
builds them unless the owner reopens one.


## Phone header and back buttons (1.1 bug hunt, round 7, #420 #422 #424)
- **A phone held sideways** (screen height 500 px or less) puts the Show pills and the feed switch on the title row,
  and the tab bar becomes one row of icon and name (44 px instead of 56). Header and tab bar together went from about
  44% of a 375 px screen to about 28%. Rejected: a navigation rail down the left edge, which frees the most height but
  changes the geometry that the menus, the toasts and the safe areas measure from.
- **A phone's list header has at most four controls and a wide screen's at most five**: Article length, Show and sort, Aa
  and the actions menu (and Oldest first on a wide screen). Refresh is the pull on the list and the first item of the
  actions menu at every width; there is no Settings icon in the header (the sidebar and the tab bar lead there). Rejected: one "View" sheet holding every list control, which adds a tap to each of them and waits on the
  bottom-sheet menus.
- **One way back**: a chevron before the title of any screen inside another (a Settings group, Feed health, Your year).
  It is the `BackButton` in `web/src/ui`. Rejected: a text link or breadcrumb ("Stats > Your year"), which is a second
  pattern next to the chevron the Settings groups already had.
- **Stats ranges**: Week, Month, Year, Monthly, All. Monthly is the whole history drawn as one bar per month.

## Phone tabs (1.1 bug hunt, round 7, #421)
- **The first tab is Articles**, not Unread: it holds the lists of the whole library (Unread, All, Starred, and Muted
  once something is muted), opens on Unread, and keeps the unread count as its badge. Feeds holds the lists scoped to a
  feed or folder, Favorites and the way to Feed health; Search holds the saved searches. Rejected: a Starred tab and a
  More tab for Stats and Settings, which costs a tap for the screens that are tabs today and makes More a drawer for
  whatever does not fit; and six tabs, which cannot keep their names whole at the largest text size.
- The Muted pill stays conditional (a list that is empty for most readers is not worth a pill), and Muted, Starred and
  All articles are always listed at the top of the Feeds screen.

## Stats tiles look tappable (2026-10-20, issue #562)

The Stats summary tiles (previous-period figure) and a feed's Read rate (the counts behind it) are buttons, and nothing showed it.

- **Chosen: a chevron cue.** Each such tile carries a small down chevron in its top-right corner in the secondary text colour
  (`text-fg2`, the colour the other row chevrons use, so it holds contrast in every scheme). It turns over while the figure
  is open, and the tile takes the scheme's selection colour while hovered or pressed. The cue is always drawn, so it shows on
  touch where there is no hover. A tile is at least 44 px tall and wide (`hit`, 56 px with Large targets).
- **Not chosen: a card-style tile** (a border and raised surface, with a pressed state and no icon). A border says "box",
  not "opens something", and the look reference's cards are borderless on a tinted surface, so a border would make these
  tiles the only outlined ones; with no hover on touch, the pressed state alone would only show after the tap.
- Tiles that are not buttons (Opens, Avg read, Quick bounce, Opened original, Stars) stay plain: the chevron is the one
  signal for "opens more", so it is not used on anything else in that grid.
- Checked by `web/uat/stats-tiles.mjs`: the chevron holds 3:1 against the tile and against its selection tint in all 20 schemes; the tint shows on hover (a mouse) and while pressed (touch has no hover); tiles are 44 px (56 px with Large targets, also at the largest text size with a long value such as "12h 40m"), and 44 px on a 667x375 landscape phone. The Read rate card is the same height as its neighbours (56 px).

## Feeds screen rows and header (2026-10-22)

- **One row, two places.** The sidebar tree and the Feeds screen draw a feed or folder row with the same component
  (`web/src/screens/feeds/RowLine.tsx`): icon, name, unread count. A count, the "Unread badge" setting (Count, Dot, Off)
  and the 99+ cap are therefore the same on both, Favorites included. A feed or folder with nothing unread is dimmed
  (`text-fg2`), so a missing badge is never ambiguous. Starred and Muted keep a plain count that the setting does not hide.
  Rejected: patching the Favorites rows in place, which left two drawings of one row to drift apart again.
- **A plain visit is the list.** No help sentence under the title; the sentences for Edit and for Select stay, where a
  mode needs them. The header is Edit, Add feed and the Feed actions menu; Select feeds, Show only unread feeds, New
  folder, OPML and move buttons are in that menu. The top of the screen lists only the Feed index and Feed health (the
  whole-library lists are the Articles tab's pills, so the Feeds screen does not repeat them; Settings > Sync & Feeds also
  links to Feed health), and the star stays on every row (the Feeds screen is where favorites are made).
- **Show only unread feeds** is a view of this screen for this visit, not a device setting. Edit and Select always show
  every feed, because a reorder is saved against the whole tree.
- **Mark all as read** is in each folder's menu (the folder and its subfolders), shown only when the folder has unread
  articles. It asks first, naming the reach ("Mark all 12 as read"), like the list's own menu, and the Undo toast follows.
- Every More button draws one glyph (`MoreIcon`, `web/src/lib/itemActionTable.ts`), the Feeds screen's included.

## Item actions (design review 1.5, issue #613)

- One table (`web/src/lib/itemActionTable.ts`) holds the words and glyph of every article action. The row menu, the
  Gazette menu, the open article's More menu and the swipe panel draw from it.
- Row menu order: Star, Mark as read, Mark above, Mark below, Mute similar, then Edit feed, Open original, Copy link and
  Share. Share appears only where the browser can share; Copy link always does. The menu's Star fills while the article is starred.
- The open article's More menu: Mark as read, Mute similar, Edit feed, Open original.
- Icons come in two sizes through the `Icon` wrapper (16 and 20 px). The star marks starred articles and favorite
  feeds and folders; a pin or heart for favorites is declined (#629).

## Vocabulary (design review 1.5, 2026-10-22)

One name for each thing, in every screen, tooltip, label, help text and document. A change to a name is made here first.
The area packages (the sidebar and tab bar, Settings, Stats, setup, feed management) apply it; none of them coins a new
word for something listed.

- **Destinations** (the sidebar and the tab bar; `web/src/lib/destinations.ts` is the one list):
  Unread, All articles, Starred, Muted (the four lists of the whole library), Search, Feeds, Stats, Settings.
  The tab bar's first tab is "Articles" because it holds the four lists as pills; it is not a fifth list.
- **Feeds** is the one word for the place that manages feeds. "Manage feeds", "Feed list" and "Subscriptions" are not
  used. Feed health is a screen of Feeds and Feeds is the current destination while it is open.
- **Your year** is the name of the yearly summary wherever a reader sees it (link, screen title, share text).
  "Wrapped" is the internal name (code, route, settings keys) and never appears in the interface. Your year belongs to
  Stats and Stats is the current destination while it is open.
- **Uncategorized** is the folder a feed is in when it is in no other folder. It is the same in the sidebar, the Feeds
  screen, the editors and the delete messages.
- **Muted** is hidden from the header pills while nothing is muted (a pill that comes and goes would only shift the
  others), and always present in the sidebar and under Feeds.
- **Reading appearance** is the Aa control (theme, font, text size), in the article toolbar, the list header and
  Settings. **Article length** is the estimate of minutes for an article and the header filter built on it; **Reading time**
  is the Digest's sections by that estimate. The two words are never swapped.
- **This device** and **All devices** are the only scope words in Settings and in help text. "Everywhere", "locally"
  and "on the server" do not say where a setting applies.
- **Stats**, not "Statistics", is the name of the screen and of its Settings group.
- **Articles**, not "items" or "entries", in every sentence a reader sees. **Read** and **unread** are the states;
  "opened" is a statistic, not a state.
- **Pause** is what a feed's off switch does ("Pause this feed"); a feed is never "disabled" or "muted".
- Wording for people who do not run servers: no file names, environment variables or protocol names in a screen's own
  text. Those belong in the documentation and behind "Copy debug info".

### The current destination

At most one destination is current at a time. The sidebar and the tab bar read one function
(`currentDestination(pathname, search)`): a list scoped to a feed or a folder belongs to Feeds, not to Unread; Feed
health belongs to Feeds; Your year belongs to Stats. Other screens (an article, setup) have none. The sidebar shows no
destination row as current on a feed's or a folder's list, because the tree below it marks the feed or folder itself,
and none on an article. The tab bar marks Feeds there. The Show pills in a list header are not destinations: they read
the list's own view (`scope.view`).

### Resize handles

A resize handle draws a thin line on the column's edge and has a 24 px hit area wholly outside the column, so it never
covers the column's scrollbar, and what sits in the column stays at least 8 px clear of it. The sidebar's handle stays
a Tab stop: the arrow keys, Home and End are the only keyboard way to resize, and a splitter is focusable by its pattern.
## The Digest (#39, Phase C)

- **What it is.** One page of sections, a section for each folder (or feed, a device setting), each with its name in the
  theme accent as a level 2 heading, its first few unread articles and "N more in <name>". It is for reading a little of
  everything: a quiet folder is as visible as a busy one.
- **Row.** Title first (the reading font, bold while unread), then source and time; a picture at the right when Pictures is
  on and the article has one. Text only is a choice, not a fallback.
- **Order.** Sidebar order, or Quiet Feeds First (sections that get the fewest new articles a day first, a folder's pace being the sum of its feeds; a section with any feed under a week old has no pace yet and goes last). The name is
  plain on purpose; no print vocabulary.
- **Settings, for this device, in the layout menu.** Order, Sections (Folders, Feeds or Reading time) and Rows per section (3, 5 or 10). They are not per-list overrides.
  Whether rows show pictures is the device's Pictures setting, under Show and sort in the menu.
- **"N more" opens the section in place**, 20 at a time, rather than going to the section's list: the reader keeps their
  place on the page.
- **Sections load as they are reached** (6, then 4 at a time), and one that fails shows its own Retry.
- **Mark above and below stay inside a section.** The page is not one date-ordered list, so "above" means above in the
  section the article is in.
- **Rejected:** one merged list cut by headings (a long folder would push every other out of reach and the count under
  each heading would be wrong); a fixed number of articles with no way to open the rest.

## The New page (#39, Phase C)

- **What it is.** A page for catching up: the unread articles that arrived since the last visit, a section for each
  top-level folder, the newest three of each, "N new" in its heading and "N more in <name>" to open the rest in place.
  It is the Digest with a different list, not a second layout: the same rows, headings, keys and Mark above and below.
- **Name and place.** "New", the first of the Show pills on the library (not on a feed or a folder, where the question
  is not "what is new to me"). Not a view in the sidebar: it is a way of looking at Unread.
- **One mark for the account.** Where the reader left off is kept on the server, so a phone and a laptop agree about what
  is new. It is the newest article the page knew of when it was left.
- **It moves when you leave, not when you look.** Opening an article from the page and coming back, hiding the tab, and
  switching windows do not move it. Closing the tab or reloading does not move it either, so the page shows the same
  articles again: a catch-up that quietly forgot what you had not read would be worse than one that repeats itself.
- **Two devices.** The later leave wins; an older page cannot move the mark back. A page left open for days keeps showing
  its own list until it is left or reloaded.
- **Fixed look.** Three rows, folders as sections and sidebar order, whatever the Digest's settings say on that device:
  the page answers one question and has no layout menu. "N new" is the server's count at the time the page opened and
  does not change as the reader reads.
- **Ends.** A line closes the page ("That's every folder with something new.") once every section is listed, and when nothing arrived it says "Nothing new" and points
  to Unread.
- **Mark all** marks only what arrived after the mark, not older unread articles the page does not show.
- **Rejected:** moving the mark on opening (a phone call would lose the page); on every scroll (a half-read page would
  not come back); a per-device mark (the point is that the second device knows what the first showed).

## The Feed index (#39, Phase C)

- **What it is.** One block for every feed: its name (a link to the feed), "N unread" or "All read", and its newest
  three headlines. It answers "what is each of my feeds up to", which no list of articles does.
- **Name and place.** "Feed index", under Feeds: a row after Feeds in the sidebar and a link on the Feeds screen, where
  the Feeds tab stays current. It is not a list view, so it has no pills.
- **Newest means newest of all.** Read articles count, so a feed that is all read still shows its last three. The
  unread count beside the name says what is left to read.
- **Order.** Sidebar order or Quiet Feeds First, the Digest's setting, with an Order menu on the page itself.
- **Failing feeds** keep their block and add the line their own list shows; the page does not hide a feed that is
  broken.
- **Keys.** The Digest's: one Tab stop, arrows, Home, End and Page keys through names and headlines.
- **Back.** A headline opens its article inside the feed's own list, so next and previous stay with that feed. Back (the
  button, the swipe, Esc, the browser's Back) returns to the index, as history does, scrolled where it was left with
  the headline that was opened focused.
- **Nothing to show.** A feed with no article, or only muted ones, says "No articles to show." under its name.
- **Rejected:** a layout of the list screen (its mark-all and scope belong to one list of articles); showing only
  unread headlines (a quiet library would show empty blocks).

## The Coffee break (#39, Phase C; reversed in 1.6)

- **What it is.** Unread articles grouped by how long they take: Under 3 minutes, 3 to 7 minutes, 8 minutes and up,
  and Unknown length. It answers "what can I read in the time I have", which no date-ordered list does.
- **Reversed: it is a Sections choice, not a layout.** Phase C made it a layout of its own. In practice it was the Digest
  with a different cut, and it was not itself everywhere but Unread: in All, Starred, Muted and under a length filter
  it silently drew Compact rows, so a reader who chose it saw a different layout depending on the list. The layout menu
  also grew a block of its own and the order rule a branch for it. Now the Digest's Sections are Folders, Feeds or
  Reading time. Reading time is enabled on Unread and greyed, with the reason beside it, on every other list; there the
  Digest draws Folders. A device that had the Coffee break as a layout (or for some lists) opens the Digest cut by
  reading time.
- **Fixed order and no empty groups.** Shortest first. A group with nothing to read is not drawn; one the reader has
  just finished stays until they leave, so a mark is never a disappearing act.
- **Same page as the Digest.** The rows, "N more" opening a group in place, keys, and Mark above and below within a
  group. Rows per section and Pictures are the Digest's device settings.
- **Design twice.** A: each group is an ordinary list of the same scope plus a reading-time bucket, so it reuses the
  Digest's planner, lazy loading and mark rules, and the server learns one parameter (`unknown_length`). B: a new
  endpoint returning pre-grouped buckets with exact counts. B gives exact "N more" numbers at once, but costs a second
  list contract, a second set of keys and mark scopes, and a page that behaves like no other list. A won; the cost is
  that "N more" says "More" until a group is loaded.
- **Names.** The header filter that keeps one length is "Article length", so "Reading time" names one thing: the
  Digest's sections.
- **Rejected:** a length slider (the filter menu already has lengths); time badges only (no grouping); keeping two
  layouts and only explaining the fallback.

## Pictures, order and the layout menu (Build 4/6 UI, PR C)

- **One picture setting.** "Pictures: Show or Hide" (`client.list_images`) replaces "Thumbnails in Inbox" and the
  Digest's "Images". It covers the Inbox thumbnails and the Digest images; Editorial and Cards always show pictures. It
  appears twice, bound to the one value: in Settings, Appearance, Lists, and in the layout menu, as Pictures under Show and sort. Rejected: a
  picture control in each layout's own block, which repeats the problem of a control that exists only under some layouts.
  An old cache or stored profile that hid pictures in either place keeps them hidden (read once; migration 0021 rewrites the
  stored profiles).
- **"Pictures" means display only.** The server's image proxy group in Settings is "Image cache".
- **Order and Group by never disappear.** Where a layout or a search sets its own (the Gazette and the Digest are always
  newest first; the Gazette prints its own pages, the Digest groups by section, a search keeps its own headers), the radios
  are greyed with a one-line reason and stay focusable (`aria-disabled`, with the reason as their description), so a
  keyboard or screen reader user reaches the reason. The wide header's Oldest first button greys with the same reason, so it
  is never live while the menu's radios are greyed. Rejected: hiding the inert Order the way Group by was hidden; a control
  that vanishes leaves no explanation.
- **Groups (superseded by "One flat list", below).** The layouts sat under three headings: Pictures (Editorial, Cards),
  Lists (Compact, Inbox, Headlines) and Pages (Gazette, Digest), each a labelled group.
- **The trigger is "Show and sort"** (button name "Show and sort, Cards layout"). The other candidate was "Arrange list".
  "Show and sort" names the two jobs (what is shown: layout and pictures; how it is sorted: order and grouping), shares no
  word with "List actions", and the name does not claim more than the menu holds. Rejected: "Layout and order", which no
  longer fits a menu that holds pictures.

### One flat list (Build 4/6 UI, PR H)

- **One radio group of the seven layouts.** Compact, Headlines, Inbox, Editorial, Cards, Gazette, Digest, in that order
  (densest to most page-like, the order Settings uses too). Each row is a small drawing of the layout, its plain name and a
  short hint (`LAYOUT_HINTS`, shared with Settings). There are no category headings, no sub-groups and no "Picture" or
  "Picture in list" naming. Below it, under Show and sort: Order, Group by, Pictures (Show or Hide), and on a feed or folder
  Opens in; the Digest's own block follows when the Digest is the layout. The drawing is hidden from assistive technology; a
  row reads as its name and hint. Arrow keys move through every row, Enter or Space chooses, Escape closes.
- **Why.** The three headings were added for screen-reader grouping, not for a task. A reader choosing a layout wants to see
  what each looks like, and a name under a heading does not say that. Pictures held two layouts while the Pictures setting
  sat inside it. One list also scales: an eighth layout is a row, not a new category.
- **Design twice.** A: a segmented row of seven drawings. B: one flat list with a drawing and a hint per row. A fits a phone
  badly (seven tap targets of about 40 px, names only in a tooltip or a toast) and has no room for a hint. B costs height,
  so the phone sheet scrolls to reach Show and sort. B won. A grid of tiles and a one-step cycle button were also drawn and
  rejected for the same reasons.
- **Spacing is not in the menu.** The first sketch listed it. It is the text-spacing accessibility setting, device-wide and
  not a list choice, so it stays in Settings, Appearance.
- **Pictures says where it applies.** A muted line, "Applies to Inbox and Digest.", sits under Show and Hide: only those two
  layouts have pictures that Hide can remove (Editorial and Cards always show theirs, Compact and Headlines have none).
- **Hints checked against the code.** The Gazette is always newest first (`followsOrder`), so "Pages, newest first" is true;
  the Digest offers Folders, Feeds and Reading time, so its hint names all three.

### Helper text (Build 4/6 UI, PR G)

- **Why.** Text under a control that says what the label already says tells a reader the screen thinks they are slow. Each
  line is also one more thing to read, translate and keep true.
- **Cut.** A description that restates its label ("Text size: Makes all text larger or smaller"). "Tap" or "click" for a
  standard control. An explanation of something obvious. Reassurance and marketing tone. Empty-state text longer than one
  short line, or that repeats the title (`EmptyState` `why` is optional).
- **Keep.** A consequence the label cannot show: what happens to data, who sees it, what is lost, what is left alone.
  Units, limits and defaults. Why a control is disabled (the reason is its `aria-describedby`). An error that says how to
  fix it. Text a screen reader needs for a name. Legal and security wording, release notes, the setup wizard's security
  warnings, the layout hints and the Pictures note.
- **Test for a line.** Delete it. If a reader would do something different or lose something, put it back.

## Internal cleanup lines ruled out (#207)

Five "do someday" cleanups were each looked at against the code as it is and ruled out. None has a measured symptom, and
each can be reopened by one.

- **Move JSON-blob data into tables.** Settings are JSON values by decision (`internal/store/migrations/0001_init.sql`),
  and a typed settings table is a standing "never". Nothing reads a blob in a way a table would make cheaper.
- **Account-row caches.** One snapshot cache exists (`internal/greader/api.go`); a second was looked for and not found.
- **Shared User-Agent vocabulary.** There is one source (`internal/api/api.go`).
- **Phone list unmount.** No measured symptom. The node budget added with the list work would show one.
- **Unread-counts timer.** No measured symptom, and the same node budget covers it.

The hygiene report script is not built. The checks it would have run are three commands in
[handover.md](handover.md#routine-repository-checks).
