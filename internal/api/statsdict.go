package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

// The stats data dictionary: the single source of truth for the export envelope's "dictionary" and
// for GET /api/stats/dictionary (JSON and Markdown). Column order here is the CSV column order.

// dictColumn describes one raw export column.
type dictColumn struct {
	Name, Type, Unit, Description string
}

// statsColumns lists the raw columns in export order. event_id is internal and not exported.
var statsColumns = []dictColumn{
	{"id", "integer (string in JSON)", "", "Row id, ascending in recording order. Unique and stable; never reused."},
	{"ts", "integer", "unix seconds (UTC)", "When the event was recorded."},
	{"local_date", "text", "YYYY-MM-DD", "The date of ts in the time zone (setting tz) in force when the row was written. Fixed at write time: changing the time zone later does not rewrite old rows."},
	{"local_hour", "integer", "0-23", "The local hour of ts, same zone as local_date."},
	{"local_weekday", "integer", "0-6", "The local weekday of ts, 0 = Sunday through 6 = Saturday, same zone as local_date."},
	{"kind", "text", "", "What happened: open, read_time, scroll, star, unstar, open_original or share (see kinds)."},
	{"client", "text", "", "Where it came from: web, pwa, reeder, netnewswire, unread or api."},
	{"inferred", "integer", "0 or 1", "1 when the event was inferred from a Reader API request rather than reported by the web app. Excluded from an export unless include_inferred=1."},
	{"item_id", "integer (string in JSON)", "", "The article's id at the time of the event."},
	{"feed_id", "integer (string in JSON)", "", "The feed's id at the time of the event."},
	{"feed_title", "text", "", "The feed's name when the event was written (a snapshot: later renames do not change it). Empty when the feed was already gone."},
	{"folder_id", "integer (string in JSON), may be empty", "", "The feed's folder id when written; empty for a feed in no folder."},
	{"folder_name", "text, may be empty", "", "The folder's name when written; empty for no folder."},
	{"item_title", "text, may be empty", "", "The article's title when written. Blank in CSV and null in JSON and JSONL when the export was made with titles=0 (see the X-Kipple-Titles-Included header and titles_included)."},
	{"item_url", "text, may be empty", "", "The article's URL when written. Blank in CSV and null in JSON and JSONL when the export was made with titles=0."},
	{"value", "integer, may be empty", "seconds for read_time, percent for scroll", "read_time: seconds of active reading in this slice (at most 60 per event; sum by session_key for the total). scroll: deepest scroll position, 0-100. Empty for every other kind."},
	{"session_key", "text, may be empty", "", "Ties the events of one opening of one article together: an open, its read_time slices and its scroll share the key. Empty for star, unstar, open_original and share."},
}

var statsKinds = [][2]string{
	{"open", "The article was opened in the reader. Starts a session (session_key)."},
	{"read_time", "A slice of active reading time in seconds (value) for a session. Active means the tab was visible and focused with recent activity in the article; idle time and list browsing never count."},
	{"scroll", "The deepest scroll position (value, 0-100 percent) reached in a session. One row per session; a deeper scroll raises the same row."},
	{"star", "The article was starred (from the web app or a Reader API client)."},
	{"unstar", "The article was unstarred."},
	{"open_original", "The link to the original article was opened from the reader."},
	{"share", "The article was shared with the system share sheet or copied to the clipboard."},
}

// dictConcept is one named definition.
type dictConcept struct{ Name, Text string }

var statsConcepts = []dictConcept{
	{"what_is_recorded", "One row per event. Marking articles read (one, many, everything, by scrolling or by keyboard) is not an event and is never recorded; only the kinds listed are. With recording off no new rows are written; rows already recorded are kept and can still be exported and deleted."},
	{"read", fmt.Sprintf("An open counts as a read when its session has at least %d seconds of read_time in total, or a scroll value of at least %d together with at least %d seconds of read_time.", store.StatsReadSeconds, store.StatsReadScroll, store.StatsReadScrollSeconds) + " A scroll alone is never a read: a quick flick through an opened article, or paging past it with next and previous, is a bounce. Marking articles read (one, many or everything, by scrolling the list or by keyboard) creates no open, so it is never a read. A session's read_time and scroll count only within the requested range. Legacy opens also count as reads (see legacy open)."},
	{"legacy_open", "An open recorded before the first read_time or scroll row ever written, when the timing feature did not exist yet. Nothing was measured for it, so whether it was really read is unknowable; it is counted as a read, which means totals that include legacy opens can overstate real reading. Legacy opens are left out of tracked_opens and bounce_rate. That first timed row is the earliest ever recorded, remembered even if the rows are later deleted, so deleting old data never changes how kept opens are classified."},
	{"bounce", fmt.Sprintf("A non-legacy open that is not a read: under %d seconds of read_time, and either under %d seconds or a scroll under %d.", store.StatsReadSeconds, store.StatsReadScrollSeconds, store.StatsReadScroll)},
	{"local_time", "local_date, local_hour and local_weekday are computed when the row is written in the time zone named by the tz setting (the export's tz field is the zone in force now). ts is always UTC unix seconds, so it stays exact even if the zone changed."},
	{"range", "A range selects rows by local_date, inclusive on both ends. range=all is every row."},
	{"summary_computation", "The summary is computed from the same rows. It is not stored, so it can be recomputed from a raw export."},
	{"identifiers", "In JSON all ids are strings. Times are unix seconds. CSV and JSONL have no envelope, so their metadata is in response headers: X-Kipple-Rows (the number of records at the start, not lines, since a CSV title can contain line breaks: parse CSV with a CSV parser; JSONL has one record per line; for JSON event_count is authoritative; fewer records means the download was cut off or rows were deleted while it ran), X-Kipple-Rows-Sent (an HTTP trailer with the count actually written, best effort since a proxy may drop it), X-Kipple-Titles-Included (0 or 1), X-Kipple-TZ and X-Kipple-Include-Inferred; a titles=0 filename ends in -no-titles. JSON exports carry titles_included, include_inferred, recording_enabled, tz and, after the events, event_count. CSV text cells that start with = + - @ tab or carriage return are prefixed with a single quote so a spreadsheet does not run them as a formula; remove that leading quote when analysing. A line break inside a CSV text cell is one CRLF: CRLF, LF and a lone CR in the source all become one break."},
}

