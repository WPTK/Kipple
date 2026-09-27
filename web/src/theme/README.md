# Theme system

Source of truth: [`schemes.json`](./schemes.json) (20 schemes, 12 tokens each). Everything else is generated
from it: [`css.ts`](./css.ts) emits one `:root[data-theme="<id>"]` block per scheme as the virtual stylesheet
`virtual:kipple-themes.css`, and the theme boot script. Tailwind maps the tokens in `src/index.css`
(`bg-bg`, `bg-surface`, `text-fg`, `text-fg2`, `border-line`, `text-accent`, `text-link`, `bg-unread`,
`text-star`, `text-danger`, `bg-selection`). Derived in `index.css`: `--kp-toast-bg` and `--kp-hl-bg`; device tokens
(`--kp-scale`, `--kp-reading-font`, `--kp-app-font`, `--kp-col` and the density variables) come from `lib/prefs.ts`. The
`meta` token has no Tailwind utility; it is only used for `<meta name="theme-color">`.

## How it works

- **Per device.** The choice is part of the server's device profile (`ui.theme`, `system` or a scheme id, plus
  `ui.theme_day`, `ui.theme_night`, `ui.theme_schedule`, `ui.theme_night_start` and `ui.theme_day_start`; see
  `lib/deviceSync.ts`). `localStorage` (`kipple.theme.v1`,
  `{mode: "follow" | "fixed", fixed, day, night, schedule, nightStart, dayStart}`) is the instant-paint cache the boot
  script reads. The schedule is a flag under follow, not a third mode, so an older build (in another tab, or a client
  that syncs the profile) passes it through: it reads follow-system and never writes the flag, and a cache it writes
  keeps this tab's schedule fields (missing fields keep the current values).
- **Follow system** resolves `prefers-color-scheme`: default day Paper, night Midnight. The Day and Night
  pickers accept **any** scheme (a dark day theme or a light night theme is allowed).
- **On a schedule** uses the same Day and Night pickers but switches at two times of day ("Night starts", default
  21:00; "Day starts", default 07:00) on the device's own clock, whatever the OS reports. Night runs from its start
  (inclusive) to the day start (exclusive), wrapping past midnight when needed; equal times mean the day theme stays.
  `isNightAt()` holds that rule; `initTheme()` arms a timer for the next switch (at most 15 minutes, re-armed) and
  re-checks when the tab is shown or focused, so the switch is live. Picking a fixed theme turns the schedule off (the
  server does the same for a client that predates it).
- `resolveTheme()` in `settings.ts` is the one pure rule for all three choices (`themeChoice()` names them).
- **No flash.** A blocking classic script in `<head>` (`assets/theme-boot-<hash>.js`, built from
  `bootScript()`) reads the stored choice, resolves it, sets `data-theme` and a single
  `<meta name="theme-color">` before first paint (in dev the script is inlined by the Vite dev server). It is a hashed file rather than an inline script so a strict
  `script-src 'self'` CSP needs no hash. `theme.test.ts` evaluates it against `resolveTheme()`.
- **Live meta theme-color.** `applyTheme()` (in `theme.ts`) updates the same tag on every pick, every
  OS appearance change and every scheduled switch. `index.html` keeps Paper/Midnight media-qualified tags as the first-paint fallback.
- **Picker.** Follow system and On a schedule first (the Day and Night selects, plus the two times on a schedule,
  show under them), then a short featured list (Paper, Linen, Newsprint, Graphite, Midnight),
  the rest under "More themes", and the accessibility group collapsed under its own fold.
  The offered schemes are narrowed to the ids the server lists in the `ui.theme_day` options
  (`theme/serverThemes.ts`), and the fold is labelled "Accessibility themes". The Aa menu has a compact grouped select
  of every theme with "Match my device" and "On a schedule" first. Unread state is always a dot plus a bold title, never color alone.

## Changes from the round-2 research (the owner's decisions)

- Fern is **Directory** (round-2 softer values). Gray is **Newsprint**. Both Cocoas kept: **Cocoa Kraft**
  (light tan) and **Cocoa Mid** (dark brown). All ten round-2 additions kept.
- **Signal danger** is now `#a0006a` (was `#a00000`): star vs danger under deuteranopia goes from delta E 0.6
  to 52.1, and it is 7.77:1 on white.
