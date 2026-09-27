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
auto-night. See memory `parking-lot`.

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
  Editorial and Headlines Email - Compact, ids unchanged). Parked: Columns, Reader list,
  Expanded stream.
- Layout is choosable **per feed and per folder** (override), plus a global/device default. UI in phase 2.
- Density: the owner doubts sliders. Use **named steps in a segmented picker with a live preview**
  (Dense / Snug / Standard / Relaxed / Airy). One "Density" choice drives both list rows and reading text;
  an "Adjust separately" disclosure splits them. No sliders, no "link" toggle. Must not look messy.

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
  mark read, star, highlight, saved searches and auto-read. Not shipped: only-show-matching, a
  reading-time filter UI (the API has `min_minutes` and `max_minutes`), and per-feed view/order (order
  is per device).) Quiet hours rejected.
- Image cache default cap **1 GiB** (check Host-A free disk before deploy). Default mode: **all images**
  (inline too) through Kipple, matching common RSS-reader practice; enables the strict CSP.
- Backup export, `kipple restore`, `kipple password`; ship them (and take an off-box export) before the
  0004/0005 migrations reach the live DB. Implementation order: `backend-additions-round2.md` §11.

### Working agreement
- Commit and open PRs without asking. Merge docs-only PRs when CI is green. Code PR for `phase-2` opens at
  deploy time. Reviews (Opus, high) after every two or three backend steps; fix all findings.

## Stats round (phase 4 pre-meeting, 2026-09-26)

Decisions are recorded in full in the private history repository. Summary of what shapes the build:

- **Focus:** sources and pruning (most read, time per source, never opened) with habits at the top (summary strip,
  daily activity, streaks, weekday-by-hour heatmap, behavior facts). Wrapped is a simple yearly summary with an opt-in
  share sheet, aggregates only by default. No goals, targets, badges, comparisons with other people or directives.
- **Reading:** every open is recorded; views count an item as read at 10 s active time or 25% scroll. List-preview
  opens count. Reading stats (open, read time, scroll, open original, share) are web-only; Reader API clients are not tracked for reading (`stats.api_single_read_is_open` stays off), though their stars are recorded.
- **Screen:** one Stats nav entry, phone first. Range Week/Month/Year/All (default Month). Items/Minutes toggle with
  folder rollup; average read length, quick-bounce rate, open-original rate, most-starred feeds; never opened.
  Deferred: per-feed drill-down, period comparison, monthly charts, read rate per feed.
- **Settings:** first day of week (Sunday or Monday, default Sunday); stats on/off (off stops recording and hides the
  screen, keeps data); Wrapped on/off; delete a range; delete all (typed confirmation).
- **Export:** CSV and JSON (array or JSON Lines), raw or summary, range, titles/URLs toggle, data dictionary.
- **Sender:** 15 s flush, visibilitychange primary and pagehide backup, 2 minute idle cutoff, a random id on every event
  (unique index) for dedup, offline events queued, losses accepted.
- **Delivery:** alpha.4 sender and settings, alpha.5 screen, alpha.6 export and data controls, alpha.7 Wrapped.
