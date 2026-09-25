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
src/layouts/  list layouts behind the ListLayout interface (Magazine now; Cards/Compact/Inbox/Headlines next)
src/screens/  list pane, article pane, feeds, search, settings, login
src/shell/    app shell (tab bar / sidebar, landmarks, live region, toasts)
src/theme/    schemes.json, CSS + boot script generators, picker (see src/theme/README.md)
src/lib/      keyboard, per-device prefs (density, font, text size), formatting, safe HTML
src/test/     Vitest setup and API mocks
```

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

`npm test` runs theme resolution (follow-system pair, custom pair, boot-script parity), the API client (401,
cursor paging, pwa header), the SSE reducer and fallback polling, keyboard rules, and axe on the login, list,
article, settings, feeds and search screens. jsdom has no layout, so `src/test/setup.ts` stubs sizes for the
virtualizer. Colour contrast is checked by `npm run contrast` and `theme.test.ts` (axe cannot in jsdom).