- **Carbon** is now a neutral cool gray-black (`#121315`, channel spread under 12) with cool sky-blue accents.
  **Fountain** is a clear navy (`#13284f`) with cream text (`#f3ead2`) and warm accents (amber accent, yellow
  star, rose danger). Background delta E between them is 28.2, accent delta E 90.9. Both stay AA (tables below).
- Two contrast fixes the research missed (danger text on the surface color): Cocoa Kraft danger `#8f1d12` to
  `#7d1a10`, Cocoa Mid danger `#ffb3a6` to `#ffc4b8`.

## Verify

`npm run contrast` recomputes everything and exits 1 on a miss: text 4.5:1, accent/unread/star 3:1, the
color-blind-safe schemes and Signal keep all three accent/star/danger pairs at delta E 15 or more for each
simulated deficiency, and Carbon and Fountain stay apart. `npm run contrast -- --markdown` prints the tables
below. The script also requires 4.5:1 for text, text2, link and danger on the surface, for text on the selection color
and for text2 on the selection color (a selected row's meta line, a selected option's hint; issue #48 found Midnight at 4.05:1,
fixed with text2 `#9a9a9a` to `#a6a6a6`, and the check moved Carbon `#a3a8ae` to `#a7acb2` and Lamplight `#b89a72` to
`#bda078`). Graphite (3.59) and Cocoa Mid (4.00) still miss it and need a design call rather than a nudge; they are listed
in `TEXT2_SELECTION_KNOWN_GAPS` (`contrast.ts`), reported as known gaps, and the script fails once one of them passes so the
entry is removed. It also checks text on the toast tint (18% accent into the surface) with accent and danger borders at 3:1, text on the
highlight tint (20% star into the background) with the star underline at 3:1, and for Carbon and Fountain a background
delta E of 25 and an accent delta E of 40. `theme.test.ts` asserts the WCAG, toast, highlight, Signal and
Carbon/Fountain checks; the per-deficiency color-blind check is `npm run contrast` only.

## Final tokens

