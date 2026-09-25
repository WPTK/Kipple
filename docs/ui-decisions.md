# UI design meeting: decision log

Round 1, 2026-09-25 (the owner and Claude). Inputs: `docs/research/ui-principles-and-accessibility.md`,
`ui-color-schemes.md` (+ `.json`), `ui-gestures-and-layouts.md`, `design-audit-2026-09-25.md`.
This file records what the owner decided. It is the authority where the research files differ. Items
marked TODO need more work before the frontend is built.

## Accessibility

- WCAG deviations are fine as defaults **provided an option exists to enable the accessible behavior**,
  and enabling any option must never break anything else.
- Implement everything the research file lists for **phase 2 and phase 3**, including read-aloud,
  `prefers-contrast`, reduced motion, text scaling, focus and semantics, live region for new items.
- More **density** options than the three presets (TODO: define; e.g. compact/snug/comfortable/relaxed/airy,
  covering both reading text and list rows).
- Font: add **Atkinson Hyperlegible Next** as a font option, labelled "Easy to read". OpenDyslexic is
  dropped (weak evidence). Font file size is not a concern.
- The proposed six-control Accessibility settings section is approved. Look for further opportunities in it.

## Colors

- the owner loves the eight schemes and their names: **Paper, Linen, Parchment, Fern→(renamed), Cocoa, Graphite,
  Midnight, Signal**. Alternate names are not wanted.
- **Fern is renamed.** Theme: the green pages of a phone book (municipal and government listings) or other
  paper. Candidates: Ledger, Directory, Gazette, Bond. TODO: the owner picks. The green itself should be "a
  little softer" than the proposal, but only a little.
- **Cocoa** is too dark as a background: rework (lighter warm brown).
- Keep **both Paper and Linen**. Add a **gray** theme with a paper-themed name (candidate: Newsprint).
- Add more schemes: some accessibility-friendly (color-blind-safe accents, cream/low-glare, high-contrast
  dark), some purely pretty and legible. Paper-themed names preferred. TODO.
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
- **Row swipe: full swipe with undo. Swipe left = mark read, swipe right = mark unread.** (Star: TODO, likely
  a tap target on the row/article and `s`.)
- **Article swipe: swipe back to the previous screen** (the article list you came from, not always the feed
  list), i.e. drill-in navigation stack. Keep next/previous **buttons** and **j/k**.
- Keys: j/k next/previous, s star, o open original, r refresh, m mark read (plus common conventions, TODO
  keymap).
- First-run **peek animation** teaches swipe reveal.
- Mark-read-on-scroll: keep as a setting, **off by default**.

## Layout

- **Magazine is the default**; compact list and cards also ship. the owner likes the proposed layouts and wants
  other options explored, notably an **email-style ("Inbox") layout** (TODO research).
- Lead images: **proxied and cached** by Kipple (sites dislike hotlinking), with a **bounded cache** that
  cannot grow out of hand (size cap setting with LRU eviction on the data volume). This replaces the
  design's "no disk cache in phase 2".

## design.md audit answers

1. Appearance settings are **per device**.
2. Default sort **newest first** (oldest-first available).
3. Mark above/below: best practice, defined against the list's own order (TODO API).
4. Keymap from the answers above plus common conventions.
5. Image privacy and embeds: open but safe (`Referrer-Policy`, CSP, sandboxed embeds, TODO).
6. **Export backup** in the UI (saves a file wherever the owner wants); for testing, an off-box copy is fine.
7. Rollback cost accepted (restore the pre-migration snapshot).
8. **Keyword mute filters: yes**, and other filtering features in the same family (TODO brainstorm).
9. Pending defaults and search: follow industry best practice (FTS5 stemming with prefix fallback).
10. Offline/PWA scope: read and interact with what is already downloaded; show an "offline" message at
    launch; queue actions and sync when back online.

## Parking lot additions

Design system / static demo site / pull-and-run image; Cloudflare Access JWT; passwordless login; scheduled
auto-night. See memory `parking-lot`.

## Next chunks

1. Round-2 research (colors, layouts incl. Inbox, keymap, gesture spec, filters brainstorm, accessibility spec).
2. Consolidate into a UI spec and update `design.md` (audit contradictions).
3. Backend items these decisions add (image cache, mute filters, mark above/below, oldest-first order,
   per-device settings, backup export, security headers, FTS stemming, health status reconciliation).
4. Frontend build after the owner signs off the UI spec.
