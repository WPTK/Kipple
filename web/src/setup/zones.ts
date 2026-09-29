// The time zone step's list (design 7a): the browser's own IANA names with each one's current UTC offset, a bundled
// fallback for browsers without Intl.supportedValuesOf, and a search that matches the name and the offset.

/** Used when the browser cannot list its zones. Common zones only; the server stays the validator. */
export const FALLBACK_ZONES: readonly string[] = [
  "UTC",
  "Africa/Cairo", "Africa/Johannesburg", "Africa/Lagos", "Africa/Nairobi", "Africa/Casablanca",
  "America/Anchorage", "America/Argentina/Buenos_Aires", "America/Bogota", "America/Chicago", "America/Denver",
  "America/Halifax", "America/Lima", "America/Los_Angeles", "America/Mexico_City", "America/New_York",
  "America/Phoenix", "America/Santiago", "America/Sao_Paulo", "America/St_Johns", "America/Toronto", "America/Vancouver",
  "Asia/Baghdad", "Asia/Bangkok", "Asia/Dhaka", "Asia/Dubai", "Asia/Hong_Kong", "Asia/Jakarta", "Asia/Jerusalem",
  "Asia/Karachi", "Asia/Kathmandu", "Asia/Kolkata", "Asia/Manila", "Asia/Seoul", "Asia/Shanghai", "Asia/Singapore",
  "Asia/Taipei", "Asia/Tehran", "Asia/Tokyo",
  "Atlantic/Azores", "Atlantic/Reykjavik",
  "Australia/Adelaide", "Australia/Brisbane", "Australia/Darwin", "Australia/Melbourne", "Australia/Perth", "Australia/Sydney",
  "Europe/Amsterdam", "Europe/Athens", "Europe/Berlin", "Europe/Brussels", "Europe/Dublin", "Europe/Helsinki",
  "Europe/Istanbul", "Europe/Kyiv", "Europe/Lisbon", "Europe/London", "Europe/Madrid", "Europe/Moscow", "Europe/Oslo",
  "Europe/Paris", "Europe/Prague", "Europe/Rome", "Europe/Stockholm", "Europe/Vienna", "Europe/Warsaw", "Europe/Zurich",
  "Pacific/Auckland", "Pacific/Fiji", "Pacific/Honolulu",
];

/** The browser's own zone name, or "" when it cannot say. */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

/** Every zone the browser knows (plus UTC, which Intl leaves out), sorted; the bundled list when it cannot list them. */
export function zoneNames(): string[] {
  let names: string[] = [];
  try {
    const f = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    if (typeof f === "function") names = f.call(Intl, "timeZone");
  } catch {
    names = [];
  }
  if (names.length === 0) names = [...FALLBACK_ZONES];
  const set = new Set(names);
  set.add("UTC");
  return [...set].sort((a, b) => (a === "UTC" ? -1 : b === "UTC" ? 1 : a.localeCompare(b)));
}

/** "UTC+09:00" (or "UTC" for zero) for a zone at `at`, or "" when the browser cannot work it out. */
export function offsetLabel(zone: string, at: Date = new Date()): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", { timeZone: zone, timeZoneName: "longOffset" }).formatToParts(at);
    const v = parts.find((p) => p.type === "timeZoneName")?.value ?? "";
    if (v === "GMT" || v === "UTC") return "UTC";
    const m = /^GMT([+-])(\d{1,2})(?::?(\d{2}))?$/.exec(v);
    if (!m) return "";
    const label = `UTC${m[1]}${(m[2] ?? "0").padStart(2, "0")}:${m[3] ?? "00"}`;
    return label === "UTC+00:00" || label === "UTC-00:00" ? "UTC" : label;
  } catch {
    return "";
  }
}

export interface ZoneEntry {
  name: string;
  /** "UTC+09:00". */
  offset: string;
  /** What the list shows: "Asia/Tokyo (UTC+09:00)". */
  label: string;
  /** Lower-case text the search looks in: the name with spaces for underscores and slashes, and the offset written several ways. */
  haystack: string;
}

export function zoneEntries(names: string[] = zoneNames(), at: Date = new Date()): ZoneEntry[] {
  return names.map((name) => {
    const offset = offsetLabel(name, at);
    const label = offset ? `${name.replaceAll("_", " ")} (${offset})` : name.replaceAll("_", " ");
    return { name, offset, label, haystack: `${name} ${name.replace(/[_/]/g, " ")} ${offset} ${offsetAliases(offset)}`.toLowerCase() };
  });
}

/** "UTC+09:30" also as "+9:30", "+09:30", "+0930" and, for a whole hour, "+9" and "+09". */
function offsetAliases(offset: string): string {
  const m = /^UTC([+-])(\d\d):(\d\d)$/.exec(offset);
  if (!m) return offset === "UTC" ? "gmt utc z" : "";
  const [, sign, hh, mm] = m;
  const h = String(Number(hh));
  const out = [`${sign}${h}:${mm}`, `${sign}${hh}:${mm}`, `${sign}${hh}${mm}`, `gmt${sign}${h}`];
  if (mm === "00") out.push(`${sign}${h}`, `${sign}${hh}`);
  return out.join(" ");
}

/** The entries that match every word of `query` (name or offset); an empty query matches all. */
export function searchZones(entries: ZoneEntry[], query: string): ZoneEntry[] {
  const words = query.toLowerCase().replace(/[_/]/g, " ").split(/\s+/).filter(Boolean);
  if (words.length === 0) return entries;
  return entries.filter((e) =>
    words.every((w) => {
      // "+9" must not match every zone that merely has a 9 in its offset: an offset query is compared as a whole word.
      if (/^(utc|gmt)?[+-]\d/.test(w)) {
        const bare = w.replace(/^(utc|gmt)/, "");
        return e.haystack.split(" ").some((t) => t === bare || t === `gmt${bare}` || (bare.length > 2 && t.startsWith(`utc${bare}`)));
      }
      return e.haystack.includes(w);
    }),
  );
}