| Scheme | Kind | Group | bg | surface | text | text2 | border | accent | link | unread | star | danger | selection | meta |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Paper | light | light | #ffffff | #f6f6f4 | #1b1b1a | #5c5c58 | #dcdcd8 | #1a5fb4 | #1a5fb4 | #1a5fb4 | #b25e00 | #b3261e | #cfe0f7 | #ffffff |
| Linen | light | light | #faf8f3 | #f2efe7 | #26241f | #5f5b52 | #e0dbcf | #0f5c8c | #0f5c8c | #0f5c8c | #a85a00 | #b3261e | #d6e6ee | #faf8f3 |
| Newsprint | light | light | #e6e5e1 | #dbdad5 | #1f1f1d | #4d4d49 | #c4c3bd | #2c5d8f | #23558a | #2c5d8f | #8f5500 | #a82a20 | #c5d3e3 | #e6e5e1 |
| Parchment | light | light | #f4ecd8 | #ece2c8 | #4a3b28 | #6b5a42 | #d9cba8 | #8a4b14 | #7a4310 | #8a4b14 | #9a5200 | #a8321f | #e6d4a6 | #f4ecd8 |
| Directory | light | light | #dcead6 | #cfe2c8 | #1f2d1b | #3e5237 | #b8cfae | #1f6b3a | #17603a | #1f6b3a | #8a5a00 | #a52a1f | #b9d9ad | #dcead6 |
| Cocoa Kraft | light | light | #d2b48e | #c6a67e | #2b1c0f | #4f3a25 | #ad8d63 | #6b2e0a | #5e2708 | #6b2e0a | #7a4700 | #7d1a10 | #e6cf9f | #d2b48e |
| Airmail | light | color | #e2ecf5 | #d5e3f0 | #16212e | #435466 | #b8cbde | #1c5490 | #1c5490 | #1c5490 | #8f4a00 | #a82a20 | #c3d8ee | #e2ecf5 |
| Stationery | light | color | #ece8f6 | #e0dbef | #211c2e | #52496a | #c9c1e0 | #5a3d9e | #4f3591 | #5a3d9e | #8a4f00 | #a82a3a | #d3c8ee | #ece8f6 |
| Tissue | light | color | #f9e9e7 | #f1dbd8 | #3a2424 | #6e4a4a | #e3c3bf | #9b2f5e | #8a2856 | #9b2f5e | #8a4f00 | #b3261e | #f0c9cc | #f9e9e7 |
| Graphite | dark | dark | #121212 | #1c1c1e | #e8e8e6 | #a3a3a0 | #2c2c2e | #7cb7ff | #8ec1ff | #7cb7ff | #f2c14e | #ff8a80 | #2b4a6f | #121212 |
| Midnight | dark | dark | #000000 | #0b0b0c | #e0e0e0 | #a6a6a6 | #232325 | #6db0ff | #7db9ff | #6db0ff | #f2c14e | #ff8a80 | #1f3b5c | #000000 |
| Cocoa Mid | dark | dark | #58463a | #665244 | #f6eee3 | #e0d2c0 | #7a6552 | #f0b36a | #ffd0a0 | #f0b36a | #f5c85a | #ffc4b8 | #75604d | #58463a |
| Fountain | dark | dark | #13284f | #1b3565 | #f3ead2 | #c6c9d6 | #2f4a80 | #f2a65a | #ffc888 | #f2a65a | #ffe27a | #ff9aa8 | #2f5296 | #13284f |
| Foolscap | light | accessibility | #f8f0d6 | #efe6c8 | #2d2a24 | #5a5548 | #dcd2b0 | #24598a | #1f5283 | #24598a | #8f5200 | #a52a20 | #e5d9a8 | #f8f0d6 |
| Tracing | light | accessibility | #f7f8fa | #edeff3 | #1a1d21 | #565c66 | #d5d9e0 | #0a5ba8 | #0a5ba8 | #0a5ba8 | #9a4a00 | #a3195b | #cde0f5 | #f7f8fa |
| Signal | light | accessibility | #ffffff | #ffffff | #000000 | #2b2b2b | #000000 | #0033cc | #0033cc | #0033cc | #7a4a00 | #a0006a | #ffe066 | #ffffff |
| Carbon | dark | accessibility | #121315 | #1b1d20 | #e4e6e8 | #a7acb2 | #2b2e32 | #56b4e9 | #74c3f0 | #56b4e9 | #f0b000 | #ff8fc0 | #24405c | #121315 |
| Lamplight | dark | accessibility | #1d140e | #271b13 | #e8cfa6 | #bda078 | #3a2a1e | #e39a4a | #eeaa60 | #e39a4a | #f0c060 | #f09078 | #4a3420 | #1d140e |
| Inkwell | dark | accessibility | #000000 | #000000 | #ffffff | #d9d9d9 | #ffffff | #66b3ff | #66b3ff | #66b3ff | #ffd23f | #ff8080 | #0b3d91 | #000000 |
| Teletype | dark | accessibility | #000000 | #0d0d00 | #ffe600 | #d4c400 | #ffe600 | #4fd8ff | #4fd8ff | #4fd8ff | #ffffff | #ff8a8a | #4a4400 | #000000 |

## WCAG contrast (ratio : 1)

Thresholds: text, text2, link, danger 4.5; accent, unread, star 3 (non-text). All pass except text2/sel in the two known gaps
(Graphite and Cocoa Mid, see Verify).