// dictSummary describes the summary fields (content=summary).
var statsSummaryFields = [][2]string{
	{"enabled", "Whether recording is on now. When false, GET /api/stats/summary returns the rest empty; a content=summary export still summarises the stored rows (see recording_enabled)."},
	{"tz / week_start", "The time zone in force and the first day of the week (sunday or monday)."},
	{"range", "key (week, month, year, all or custom), from and to (inclusive local dates; for all, the first date present through today), days, and last_event_date (the newest local_date of any row, which can be after today when rows were written under another time zone; null with no rows). A summary covers from..to only, so rows dated after today are not in the all summary; a raw all export has no end and includes them."},
	{"first_event_date", "Smallest local_date of any row, or null."},
	{"totals.opens", "Count of open rows."},
	{"totals.items_read", "Distinct items with a read open (see read; legacy opens are included)."},
	{"totals.active_seconds", "Sum of read_time values."},
	{"totals.days_active", "Local dates with at least one read open."},
	{"totals.legacy_opens", "Opens in the range that are legacy opens (see legacy open): counted as reads without any measured reading, so this many opens behind items_read and days_active are unverified."},
	{"daily", "One entry per day of the range, zero-filled: date, items_read, active_seconds."},
	{"streaks", "All time, not limited to the range. current: consecutive days with a read open ending today or yesterday; longest and longest_end: the longest run and its last day."},
	{"heatmap", "Non-zero (weekday 0-6 Sunday first, hour 0-23) cells with active_seconds and opens."},
	{"behavior.busiest_weekday / busiest_hour", "Highest active_seconds, ties by opens; null with no activity."},
	{"behavior.avg_read_seconds", "Mean read_time of read opens that have any."},
	{"behavior.longest_read", "The read open with the most read time (title blank with titles=0)."},
	{"sources", "Per feed with activity: items_read, opens, active_seconds, avg_read_seconds (timed_seconds / timed_items), bounce_rate (bounces / tracked_opens), open_original_rate (items_original / items_opened), stars. At most 300 (sources_truncated)."},
	{"never_opened", "Subscribed feeds with no open in the range, oldest first, at most 500."},
}

// orderedObject marshals to a JSON object with its keys in slice order.
type orderedObject []struct {
	Key string
	Val any
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(kv.Key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(kv.Val)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func (o *orderedObject) add(k string, v any) {
	*o = append(*o, struct {
		Key string
		Val any
	}{k, v})
}

// exportDictionary is the compact column dictionary embedded in every JSON export.
func exportDictionary() orderedObject {
	var o orderedObject
	for _, c := range statsColumns {
		m := map[string]string{"type": c.Type, "description": c.Description}
		if c.Unit != "" {
			m["unit"] = c.Unit
		}
		o.add(c.Name, m)
	}
	return o
}

// fullDictionary is GET /api/stats/dictionary.
func fullDictionary() orderedObject {
	var o orderedObject
	o.add("format", "kipple-stats-dictionary")
	o.add("version", 1)
	o.add("columns", exportDictionary())
	var k orderedObject
	for _, x := range statsKinds {
		k.add(x[0], x[1])
	}
	o.add("kinds", k)
	var c orderedObject
	for _, x := range statsConcepts {
		c.add(x.Name, x.Text)
	}
	o.add("concepts", c)
	var s orderedObject
	for _, x := range statsSummaryFields {
		s.add(x[0], x[1])
	}
	o.add("summary_fields", s)
	return o
}

// dictionaryMarkdown renders the same content as text.
func dictionaryMarkdown() string {
	var b strings.Builder
	b.WriteString("# Kipple reading statistics: data dictionary\n\n")
	b.WriteString("Every raw export row is one event. Columns, in export order:\n\n")
	b.WriteString("| Column | Type | Unit | Description |\n|---|---|---|---|\n")
	esc := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
	for _, c := range statsColumns {
		b.WriteString("| `" + c.Name + "` | " + esc(c.Type) + " | " + esc(c.Unit) + " | " + esc(c.Description) + " |\n")
	}
	b.WriteString("\n## Kinds\n\n")
	for _, x := range statsKinds {
		b.WriteString("- `" + x[0] + "`: " + x[1] + "\n")
	}
	b.WriteString("\n## Definitions\n\n")
	for _, x := range statsConcepts {
		b.WriteString("- **" + strings.ReplaceAll(x.Name, "_", " ") + ".** " + x.Text + "\n")
	}
	b.WriteString("\n## Summary fields\n\n")
	for _, x := range statsSummaryFields {
		b.WriteString("- `" + x[0] + "`: " + x[1] + "\n")
	}
	return b.String()
}
