# UI design meeting: decision log

Round 1, 2026-09-25 (the owner and Claude). Inputs: `docs/research/ui-principles-and-accessibility.md`,
`ui-color-schemes.md` (+ `.json`), `ui-gestures-and-layouts.md`, `design-audit-2026-09-25.md`.
This file records what the owner decided. It is the authority where the research files differ. Round-1 TODOs
were resolved in round 2 and what shipped; each is annotated below.

## Accessibility

- WCAG deviations are fine as defaults **provided an option exists to enable the accessible behavior**,
  and enabling any option must never break anything else.
- Implement everything the research file lists for **phase 2 and phase 3**, including read-aloud,
  `prefers-contrast`, reduced motion, text scaling, focus and semantics, live region for new items.
- More **density** options than the three presets (TODO, resolved: Dense, Snug, Standard, Relaxed and Airy steps, see
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
- **Follow system** stays. Default pair when no custom pair is chosen: Paper (day) / Midnight (night)
  (the owner wrote "paper/onyx"; Onyx was Midnight's alternate name, so Midnight is assumed. Confirm). An
  option lets the owner pick any custom day theme and night theme for follow-system.
- Midnight text `#e0e0e0` is bright enough. **No image dimming** in dark themes.
- No Kipple identity/accent color now. Parking lot: a design system, a static demo site, and pull-and-run
  image for others (Kipple stays single-user).

## Gestures and navigation

- Must be iOS compliant in the least annoying way (nothing starting in the left ~24 px; no fighting Safari's
  back gesture; every gesture has a button).
- Haptics not needed; visual snap plus **undo toast** is approved.
- **Row swipe: full swipe with undo. Swipe left = mark read, swipe right = mark unread.** (Star: TODO, resolved: a row button, a swipe, `s` and the row menu.)
- **Article swipe: swipe back to the previous screen** (the article list you came from, not always the feed
  list), i.e. drill-in navigation stack. Keep next/previous **buttons** and **j/k**.
- Keys: j/k next/previous, s star, o open original, r refresh, m mark read (plus common conventions, TODO
  keymap; resolved, the KEYMAP in `web/src/lib/keys.ts`).
- First-run **peek animation** teaches swipe reveal.
- Mark-read-on-scroll: keep as a setting, **off by default**.

## Layout

- **Magazine is the default**; compact list and cards also ship. the owner likes the proposed layouts and wants
  other options explored, notably an **email-style ("Inbox") layout** (TODO research, resolved: Inbox shipped; the five layouts are
  Editorial (id `magazine`, this decision's "Magazine"), Cards, Compact, Inbox and Email - Compact (id
  `headlines`)).
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

## Parking lot additions

Design system / static demo site / pull-and-run image; Cloudflare Access JWT; passwordless login; scheduled
auto-night. The parking lot itself is kept outside this repository. (Cloudflare Access JWT and passwordless login
shipped in phase 5 as #40, the scheduled auto-night theme as #41; see "Phase 5 planning meeting" below.)

## Next chunks

All four steps are done (historical list).

1. Round-2 research (colors, layouts incl. Inbox, keymap, gesture spec, filters brainstorm, accessibility spec).
2. Consolidate into a UI spec and update `design.md` (audit contradictions).
3. Backend items these decisions add (image cache, mute filters, mark above/below, oldest-first order,
   per-device settings, backup export, security headers, FTS stemming, health status reconciliation).
4. Frontend build after the owner signs off the UI spec.

---

## Round 2 answers (2026-09-25, later)

Authoritative over the round-1 and round-2 research files where they differ. the owner: "Go ahead and begin
implementing anything. Commit and PR as needed without asking." (Host-A deploys still need his go-ahead.)

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
  Editorial and Headlines Email - Compact, ids unchanged). Columns, Reader list and Expanded
  stream were parked, then replaced by the layouts in "Additional layouts (#39)" below.
- Layout is choosable **per feed and per folder** (override), plus a global/device default. UI in phase 2.
- A folder's layout override applies to its subfolders and their feeds too; the nearest override up the tree wins
  (feed, then its folder, then each folder above, then the device default).
- Density: the owner doubts sliders. Use **named steps in a segmented picker with a live preview**
  (Dense / Snug / Standard / Relaxed / Airy). One "Density" choice drives both list rows and reading text;
  an "Adjust separately" disclosure splits them. No sliders, no "link" toggle. Must not look messy.

### Folders (nested, #209)
- The sidebar shows folders as a **tree** (WAI-ARIA navigation tree: one tab stop, arrow keys move, Right/Left expand
  and collapse or step into and out of a folder, Enter opens the list). Each level indents 12 px, less past the
  fourth so deep trees fit a phone. A folder's subfolders come before its own feeds. A branch with no feed anywhere
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
- In the sidebar trees the item is the only tab stop: the chevron and the star are pointer targets there, and **P**
  adds the focused item to the favorites or takes it out (listed in the shortcuts overlay under Sidebar).
- A subfolder's or feed's layout menu names where its layout comes from: "Inherited from Tech (Cards)".
- Deleting a folder says how many subfolders are deleted with it and that its feeds move to the default folder.
- OPML import has an opt-in switch, off by default: "Move feeds that already exist into the file's folders".

### Gestures and keys
- **Swipe directions match iOS Mail everywhere** (this supersedes round 1's "read left, unread right"):
  swipe right (leading) = toggle read/unread; swipe left (trailing) = star, plus a "More" action opening the
  row menu. Full swipe commits with undo. Star also has a row button, `s`, and the long-press menu.
- Undo toast lasts **15 seconds** (merged toast; `z` undo stack as specified).
- Single-key shortcuts: global on/off setting. `c`: toggle the Compact layout (match the layout system).
  `m` in Unread view: row dims in place (shipped differently: a row marked read on purpose dims,
  then leaves the Unread list after 1.5 s with the undo toast up; the open article leaves when you
  move off it; a swiped row leaves at once; undo or mark unread cancels it). `Shift+A` marks only items present when the list loaded. Mark
  above/below approved (anchor excluded, disabled for relevance-sorted search).

### Backend features
- Filters family **F1 to F7 in phase 2** (mute, mark read, auto-star, only-show-matching, highlights, saved
  searches, auto-read after N days, reading-time filter, per-feed view/order). (Shipped in phase 2: mute,
  mark read, star, highlight, saved searches and auto-read. The rest, only-show-matching, the reading-time
  filter UI and per-feed view and order, shipped with #38; see "Filters follow-ups" below.) Quiet hours rejected.
- Image cache default cap **1 GiB** (check Host-A free disk before deploy). Default mode: **all images**
  (inline too) through Kipple, matching common RSS-reader practice; enables the strict CSP.
- Backup export, `kipple restore`, `kipple password`; ship them (and take an off-box export) before the
  0004/0005 migrations reach the live DB. Implementation order: `backend-additions-round2.md` §11.

### Filters follow-ups (#38, 0.8.0-beta.3)
Choices made where the decisions above were silent; the owner may overrule any of them.
- **Only show matching** is a choice under "What it does", stored as an inverted Mute (no new action, no schema
  change). For Mute the "Act when it does NOT match" option is that choice, so the checkbox is offered for Mark as
  read and Star only.
- **Reading-time filter:** three lengths, "5 min or less", "6 to 15 min" and "Over 15 min", from a timer button in the
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
- Layout, order and view of one list are one object per feed or folder in the device profile
  (`client.list_overrides`), set from the list header's options menu (its button reads "List options, Cards layout")
  and from the feed and folder editors, whose first choice names what the list inherits, as the menu does.
### Working agreement
- Commit and open PRs without asking. Merge docs-only PRs when CI is green. Code PR for `phase-2` opens at
  deploy time. Reviews (Opus, high) after every two or three backend steps; fix all findings.

## Stats round (phase 4 pre-meeting, 2026-09-26)

Decisions are recorded in full in the private history repository. Summary of what shapes the build:

- **Focus:** sources and pruning (most read, time per source, never opened) with habits at the top (summary strip,
  daily activity, streaks, weekday-by-hour heatmap, behavior facts). Wrapped is a simple yearly summary with an opt-in
  share sheet, aggregates only by default. No goals, targets, badges, comparisons with other people or directives.
- **Reading:** every open is recorded; views count an item as read at 10 s active time, or 25% scroll with at least 3 s active time (a scroll alone stopped counting with issue #120). List-preview
  opens count. Reading stats (open, read time, scroll, open original, share) are web-only; Reader API clients are not tracked for reading, though their stars are recorded.
- **Screen:** one Stats nav entry, phone first. Range Week/Month/Year/All (default Month). Items/Minutes toggle with
  folder rollup; average read length, quick-bounce rate, open-original rate, most-starred feeds; never opened.
  Deferred: per-feed drill-down, period comparison, monthly charts, read rate per feed.
- **Settings:** first day of week (Sunday or Monday, default Sunday); stats on/off (off stops recording and hides the
  screen, keeps data); Wrapped on/off; delete a range; delete all (typed confirmation).
- **Export:** raw events as CSV, JSON or JSON Lines; the summary is JSON only (a summary is several tables, so a CSV or JSON Lines summary was not built). Range, a toggle to leave out article titles and links (feed and folder names, times and the time zone stay), and a data dictionary. Export and delete stay available with statistics off.
- **Sender:** 15 s flush, visibilitychange primary and pagehide backup, 2 minute idle cutoff, a random id on every event
  (unique index) for dedup, offline events queued, losses accepted.
- **Delivery:** alpha.4 sender and settings, alpha.5 screen, alpha.6 export and data controls, alpha.7 Wrapped.

## Phase 5 planning meeting (2026-09-27)

Not a UI meeting — recorded here per the owner's instruction that all planning decisions land in this file plus
`kipple-history`. Phases 1-4 are complete (v0.3.0-alpha.7 deployed, kipple.cc public). Six topics, one at a time
with a recommendation each, same format as the phase 4 pre-meeting.

1. **What phase 5 is.** Not release-steps-only, not parking-lot-only: the owner chose to **interleave** release
   readiness (steps 8+) with the two parking-lot items that were explicitly gated on "planned work finished"
   (Cloudflare Access JWT validation, passwordless login).
2. **Sequencing.** The owner chose **fully parallel**: the code audit/changelog review and the Access
   JWT/passwordless work happen on separate branches at the same time, accepting the risk that the audit could
   flag something in the auth path and cause rework, rather than sequencing them.
3. **Stale owner checklist (from the phase 3 handoff).** GitHub private vulnerability reporting toggle, approving
   a proposed CLAUDE.md edit list (kept in the private history repository), and turning off Host-A debug logging
   (`KIPPLE_LOG_LEVEL`, `KIPPLE_LOG_GREADER_FORMS`) were never marked closed. The owner will flip debug logging
   off himself (ssh to Host-A, edit `.env`, restart `kipple`). A Host-A-side off-site backup job for Kipple's own
   data (separate from Host-B's own off-site backup job) is **not** being built in phase 5 — local `docker cp`
   snapshots stay the only backup path for now. (Private vulnerability reporting has since been turned on.)
4. **Parking-lot scope boundary.** The owner's rule for phase 5: **only work directly related to Kipple and its
   Docker image.** In: auto-night theme (small, self-contained, ships in phase 5). Out: the 1.5.0/2.0.0
   setup-app/single-image roadmap, a design system, a static demo site, and user-chosen Google Fonts (all stay
   parked, unchanged from the existing parking-lot timing).
5. **Owner involvement.** The owner wants to be **involved as little as possible** in phase 5. Practical reading:
   batch work into branches/PRs and only interrupt him for the things CLAUDE.md already reserves for him —
   deploys, Cloudflare changes, and the final go/no-go — not for routine build decisions in between.

**Phase 5 outline (final):**
- (A) Full code audit + changelog review (Sonnet routine, Opus review/judging, per the existing model policy).
- (B) Cloudflare Access JWT validation + passwordless login, on a branch, in parallel with (A). Needs the owner's
  Cloudflare-side Access app config before the JWT work can be verified end-to-end.
- (C) Auto-night theme.
- (D) Documentation run, first-time Docker setup walkthrough, backup/restore-settings guide.
- (E) Final go/no-go meeting.

No deploys happen without asking first, per standing instruction; Cloudflare changes stay the owner's.

### Addendum (2026-09-27, later): UAT and release-process gaps

The owner asked to evaluate a third-party Claude Code skill (`mecabots/webapp-uat`, GitHub `tsilverberg/webapp-uat`)
for UAT. Recommendation given and accepted: don't install it (unverified npm scope mismatched from the GitHub repo
owner, and its i18n/placeholder checks don't apply to Kipple), but build the useful parts — console/network error
capture, WCAG audit, responsive checks — as an in-repo Playwright + axe-core script instead, so it stays auditable
and dependency-light.

The owner also asked to study general UAT methodology (testmonitor.com's UAT guide) and produce a Kipple-specific
UAT plan, and to add these release-process gaps as phase 5 line items:
- A Kipple-specific UAT plan, `docs/uat-plan.md` (roles, entry/exit criteria, scripted/agent-driven/owner-only
  test suites, defect severity scale, sign-off feeding the go/no-go meeting).
- Reader API regression replay (Reader API client recorded sequences) against the actual deployed
  build, not just CI's unit-level contract tests.
- Migration rehearsal against a copy of the live Host-A DB, made a standing checklist item rather than ad hoc.
- An actual end-to-end `kipple restore` drill (not just documentation) — first real run this cycle.
- The first-time Docker setup walkthrough treated as literal UAT (follow it verbatim on a clean machine, log
  every stuck point) rather than a documentation paraphrase exercise.

**Phase 5 outline, updated:** (A) code audit + changelog review (DONE, PR #26); (B) Cloudflare Access JWT +
passwordless, in parallel with A (DONE, PR #40); (C) auto-night theme (DONE, PR #41); (D) documentation run
(DONE, #42; a further pass for the 0.5 setup wizard is PR D of the setup wizard work) + Docker walkthrough (now doubling as UAT Suite 5) + backup/restore-settings guide; (F) UAT plan execution (`docs/uat-plan.md`
Suites 1-4, migration rehearsal, restore drill, Reader API regression replay); (E) final go/no-go meeting, fed by
D and F's sign-off.

---

## Additional layouts (#39, 2026-10-06)

Design discussion with the owner. Mockups were reviewed in the session; nothing is built yet. Columns, Reader list and
Expanded stream (the parked names) are dropped; Expanded stream may return later. The ideas below replace them.

### Rules for every new layout
- Every layout joins the `c` toggle and the per-feed and per-folder override, and has a defined iPhone behavior.
- These layouts arrange many articles at once, so they need a list-level page contract beside the one-row-per-item
  contract. The owner does not want the existing row contract to limit the designs.
- Colors come from the active theme. The unread accent is the theme's accent, never a fixed blue.

### 1. Newspaper (first priority): "The Gazette"
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
- **Order:** always newest first; the per-device sort order is ignored in this layout.
- **Reading:** a story marked read fades in place and the page does not reflow; the page re-plans on refresh or when you
  leave and return. A page's contents are fixed once planned, so loading more never reflows earlier pages.
- **Paging:** all loaded pages stack in one scroll with a page rule and a small header between them.
- **Columns:** two for a few stories up to four for many. Stories that do not fit become headline-only "In brief" lines.
  Unread titles are bold, read ones fade.
- **Inner pages (2, 3, ...):** no masthead; a small header line (name, page number, date), a section header, columns,
  and at most one feature with a picture per section. The last page is briefs only and ends the paper: "That's the
  Gazette."
- **Sections:** the next level down from the list's scope. A list of everything or a parent folder groups by folder; a
  single folder groups by feed name.
- **Finite:** the paper ends. The last page appears once all of the list's articles are loaded.
- **iPhone:** one column in the same order (lead, next stories, then sections), a compact masthead and no folio row.

### 2. Source rows
- One horizontal strip of tiles per feed (image, title, age), newest first, feeds in sidebar order, feeds with nothing
  unread at the bottom. A tile opens the article; the feed name opens that feed's list. Feeds without images get text-only
  tiles. iPhone: each strip swipes sideways with the next tile partly visible.

### 3. Triage stack
- One article card at a time (image, source, title, excerpt), the next card showing behind it. Swipe directions follow the
  rest of the app: right marks read, left stars; tap opens the article; each action advances and shows the undo toast.
  Desktop: arrow keys and Enter. A "N left" counter. iPhone-first.

### 4. Daily edition: parked
- A finite page for one calendar day was mocked and dropped for now. The approach needs rethinking; do not build it from
  these notes.

### 5. Inbox and Email - Compact: mail-client look
- **Inbox** takes a full-density mail look: a round favicon avatar, source name and time on the first line, the title in
  the theme accent while unread (also bold, with a bar or dot at the left edge), a one-line preview, a pale accent fill
  for the selected row. On hover the time gives way to star, mark read and menu. Date groups (Today, Yesterday, then
  older), an Unread / All tab strip with a Filter control, and a reader pane with a large title, an avatar header and
  an icon toolbar.
- **Email - Compact** becomes the single-line density of the same look: source, title and time in one row, like a table.
- No other product is named in the UI or docs.

### Still open
- Whether a Columns-style text layout is worth shipping next to Cards (no owner intent yet).
- Whether Expanded stream returns (full text under each headline, collapsed by default, no scroll-based read marking).

