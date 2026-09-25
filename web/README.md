# Kipple web

React 19, TypeScript (strict), Vite 8, Tailwind v4, TanStack Query and Virtual, React Router. The build
(`web/dist`) is embedded into the Go binary by `web/embed.go`.

## Stack notes

- **Components:** shadcn-style, hand-written on the scheme tokens, using **Radix** primitives (the `radix-ui`
  package: Collapsible, Dialog). No shadcn CLI, no generated files.
- **Router:** React Router (`react-router`), plain `BrowserRouter`. Article routes are `/i/:id?from=<scope>`;
  opening one is a push, prev/next is a replace, so back is always one step to the list.
- **Data:** TanStack Query (lists are snapshots: `staleTime: Infinity`, patched in place by mutations and SSE),
  TanStack Virtual for the list.
- **TypeScript is 6.0.x**, not 7: `typescript-eslint` supports up to 6.0.
- **Fonts** are self-hosted through `@fontsource` (Literata variable, Atkinson Hyperlegible Next as "Easy to
  read"). The other CLAUDE.md fonts come with the reading-settings step.
- **`internal/web` only serves `/assets/*` and `index.html`.** Anything in `public/` would fall through to
  `index.html`, so there is no `public/`; generated files (theme boot script, fonts) go under `assets/`.

## Layout

```
src/api/      fetch client (X-Kipple-Client, 401), types, TanStack Query hooks, SSE + fallback polling
src/layouts/  the five list layouts behind the ListLayout interface (Magazine, Cards, Compact, Inbox, Headlines)
src/gestures/ row swipe, long press, swipe back, pull to refresh (pointer tracking, no gesture library)
src/screens/  list pane, article pane, feeds, search, settings, login
src/shell/    app shell (tab bar / sidebar, landmarks, live region, toasts)
src/theme/    schemes.json, CSS + boot script generators, picker (see src/theme/README.md)
src/lib/      keyboard, per-device prefs (layout, order, density, font, text size), undo, formatting, safe HTML
src/test/     Vitest setup and API mocks
```

## Lists: layouts, gestures, keys, device prefs, undo

**Layouts.** Five, chosen from the layout menu in the list header: **Magazine** (default; wide screens get a
list plus reader pane), **Cards** (a grid of 1 to 3 columns from the list's own width; no reader pane, an
article opens full width), **Compact** (one dense line per article), **Inbox** (mail-style rows with an
optional trailing thumbnail) and **Headlines** (titles only). The choice is per device, with a per-feed and
per-folder override (feed beats folder beats device default). `c` toggles Compact for the session only.

**Device prefs** (`src/lib/devicePrefs.ts`, `localStorage` key `kipple.device.v1`, behind one storage seam):
layout, overrides, order (newest or oldest first), Inbox thumbnails (auto or off) and whether the first-run
swipe peek has played. Density, font, text size and theme live in `src/lib/prefs.ts` and `src/theme/`.

**Gestures (touch).** Swipe a row right to toggle read or unread; swipe left to star, or open its menu;
long-press (or the More button) for the row menu (read, star, mark above, mark below, open original, copy
link, share). Swipe from the left edge to go back from an article; pull down at the top of a list to refresh.
All of these have a visible button or a key, none is the only way to do something, and `prefers-reduced-motion`
turns the animation off (rows leave instantly).

**Keys** (single keys can be switched off in Settings; `?` always opens the searchable overlay): `j`/`k` next
and previous, `Enter` open, `o` original, `v` background tab, `m` toggle read, `s` star, `x` select, `z` undo,
`f` full text, `r` refresh, `[`/`]` previous or next feed (or folder), `{`/`}` mark above or below, `A` mark
all, `c` Compact, `u` or `Esc` back to the list, `G`/`Home` bottom and top, `/` search, `g` then `i`, `a`,
`s`, `f` or `,` to jump to Unread, All, Starred, Feeds or Settings. Ctrl, Cmd and Alt are never bound.

**Undo.** Every read, unread, star and bulk mark pushes an undo entry. The toast (one slot, merged text such as
"4 articles marked read", 15 s, paused while hovered or focused) has a real Undo button; `z` and the header menu do
the same. Undoing a bulk mark sends the by-id `{ids, read:false}` call. Rows leaving the Unread list collapse over
180 ms and the first still-visible row stays put on screen (`src/lib/collapse.ts`).

**Live updates.** The list is a snapshot (SSE never refetches it). New arrivals show an "n new articles" pill,
counted only for feeds in the current list (a feed, a folder's feeds, or everything; never on Starred, search or an
oldest-first list). If the event stream is closed for good (a proxy 502 during a deploy) the app recreates it with
1 s to 30 s backoff, polls `/api/status` meanwhile, and resyncs when it is back.

**Bulk read API.** `POST /api/items/mark-read` with `{scope:{view, feed_id|folder_id|all:true, q?}, bound?:{order,
side, anchor:{sort_at,id}, inclusive:false}, max_id, read:true, reason}`, answered by `{changed, restored, count,
undoable}` (docs/design.md 7.1). Toasts and announcements go through the shell's persistent live regions.

## Scripts

| Command | What |
|---|---|
| `npm run dev` | Vite on 127.0.0.1:5173, proxying `/api`, `/img`, `/healthz` to 127.0.0.1:7080 |
| `npm run seed` | Build and run a throwaway local Kipple with a few real feeds imported (below) |
| `npm run lint` | ESLint (typescript-eslint, react-hooks) |
| `npm test` | Vitest + Testing Library + axe |
| `npm run build` | `tsc --noEmit` then `vite build` |
| `npm run contrast` | Recompute WCAG and color-blind checks for every theme |

## Local development

Use `127.0.0.1`, never `localhost` (it resolves to `::1` first on this machine and stalls).

**Fast path (two terminals):**

```
cd web
npm install --cache <some-dir>   # on Host-B the shared npm cache throws EPERM; see host-b-dev-gotchas
npm run seed                     # terminal 1: Kipple on 127.0.0.1:7080, feeds fetching in the background
npm run dev                      # terminal 2: http://127.0.0.1:5173
```

Sign in as `dev` with `dev-password-only-for-local-testing`. Those credentials belong to the throwaway data
directory only (`%TEMP%\kipple-dev`, override with `KIPPLE_DEV_DATA`; `npm run seed -- --keep` reuses it).
Needs Go on PATH and network access for the feeds.

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

To try the production bundle through Go: `npm run build`, then `go run ./cmd/kipple serve`. Until
`placeholderApp` in `internal/web/handler.go` is flipped, `/` redirects to `/_status`; open a deep link such as
`/l/unread` to get the app.

## Tests

`npm test` runs the fix and gesture suites (`src/screens/f2a.test.tsx`, `fixes.test.tsx`), theme resolution (follow-system pair, custom pair, boot-script parity), the API client (401,
cursor paging, pwa header), the SSE reducer and fallback polling, keyboard rules, and axe on the login, list,
article, settings, feeds and search screens. jsdom has no layout, so `src/test/setup.ts` stubs sizes for the
virtualizer. Colour contrast is checked by `npm run contrast` and `theme.test.ts` (axe cannot in jsdom).
