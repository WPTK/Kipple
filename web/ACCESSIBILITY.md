# Accessibility checklist (web)

Source: `docs/research/ui-principles-and-accessibility.md` (phases 2 and 3) and `docs/ui-decisions.md`.
Status is what the code does today. Anything not verified on a real device is listed at the end.

## Structure and focus

- [x] Landmarks: `nav` (tab bar or sidebar), `main`, one `h1` per screen, articles as `<article>`, real buttons and links. axe runs on login, list, article, settings, feeds, add-feed, feed editor and health.
- [x] Skip link; focus moves to the screen heading on navigation and to the article title on open; Radix dialogs and popovers trap and restore focus.
- [x] Visible focus ring (2 px, 3 px in high-contrast and in Signal, Inkwell and Teletype; system `Highlight` in forced colors) on everything via `:focus-visible`.
- [x] Live regions: one polite region for announcements ("n new articles", "Reading aloud", "Saved"), never per item; toasts pause on hover and focus, info toasts last 8 s and errors stay until dismissed. Toast colors are checked in every theme by `npm run contrast`.
- [x] Form errors are `role="alert"` next to the field and linked with `aria-describedby`.

## Filters, devices, muted and highlights

- [x] The filter editor is a labelled dialog (focus trapped and restored); every control has a visible label, help linked with `aria-describedby`, and a server or limit error shown next to its field as `role="alert"`. Terms are a list of chips, each with a "Remove <term>" button; Enter adds.
- [x] The live preview count is a polite live region that changes only after the 600 ms debounce; the footer copy of it is `aria-hidden` so it is not read twice. Warnings are status notices.
- [x] Apply progress is a `progressbar` (value, max, text); its visible counts are hidden from assistive technology and a polite status changes only every quarter, so it is not read twice a second. The finish is announced once ("Filter applied: n articles changed").
- [x] Filter and device rows are named by their rule or device (the switch is the name; Edit, Delete, Copy and Forget carry it in their labels). Replacing and destructive actions are confirm dialogs; deleting a filter that muted articles asks what should happen to them and defaults to the safe choice.
- [x] "Couldn't save your settings" is announced politely and has a Retry button.
- [x] Muted: each row says "Muted by <rule>" in text, with Restore and Edit rule as real buttons (also in the row menu and on the article); Restore is announced.
- [x] Highlights use the semantic `<mark>` plus a solid underline, so they are never color alone; text on the tint is 4.5:1 and the underline 3:1 in every theme (`npm run contrast`); forced colors use `Mark`. The setting can be turned off in the Aa menu.

## Color and contrast

- [x] Every theme checked by `npm run contrast` (text 4.5:1, UI 3:1, color-blind separation).
- [x] Never color alone: unread = dot plus bold title; read = normal weight and dimmed; starred = filled star plus `aria-pressed`; feed status = icon plus text label; errors = icon plus text; the paragraph being read aloud has a start bar as well as a background.
- [x] `prefers-contrast: more`: full-strength borders and secondary text, thicker focus ring, underlined links.
- [x] `forced-colors: active`: system colors for focus, checked controls and the unread dot; nothing meaningful sits in a background image.

## Text and spacing

- [x] All text in `rem`; in-app text size (5 steps) scales `html` and follows browser zoom; pinch zoom is never disabled.
- [x] Text spacing (Less, Default, More: "Adds extra space between letters, words and lines") and Density steps; no fixed-height text boxes, so WCAG 1.4.12 user overrides (line height 1.5, paragraph spacing 2x, letter 0.12em, word 0.16em) do not clip.
- [x] One font choice (the Aa menu) for the whole app except Settings and menus: Atkinson Hyperlegible Next ("Easy to read") and 16 more, applied at once.
- [x] Layouts reflow down to 320 CSS px; the article measure is capped at 46rem.

## Motion

- [x] Reduce motion: Follow system, On or Off. The in-app choice wins over the OS in CSS (`data-motion`) and in JS (`prefersReducedMotion()` in `lib/prefs.ts`, used by swipe, back-swipe, row collapse and smooth scroll).

## Pointer and keyboard

- [x] 44 px minimum targets; "Larger buttons" raises them to 56 px.
- [x] Every swipe has a button and a key; long-press is never required; no drag-only interaction (folders, feeds and favorites reorder by dragging OR with the grip's arrow keys OR with Move up and Move down buttons; bulk move and delete use checkboxes). The sidebar and list width handles are keyboard-operable separators (arrows, Shift, Home, End).
- [x] Segmented controls are native radio groups drawn as pressed buttons; a focused radio never shows its native circle, and the control keeps its place when the page above reflows.
- [x] Undo toast (15 s) instead of confirm dialogs, except deleting a feed, which confirms and shows the starred count.
- [x] Full keyboard map with a searchable `?` overlay; single-key shortcuts can be switched off (WCAG 2.1.4) and default to off on a touch-first device.
- [x] Mark as read while scrolling is off by default and never counts as a read for stats.

## Cognitive

- [x] Plain language: sentence case, no "please", no exclamation points; errors say what happened and what to do.
- [x] "Titles only in lists" (Accessibility) and the Email - Compact layout reduce clutter; per-feed and per-folder layout overrides.
- [x] Predictable navigation; new items arrive behind a pill and never reorder under you.

## Read aloud

- [x] Web Speech API with paragraph chunking (long paragraphs split by sentence), the current paragraph highlighted, play, pause, stop, previous and next paragraph, speed 0.8x to 1.5x and a device voice picker. Hidden when the browser has no speech synthesis or "Listen to articles" is off.
- [x] iOS caveats are shown in the Listen bar: reading stops if the screen locks or the app closes; better voices are in Settings, Accessibility, Spoken Content, Voices; pause is best effort (resume restarts the current paragraph if the engine ignored it); no word highlighting.

## Not verified here

- VoiceOver, Larger Text and Increase Contrast on a real iPhone; Windows High Contrast; a real speech engine (jsdom has none, so the narrator is tested against a fake).
- Mark-as-read-while-scrolling has no jsdom test (the virtualizer has no layout there); it was not exercised in the browser pane either.