| Scheme | text | text2 | text2/surf | link | link/surf | danger | accent | star | text/sel | text2/sel |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Paper | 17.24 | 6.72 | 6.21 | 6.29 | 5.81 | 6.54 | 6.29 | 4.67 | 12.85 | 5.01 |
| Linen | 14.61 | 6.37 | 5.89 | 6.75 | 6.24 | 6.16 | 6.75 | 4.79 | 12.12 | 5.29 |
| Newsprint | 13.10 | 6.74 | 6.07 | 6.08 | 5.48 | 5.52 | 5.43 | 4.80 | 10.85 | 5.58 |
| Parchment | 9.16 | 5.63 | 5.14 | 6.76 | 6.17 | 5.68 | 5.76 | 4.98 | 7.36 | 4.53 |
| Directory | 11.58 | 6.80 | 6.23 | 6.06 | 5.55 | 5.69 | 5.21 | 4.74 | 9.38 | 5.51 |
| Cocoa Kraft | 8.36 | 5.43 | 4.66 | 6.02 | 5.17 | 5.26 | 5.28 | 3.91 | 10.81 | 7.02 |
| Airmail | 13.59 | 6.50 | 5.96 | 6.45 | 5.91 | 5.82 | 6.45 | 5.58 | 11.14 | 5.33 |
| Stationery | 13.73 | 6.92 | 6.17 | 7.75 | 6.91 | 5.70 | 6.72 | 5.45 | 10.44 | 5.26 |
| Tissue | 12.24 | 6.51 | 5.79 | 7.08 | 6.30 | 5.55 | 6.02 | 5.58 | 9.56 | 5.08 |
| Graphite | 15.27 | 7.41 | 6.73 | 10.01 | 9.09 | 8.21 | 8.99 | 11.16 | 7.40 | 3.59 |
| Midnight | 15.91 | 8.63 | 8.08 | 10.26 | 9.61 | 9.20 | 9.30 | 12.51 | 8.65 | 4.69 |
| Cocoa Mid | 7.76 | 6.02 | 4.96 | 6.28 | 5.18 | 5.88 | 4.83 | 5.65 | 5.16 | 4.00 |
| Fountain | 12.13 | 8.81 | 7.30 | 9.60 | 7.95 | 7.23 | 7.19 | 11.36 | 6.33 | 4.60 |
| Foolscap | 12.55 | 6.52 | 5.95 | 7.11 | 6.49 | 6.24 | 6.42 | 5.46 | 10.10 | 5.24 |
| Tracing | 15.92 | 6.34 | 5.85 | 6.43 | 5.93 | 6.94 | 6.43 | 5.89 | 12.55 | 5.00 |
| Signal | 21.00 | 14.16 | 14.16 | 8.95 | 8.95 | 7.77 | 8.95 | 7.48 | 16.11 | 10.86 |
| Carbon | 14.86 | 8.13 | 7.39 | 9.57 | 8.69 | 8.80 | 8.06 | 9.66 | 8.55 | 4.68 |
| Lamplight | 12.00 | 7.30 | 6.76 | 9.11 | 8.43 | 7.74 | 7.76 | 10.71 | 7.71 | 4.69 |
| Inkwell | 21.00 | 14.88 | 14.88 | 9.46 | 9.46 | 8.65 | 9.46 | 14.54 | 10.04 | 7.12 |
| Teletype | 16.57 | 11.70 | 10.88 | 12.60 | 11.72 | 9.25 | 12.60 | 21.00 | 7.82 | 5.52 |

## Color-vision-deficiency separation

Worst of the three pairs (accent vs star, accent vs danger, star vs danger), CIE76 delta E after a
Machado 2009 simulation at severity 1.0. 15 or more is counted clear. Tracing, Carbon, Inkwell, Teletype and
Signal are held to that by the script; the others are not claimed color-blind safe (as in the research).

| Scheme | Deutan | Protan | Tritan |
| --- | --- | --- | --- |
| Paper | 14.5 | 25.5 | 25.1 |
| Linen | 11.4 | 23.0 | 27.5 |
| Newsprint | 9.0 | 21.1 | 32.5 |
| Parchment | 2.0 | 7.0 | 6.7 |
| Directory | 9.3 | 11.4 | 37.1 |
| Cocoa Kraft | 1.3 | 6.9 | 10.0 |
| Airmail | 7.5 | 17.4 | 26.2 |
| Stationery | 21.2 | 34.3 | 29.6 |
| Tissue | 4.3 | 16.0 | 17.6 |
| Graphite | 33.3 | 49.5 | 27.7 |
| Midnight | 33.3 | 49.5 | 27.7 |
| Cocoa Mid | 13.5 | 18.3 | 7.2 |
| Fountain | 15.2 | 20.0 | 7.7 |
| Foolscap | 9.6 | 20.7 | 29.6 |
| Tracing | 46.2 | 32.1 | 15.6 |
| Signal | 52.1 | 50.7 | 30.9 |
| Carbon | 37.0 | 16.6 | 15.7 |
| Lamplight | 10.2 | 12.8 | 7.4 |
| Inkwell | 47.3 | 54.7 | 40.5 |
| Teletype | 36.2 | 28.6 | 49.3 |

Carbon vs Fountain delta E: bg 28.2, surface 32.3, text 14.1, accent 90.9
