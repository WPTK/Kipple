# Design review

How Kipple's whole-app design review works: what is captured, how it is judged, and how findings become issues. The
Browser UAT flows (`web/uat/`) prove that things work; they never say whether a screen looks or reads right. This review
does that. Findings go on the `1.5 - Design review` milestone with the `design-review` label.

## Captures

The review looks at pictures of every surface: each screen, state and open menu, on a phone (390x844), a tablet
(820x1180) and a desktop (1440x900), in the chosen themes, plus the smallest and largest text sizes on the screens with
the most text.

```
cd web
KIPPLE_SEED_SET=design npm run seed                         # terminal 1: an offline library built to stress the design
node ../scripts/maintainers/design-shots.mjs --out ../.design/shots       # terminal 2
node ../scripts/maintainers/design-shots.mjs --out ../.design/all --themes all --devices phone --no-sizes   # every theme
```

The output (gitignored) is one PNG per capture, `manifest.json`, `index.html` (the contact sheet, `?area=lists` for one
area) and `sheet-<area>.png` per area. The default run takes about 40 minutes; keep the seed running until it ends. `--only <regexp>` captures some surfaces, by id. A surface that cannot be reached
is listed under `missed` in the manifest, and the run exits 1.

The design library (`web/scripts/design-feeds.mjs`) has a feed with hundreds of unread articles, titles from one word to
three lines, a long feed name, pictures in landscape, portrait, panorama, square and too small to lead, articles with no
picture or no body, a long article with every block type, Unicode and right-to-left text, three nested folders, an empty
folder, an empty feed, a quiet feed, an unfiled feed and a feed that fails. The seed also stars, reads and mutes some
articles and saves a search. Stats start empty, because the seed records no reading.

### Surfaces and areas

| Area | Surfaces |
|---|---|
| lists | the six layouts on All unread, a folder, a long feed name, odd picture shapes, Unicode titles |
| states | Starred, Muted, an empty feed, nothing unread, a failing feed, not found |
| article | a lead picture, a long article, Japanese text |
| menus | the article's More actions, a row's More actions, List options |
| navigation | the Feeds screen (on a desktop the sidebar is in every list capture) |
| search | nothing typed, results |
| stats | Stats, Wrapped |
| settings | Feed health, each settings group |
| setup | the sign-in screen |

Not captured yet: the setup wizard (needs `KIPPLE_SEED_SET=fresh`), dialogs (add feed, edit feed, delete), toasts,
offline, drag and drop, and the gestures themselves. The review covers those by driving them, or a later version of the
script adds them.

## Measured checks

`scripts/maintainers/design-probes.mjs` loads the same surfaces (they live in `scripts/maintainers/lib/design-surfaces.mjs`,
which both scripts import) and measures them against published thresholds instead of picturing them. Each probe is one
condition:

| Probe | What it measures |
|---|---|
| `base` | Every surface and device in Paper: targets under 24 px (44 px on a phone or tablet) and small targets under 8 px from a neighbour, non-text contrast (icons of icon-only controls, unread markers, control boundaries, selected states), clipped and overlapping text, sideways scrolling, every axe-core WCAG 2.2 AA rule, the accessibility tree (unnamed controls, repeated names, heading levels; the snapshots are saved for reading as copy), layout shift, and the interface inventory |
| `themes` | Text contrast (axe) and non-text contrast on the busiest surfaces in all 20 themes |
| `reflow` | 320x256 (400% zoom), 280 px, two landscape phones and a desktop at 200%: sideways scroll, cut-off content, clipping, overlap |
| `spacing` | The WCAG 1.4.12 text-spacing override and the largest text size: only what is newly clipped or overlapping |
| `reading` | Article line length, body size and leading at every text size on each device, and empty states that sit small at the top of a blank pane |
| `forced` | Forced colors (icons, selected states, focus rings) and `prefers-contrast: more` |
| `vision` | Every colour-vision deficiency Chromium emulates, grayscale, blur and low contrast, side by side; whether unread is told from read by colour alone in each layout |
| `throttle` | A slow network and a 6x slower CPU: layout shift, skeletons that flash or differ from the rows that replace them, with frames |
| `keyboard` | Tab through each screen: focus visible, ring contrast, focus hidden under a bar, order; a numbered tab-order overlay per screen |
| `rtl` | Right-to-left: sideways scroll, clipping, overlap |
| `long` | A 100-character feed name, a long compound and a CJK folder name (renames them, then restores them; run it alone) |
| `stuck` | Opens articles again and again on a phone and times the loading skeleton |

```
cd web
KIPPLE_SEED_SET=design npm run seed                                                  # terminal 1
node ../scripts/maintainers/design-probes.mjs --out ../.design/probes                # terminal 2, all but long (about an hour)
node ../scripts/maintainers/design-probes.mjs --out ../.design/probes --probes long
node ../scripts/maintainers/design-probes.mjs --out ../.design/probes --report       # rebuild report.md
```

Probes can run in separate processes into one `--out`: each writes `hits-<probe>.json`, and `report.md` reads them all. The
report has one row per rule and kind of element (a row component that fails on every row is one line, with its count),
the measured value, where it happens and a cropped, framed picture of the first one. `findings.json` has every hit,
`inventory/sheet-<kind>.png` the interface inventory (buttons, icon buttons, chips, badges, inputs, menu items and icons,
grouped by height so near-duplicates sit together, plus the colours, type, spacing, radii and shadows painted),
`vision/sheet.png` the deficiency comparison, `keyboard/` the tab-order overlays and `forced/` and `rtl/` pictures. A hit is
a measurement, not yet a finding: the review reads it, groups hits by root cause and files the issue.

