// A stand-in for GET /api/stats/summary, for the flows that look at the Stats and Your year screens. A seeded library has
// no reading history (statistics are recorded as articles are read, one day at a time), so the screens' charts, heatmap
// and sources would show nothing to look at. Everything else (sign-in, the shell, the feed sheet, the share dialog) is the
// real app on the real server; only the summary is answered here, in the shape the server sends (internal/api/statssummary.go).
//
// `route(page, mode)` ("long" is "rich" with 12h 40m of active time, the longest value a tile shows):
// `route(page, mode)`: "rich" (a month of reading, twelve active days), "empty" (nothing ever read), "quiet" (earlier
// reading, none in the range), "low" (three days of history), "month" (a month of history, whatever the range) or "year"
// (more than a year of reading).

const pad = (n) => String(n).padStart(2, "0");
const ymd = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const parse = (s) => new Date(Number(s.slice(0, 4)), Number(s.slice(5, 7)) - 1, Number(s.slice(8, 10)));
const addDays = (s, n) => {
  const d = parse(s);
  d.setDate(d.getDate() + n);
  return ymd(d);
};
const utc = (s) => Date.UTC(Number(s.slice(0, 4)), Number(s.slice(5, 7)) - 1, Number(s.slice(8, 10)));
const daysBetween = (a, b) => Math.round((utc(b) - utc(a)) / 86_400_000);

export const today = () => ymd(new Date());

const SOURCES = [
  ["1", "Alpha Blog", "10", "Tech", 30, 40, 1800, 60, 0.5, 0.1, 2, { items_read: 10, new_items: 40, rate: 0.25 }],
  ["2", "A feed with a very long title that goes on and on until it has to wrap onto a second line", "10", "Tech", 18, 60, 3000, 300, 0.1, 0.3, 1, null],
  ["3", "Gamma Daily", "11", "News", 44, 20, 0, null, null, null, 5, null],
  ["4", "Delta Notes", null, null, 6, 9, 420, 45, 0.2, 0.05, 0, null],
].map(([id, title, folder_id, folder_name, items_read, opens, active_seconds, avg, bounce, orig, stars, rr]) => ({
  feed_id: id, feed_title: title, folder_id, folder_name, items_read, opens, active_seconds, timed_seconds: active_seconds, timed_items: avg ? items_read : 0,
  avg_read_seconds: avg, bounce_rate: bounce, open_original_rate: orig, tracked_opens: opens, bounces: Math.round(opens * (bounce ?? 0)), items_opened: items_read,
  items_original: Math.round(items_read * (orig ?? 0)), stars, subscribed: id !== "4", ...(rr ? { read_rate: rr } : {}),
}));

function summary(mode, q) {
  const now = today();
  const range = q.get("range");
  const from = q.get("from") ?? (range === "week" ? addDays(now, -6) : range === "year" ? addDays(now, -364) : range === "all" ? addDays(now, -60) : addDays(now, -29));
  const to = q.get("to") ?? now;
  const base = { enabled: true, tz: "UTC", week_start: "sunday" };
  const key = range ?? "all";
  const rangeOut = { key, from, to, days: daysBetween(from, to) + 1 };
  const first = mode === "empty" ? null : mode === "low" ? addDays(now, -3) : mode === "month" ? addDays(now, -30) : mode === "year" ? addDays(now, -400) : addDays(now, -90);
  const quiet = mode === "empty" || mode === "quiet";
  const days = Math.min(400, Math.max(0, daysBetween(from, to) + 1));
  const daily = Array.from({ length: days }, (_, i) => {
    const date = addDays(from, i);
    const live = !quiet && (first == null || date >= first) && (mode === "low" ? i % 1 === 0 : i % 5 !== 0);
    const n = live ? ((i * 7) % 9) + 1 : 0;
    return { date, items_read: n, active_seconds: n * 130 };
  });
  const items = daily.reduce((a, d) => a + d.items_read, 0);
  const secs = daily.reduce((a, d) => a + d.active_seconds, 0);
  const active = daily.filter((d) => d.items_read > 0).length;
  const heat = [];
  if (!quiet) for (let wd = 0; wd < 7; wd++) for (let h = 6; h < 23; h++) if ((wd * 3 + h) % 3 !== 0) heat.push({ weekday: wd, hour: h, active_seconds: ((wd + 1) * (h % 7) + (h === 20 ? 900 : 0)) * 6, opens: 1 + (h % 4) });
  return {
    ...base,
    range: rangeOut,
    first_event_date: first,
    covered_from: first,
    timed_from: first,
    read_rate_from: addDays(now, -30),
    read_rate_to: addDays(now, -1),
    read_rate: { items_read: 10, new_items: 40, rate: 0.25 },
    totals: { items_read: items, opens: quiet ? 0 : items + 8, active_seconds: mode === "long" ? 45_600 : secs, days_active: active, legacy_opens: 0 },
    daily,
    streaks: quiet ? { current: 0, longest: 0, longest_end: null } : { current: 3, longest: 9, longest_end: addDays(now, -20) },
    heatmap: heat,
    behavior: quiet
      ? { busiest_weekday: null, busiest_hour: null, avg_read_seconds: null, longest_read: null }
      : {
          busiest_weekday: { weekday: 2, active_seconds: 4000, opens: 30 },
          busiest_hour: { hour: 20, active_seconds: 3000, opens: 22 },
          avg_read_seconds: 95,
          longest_read: { item_id: "77", title: "A very long essay about the way a small server can keep a large reader, told over many words", feed_title: "Long Reads", seconds: 1320, date: addDays(now, -12) },
        },
    // A young library has one source, and no source can have more reads than the whole library.
    sources: quiet ? [] : mode === "low" ? [{ ...SOURCES[0], items_read: items, items_opened: items, opens: items, tracked_opens: items, active_seconds: secs, timed_seconds: secs, timed_items: items, items_original: 0, bounces: 0, stars: 0 }] : SOURCES,
    never_opened: mode === "empty" ? [] : [
      { feed_id: "8", title: "Quiet Feed", folder_name: "Misc", subscribed_on: addDays(now, -10) },
      { feed_id: "9", title: "Brand New Feed", folder_name: null, subscribed_on: addDays(now, -1) },
    ],
  };
}

/** Answers every summary request of `page` from the stand-in; `mode` can be changed later through the returned setter. */
export async function route(page, mode = "rich") {
  const state = { mode };
  await page.route("**/api/stats/summary*", (r) => {
    const q = new globalThis.URL(r.request().url()).searchParams;
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(summary(state.mode, q)) });
  });
  return (m) => {
    state.mode = m;
  };
}
