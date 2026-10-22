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
articles and saves a search. Statistics start empty, because the seed records no reading.

### Surfaces and areas

| Area | Surfaces |
|---|---|
| lists | the six layouts on All unread, a folder, a long feed name, odd picture shapes, Unicode titles |
| states | Starred, Muted, an empty feed, nothing unread, a failing feed, not found |
| article | a lead picture, a long article, Japanese text |
| menus | the article's More actions, a row's More actions, List options |
| navigation | the Feeds screen (on a desktop the sidebar is in every list capture) |
| search | nothing typed, results |
| stats | Statistics, Wrapped |
| settings | Feed health, each settings group |
| setup | the sign-in screen |

The surfaces, devices and sign-in live in `scripts/maintainers/lib/design-surfaces.mjs`, which both the captures and
the measured checks below use. A surface added there is captured and measured.

Not captured yet: the setup wizard (needs `KIPPLE_SEED_SET=fresh`), dialogs (add feed, edit feed, delete), toasts,
offline, drag and drop, and the gestures themselves. The review covers those by driving them, or a later version of the
script adds them.

## Measured checks

`design-probes.mjs` measures the same surfaces against the seeded instance and writes `findings.json`, `report.md`
(findings grouped by root cause, each with a cropped capture, the hit outlined in magenta), filmstrips and vision strips
under `frames/`, the accessibility tree and tab order of each screen under `aria/`, and the interface inventory sheets.

```
node ../scripts/maintainers/design-probes.mjs --out ../.design/probes                     # every check, about an hour
node ../scripts/maintainers/design-probes.mjs --out ../.design/probes --checks targets,keyboard --only '^list-'
```

| Check | What it measures |
|---|---|
| `targets` | Controls under 24 px without the WCAG 2.5.8 spacing, under 44 px on a phone or tablet, neighbours under 8 px apart |
| `contrast` | Text contrast (axe) and non-text contrast (icons in icon-only controls, unread dots, field boundaries, selected states) in every theme (`--contrast-themes`) |
| `reflow` | Sideways scrolling and clipped text at 320x256 (400% zoom), 280 px and two landscape phones |
| `spacing` | Text that newly clips, overlaps or scrolls sideways under the WCAG 1.4.12 override, the largest text size and 200% browser text |
| `forced` | Icons, unread dots, boundaries and selected states under forced colors and `prefers-contrast: more` |
| `vision` | Unread, selected states and errors told by color alone; a strip of the screen under each color-vision deficiency and in grayscale |
| `throttle` | Layout shift and skeletons (flashes, a different shape from the result) on 4x CPU and a slow network, with a filmstrip |
| `stuck` | Opens articles on a phone `--repeat` times and records any that stay on the loading skeleton, with the requests still waiting |
| `keyboard` | Tabs through each screen: focus drawn (pixels change, and by how much), not hidden under a sticky bar, order following the layout |
| `aria` | The accessibility tree: unnamed controls, repeated names, heading levels, reading order, and axe's other rules |
| `stress` | Right-to-left, 100-character names, a long compound word and CJK put into names and titles: new overflow, clipping and overlap |
| `inventory` | Every distinct button, field, menu item, badge and icon style, and every font size, spacing, radius, color and shadow value |

A finding is a measurement, not a verdict: the review decides which are real problems and files one issue per root
cause. Findings never fail the run; a surface a check could not reach is listed under `missed` and the run exits 1.

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