## Judging

Each area is reviewed against the pictures and the code. A finding names the surface and device, shows the capture, says
what is wrong and why, and proposes a fix. The rules, in order:

1. **Kipple's own decisions.** `docs/maintainers/ui-decisions.md` and `docs/design.md`. A screen that breaks one is a
   finding. A decision that looks wrong is raised with the owner, not overridden.
2. **One way to do a thing.** The same object (a feed row, an unread count, an article action) looks and behaves the
   same everywhere it appears. Two implementations of one thing are a finding even when both look fine today.
3. **Action order and placement.** The actions used most stay out of overflow menus. More is the last button in a
   toolbar. The same actions come in the same order in the toolbar, the row menu and the Gazette menu.
4. **Reading first.** Text is legible in every theme and at every text size, nothing is clipped or overlapped, long and
   short titles both look intended, and pictures of any shape are framed well.
5. **Usability heuristics.** Visibility of state, match with the reader's words, user control (undo), consistency,
   error prevention, recognition over recall, minimal clutter, helpful empty and error states.
6. **Wording.** Plain words that say what a control does. User-facing text describes the product as it is.

### Beyond the usual checks

The review deliberately goes past what the UAT flows and the contrast check cover. Each of these is a lens on the same
surfaces, and each finding names the lens that found it.

- **Conditions the app rarely sees in testing:** 200% and 400% zoom (reflow at 320 CSS px), a 280 px wide screen,
  landscape phone, the text-spacing override of WCAG 1.4.12, forced colors (Windows high contrast), reduced motion,
  slow network and CPU (skeleton flashes, layout shift), right-to-left text and long words.
- **Perception tests:** every color-vision deficiency Chromium emulates, grayscale (is anything said by color alone),
  the squint or blur test (does the hierarchy survive), and a 5-second test of each main screen.
- **Ergonomics:** thumb reach on a phone (what the main actions sit in), tap counts for the main tasks (the
  keystroke-level model), Fitts's law for small or distant targets, and controls that only appear on hover.
- **Inventories:** every button, badge, menu, empty state, icon (size and stroke), color and spacing value that is
  actually rendered, side by side, so near-duplicates show (an interface inventory).
- **The accessibility tree as copy:** what a screen reader reads, in what order, under what names, reviewed as wording.
- **Reading quality:** line length, leading and hierarchy of the article view, image framing and dimming in dark
  themes, and how unread state reads in each layout, judged against published typography and reading research.

Severity uses the issue priorities: **P1** a normal session hits it and it misleads (wrong count, hidden action),
**P2** clearly off on a common screen, **P3** polish, **P4** nit.

## Phases

| Phase | Work | Output |
|---|---|---|
| 0 | Milestone, label, the Design note issue template, the design library, the capture script, this page | First captures |
| 1 | Automated pass: a code audit for duplicate components, values that bypass the theme tokens, unlabelled icon buttons and menu order; measured checks on live pages (touch targets, contrast in all 20 themes, clipped text and sideways scrolling, layout shift, alignment, visible focus) and the conditions in "Beyond the usual checks" | Issues for the objective findings |
| 2 | Review by area, in parallel, against the captures and the rules above | Ranked findings with captures |
| 3 | Triage with the owner | Rulings; issues on the milestone |
| 4 | Fixes, grouped by area, each PR with before and after captures | Merged fixes |
| 5 | Capture everything again and compare with phase 0 | Sign-off |

The measured checks of phase 1 run on demand, like the captures. They are not a CI gate.

## Sources

The thresholds and the issues cite these. Each link was checked when it was added.

- WCAG 2.2: https://www.w3.org/TR/WCAG22/ and the Understanding pages, for example
  https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html (2.5.8),
  https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html (1.4.11),
  https://www.w3.org/WAI/WCAG22/Understanding/reflow.html (1.4.10),
  https://www.w3.org/WAI/WCAG22/Understanding/text-spacing.html (1.4.12),
  https://www.w3.org/WAI/WCAG22/Understanding/focus-not-obscured-minimum.html (2.4.11)
- Apple Human Interface Guidelines: https://developer.apple.com/design/human-interface-guidelines/accessibility,
  https://developer.apple.com/design/human-interface-guidelines/layout
- Material 3: https://m3.material.io/foundations/layout/applying-layout (breakpoints),
  https://m3.material.io/foundations/interaction/states/state-layers
- Nielsen Norman Group: https://www.nngroup.com/articles/ten-usability-heuristics/,
  https://www.nngroup.com/articles/skeleton-screens/, https://www.nngroup.com/articles/touch-target-size/,
  https://www.nngroup.com/articles/f-shaped-pattern-reading-web-content/
- Laws of UX: https://lawsofux.com/fittss-law/, https://lawsofux.com/hicks-law/, https://lawsofux.com/doherty-threshold/
- Practical Typography: https://practicaltypography.com/line-length.html,
  https://practicaltypography.com/line-spacing.html, https://practicaltypography.com/point-size.html
- Layout shift: https://web.dev/articles/cls. Forced colors:
  https://developer.mozilla.org/en-US/docs/Web/CSS/@media/forced-colors
- Interface inventory: https://bradfrost.com/blog/post/interface-inventory/
