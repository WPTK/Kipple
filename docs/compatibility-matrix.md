# Browser and client compatibility

What Kipple has been checked against, how, and what is left to a person. The test runs are described in
[uat-plan.md](uat-plan.md) (Suite 1); the promise about what stays stable across releases is in
[compatibility.md](compatibility.md).

## Web app

Checked by `npm run uat -- --browsers all` and `npm run uat:keyboard -- --browser <engine>` (in `web/`) against a seeded
instance, headless, on Windows. Each run covers every screen in a light and a dark theme at desktop (1280 px), tablet
(768 px) and phone (390 px) widths: no console errors, no failed API calls, no WCAG 2.2 AA violations (axe-core), no
sideways scroll, no stray `undefined` or `NaN`, and the font choice reachable. The keyboard run checks Tab order, focus
indicators, Escape and focus return in menus and dialogs, and the shortcuts in the `?` overlay.

| Engine | Version tested | Stands for | Screens (Suite 1) | Keyboard run | Notes |
|---|---|---|---|---|---|
| Chromium | 153.0.8010.12 | Chrome, Edge, Brave, Opera | clean | clean | The default run. |
| Firefox | 155.0 | Firefox | clean | clean | No mobile emulation in Firefox, so its tablet and phone runs are touch devices at that width. |
| WebKit | 26.6 | Safari on macOS and iOS | clean | clean | Playwright's build of the engine, not Safari itself. Links are not in the Tab order by default, see below. |

Known engine behavior (not Kipple defects, nothing to fix in the page):

- **Safari and Tab.** Safari leaves links out of the Tab order unless "Press Tab to highlight each item on a webpage"
  is on in Safari's Advanced settings (or you use Option+Tab). Buttons and fields are reached either way. The sidebar
  and list rows are links, so with the default Safari setting use the shortcuts (`j`, `k`, `Enter`, `g` then `i`, `a`,
  `s`, `f`, `,`) or turn that setting on.
- **Firefox and the system color scheme.** Playwright cannot hold an emulated dark mode in Firefox on a page sent with
  `Cross-Origin-Opener-Policy`, which Kipple always sets, so the Firefox runs flip the browser's own preference
  instead. A real Firefox follows the system setting as any browser does.

## Phones

| Device | How it was checked | Result |
|---|---|---|
| iPhone, installed from Safari (Add to Home Screen) | By hand by the maintainer on a beta build: install, relaunch and stay signed in, swipe to mark read and to star or open More, the share sheet, and whether reading time is recorded in the installed app. | Passed. Not repeated by a script: a script cannot swipe or install. |
| Phone widths in the desktop browsers above | Suite 1 at 390 px as a touch device, and the keyboard run's gesture check. | Clean. |

Every swipe or long-press action also has a button or menu, so none is gesture-only: the row's Star button and its
More actions menu (mark read or unread, mark above or below), Back to list on an open article, Refresh all feeds for
pull to refresh, and Move to folder for reordering feeds.

## Sync clients

Kipple speaks the Google Reader API at `/api/greader.php`. A client that speaks the Google Reader API as a Google Reader compatible account works the same way. Sign in with the API password from Settings > Account & Devices. The request sequences the protocol uses are replayed against each release by the contract tests in CI; no client is promised beyond what those tests cover.

## Still checked by a person

- Installing the web app on a phone and using it there (touch swipes, the share sheet, safe-area spacing, staying
  signed in).
- A real Safari with its default keyboard setting, and any other browser or version not listed above.
- Connecting a real Reader API client: add, read, star, and mark all as read from it.
