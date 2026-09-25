package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/sched"
)

func TestPatchFeedFields(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	folder := h.addFolder("Blogs")
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)
	sub := h.events()

	for _, tc := range []struct {
		name, body, col, want string
	}{
		{"custom title", `{"custom_title":"  Nice  "}`, "custom_title", "Nice"},
		{"blank title clears", `{"custom_title":"   "}`, "custom_title", ""},
		{"null title clears", `{"custom_title":null}`, "custom_title", ""},
		{"folder", `{"folder_id":"` + sid(folder) + `"}`, "folder_id", sid(folder)},
		{"folder as number", `{"folder_id":1}`, "folder_id", "1"},
		{"position", `{"position":7}`, "position", "7"},
		{"interval", `{"interval_minutes":60}`, "interval_minutes", "60"},
		{"interval min", `{"interval_minutes":5}`, "interval_minutes", "5"},
		{"interval max", `{"interval_minutes":10080}`, "interval_minutes", "10080"},
		{"interval inherit", `{"interval_minutes":null}`, "interval_minutes", ""},
		{"retention 500", `{"retention":500}`, "retention", "500"},
		{"retention unlimited", `{"retention":0}`, "retention", "0"},
		{"retention inherit", `{"retention":null}`, "retention", ""},
		{"fulltext on", `{"fulltext":true}`, "fulltext", "1"},
		{"fulltext off", `{"fulltext":false}`, "fulltext", "0"},
		{"user agent", `{"user_agent":" Foo/1 "}`, "user_agent", "Foo/1"},
		{"user agent cleared", `{"user_agent":""}`, "user_agent", ""},
		{"http auth", `{"http_auth":"bob:s3cret"}`, "http_auth", "bob:s3cret"},
		{"http auth cleared", `{"http_auth":null}`, "http_auth", ""},
		{"ignore cache", `{"ignore_http_cache":true}`, "ignore_http_cache", "1"},
		{"disable h2", `{"disable_http2":true}`, "disable_http2", "1"},
		{"insecure tls", `{"allow_insecure_tls":true}`, "allow_insecure_tls", "1"},
		{"private net", `{"allow_private_net":true}`, "allow_private_net", "1"},
		{"private net off", `{"allow_private_net":false}`, "allow_private_net", "0"},
	} {
		code, body, _ := h.api(c, "PATCH", path, tc.body)
		require.Equal(t, 200, code, tc.name+" "+fmt.Sprint(body))
		require.Equal(t, sid(id), body["id"], tc.name)
		got := h.feedRow(id, tc.col)
		if tc.want == "" {
			require.False(t, got.Valid, tc.name)
		} else {
			require.Equal(t, tc.want, got.String, tc.name)
		}
	}

	// the response is the feed detail and never echoes the credentials
	code, body, rec := h.api(c, "PATCH", path, `{"http_auth":"bob:s3cret","custom_title":"Renamed","folder_id":"`+sid(folder)+`"}`)
	require.Equal(t, 200, code)
	require.Equal(t, true, body["has_http_auth"])
	require.NotContains(t, rec.Body.String(), "s3cret")
	require.Equal(t, "Renamed", body["title"])
	require.Equal(t, sid(folder), body["folder_id"])

	// renames, moves: feed.changed
	require.NotEmpty(t, feedChanged(t, sub))
	// an empty patch is a no-op that still returns the feed
	code, _, _ = h.api(c, "PATCH", path, `{}`)
	require.Equal(t, 200, code)
}

func TestPatchFeedValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)
	before := h.feedRow(id, "updated_at")
	for _, tc := range []struct {
		name, body, kind string
	}{
		{"unknown field", `{"nope":1}`, "unknown_field"},
		{"id is not patchable", `{"id":"9"}`, "unknown_field"},
		{"title of a known field, one unknown", `{"custom_title":"ok","nope":1}`, "unknown_field"},
		{"not json", `{`, "bad_request"},
		{"empty body", ``, "bad_request"},
		{"array", `[]`, "bad_request"},
		{"title not a string", `{"custom_title":1}`, "bad_request"},
		{"title too long", `{"custom_title":"` + strings.Repeat("é", 201) + `"}`, "bad_request"},
		{"title control char", `{"custom_title":"a\nb"}`, "bad_request"},
		{"folder null", `{"folder_id":null}`, "bad_request"},
		{"folder zero", `{"folder_id":0}`, "bad_request"},
		{"folder junk", `{"folder_id":"abc"}`, "bad_request"},
		{"folder missing", `{"folder_id":"999"}`, "folder_not_found"},
		{"position negative", `{"position":-1}`, "bad_request"},
		{"position float", `{"position":1.5}`, "bad_request"},
		{"position huge", `{"position":99999999}`, "bad_request"},
		{"position string", `{"position":"3"}`, "bad_request"},
		{"interval too small", `{"interval_minutes":4}`, "bad_request"},
		{"interval too big", `{"interval_minutes":10081}`, "bad_request"},
		{"interval string", `{"interval_minutes":"30"}`, "bad_request"},
		{"retention 7", `{"retention":7}`, "bad_request"},
		{"retention negative", `{"retention":-1}`, "bad_request"},
		{"retention string", `{"retention":"250"}`, "bad_request"},
		{"fulltext number", `{"fulltext":1}`, "bad_request"},
		{"fulltext null", `{"fulltext":null}`, "bad_request"},
		{"enabled string", `{"enabled":"yes"}`, "bad_request"},
		{"enabled null", `{"enabled":null}`, "bad_request"},
		{"dedup junk", `{"dedup_mode":"weird"}`, "bad_request"},
		{"dedup null", `{"dedup_mode":null}`, "bad_request"},
		{"ua too long", `{"user_agent":"` + strings.Repeat("x", 501) + `"}`, "bad_request"},
		{"ua control", `{"user_agent":"a\r\nX-Evil: 1"}`, "bad_request"},
		{"auth without colon", `{"http_auth":"bob"}`, "bad_request"},
		{"auth newline", `{"http_auth":"a:b\nc"}`, "bad_request"},
		{"flag as string", `{"disable_http2":"true"}`, "bad_request"},
		{"url null", `{"url":null}`, "bad_request"},
		{"url blank", `{"url":" "}`, "bad_request"},
		{"url not a string", `{"url":3}`, "bad_request"},
		{"url ftp", `{"url":"ftp://a.example/f"}`, "invalid_url"},
		{"url loopback", `{"url":"http://127.0.0.1/f"}`, "invalid_url"},
		{"url private", `{"url":"http://10.20.30.5/f"}`, "invalid_url"},
		{"url metadata", `{"url":"http://169.254.169.254/x"}`, "invalid_url"},
		{"url ipv6 loopback", `{"url":"http://[::1]:9000/x"}`, "invalid_url"},
		{"url javascript", `{"url":"javascript:alert(1)"}`, "invalid_url"},
	} {
		code, body, _ := h.api(c, "PATCH", path, tc.body)
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, tc.kind, body["error"], tc.name)
	}
	require.Equal(t, before, h.feedRow(id, "updated_at"), "a rejected patch changes nothing")
	require.Equal(t, "https://a.example/feed", h.feedRow(id, "url").String)

	// a valid field beside an invalid one must not be applied
	code, _, _ := h.api(c, "PATCH", path, `{"custom_title":"Applied?","retention":7}`)
	require.Equal(t, 400, code)
	require.False(t, h.feedRow(id, "custom_title").Valid)

	// not found, bad ids, archive
	for _, p := range []string{"/api/feeds/999", "/api/feeds/abc", "/api/feeds/0", "/api/feeds/-1"} {
		code, _, _ := h.api(c, "PATCH", p, `{"fulltext":true}`)
		require.Equal(t, 404, code, p)
	}
	h.exec(`INSERT INTO feeds (folder_id, url, url_key, host, enabled, disabled_reason, retention) VALUES (1,'kipple:archive','kipple:archive','kipple.invalid',0,'archive',0)`)
	arch := h.feedByURL2("kipple:archive")
	code, body, _ := h.api(c, "PATCH", "/api/feeds/"+sid(arch), `{"custom_title":"x"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "archive_feed", body["error"])
}

func (h *harness) feedByURL2(key string) int64 {
	h.t.Helper()
	var id int64
	require.NoError(h.t, h.db.Reader().QueryRow("SELECT id FROM feeds WHERE url_key = ?", key).Scan(&id))
	return id
}

func TestPatchFeedRetentionEnqueuesTrim(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)

	code, _, _ := h.api(c, "PATCH", path, `{"retention":100}`)
	require.Equal(t, 200, code)
	require.Equal(t, []sched.Priority{{FeedID: id, Kind: sched.PriorityTrim}}, h.sched.submitted())

	// same value again, or other fields only: no new job
	h.api(c, "PATCH", path, `{"retention":100}`)
	h.api(c, "PATCH", path, `{"fulltext":true}`)
	require.Len(t, h.sched.submitted(), 1)

	// back to inherit is a change
	h.api(c, "PATCH", path, `{"retention":null}`)
	require.Len(t, h.sched.submitted(), 2)

	// a scheduler that is shutting down does not fail the PATCH
	h.sched.submitErr = sched.ErrStopped
	code, _, _ = h.api(c, "PATCH", path, `{"retention":50}`)
	require.Equal(t, 200, code)
	require.Equal(t, "50", h.feedRow(id, "retention").String)
}

func TestPatchFeedDedupModeSetsRekey(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)

	h.api(c, "PATCH", path, `{"dedup_mode":"auto"}`) // unchanged
	require.Equal(t, "0", h.feedRow(id, "rekey_pending").String)
	code, body, _ := h.api(c, "PATCH", path, `{"dedup_mode":"link_title"}`)
	require.Equal(t, 200, code)
	require.Equal(t, "link_title", h.feedRow(id, "dedup_mode").String)
	require.Equal(t, "1", h.feedRow(id, "rekey_pending").String)
	require.Equal(t, true, body["rekey_pending"])
}

func TestPatchFeedEnableDisable(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)
	sub := h.events()

	code, body, _ := h.api(c, "PATCH", path, `{"enabled":false}`)
	require.Equal(t, 200, code)
	require.Equal(t, false, body["enabled"])
	require.Equal(t, "user", h.feedRow(id, "disabled_reason").String)
	require.Equal(t, "user", body["status"])
	require.Len(t, feedChanged(t, sub), 1)

	// a gone feed with a failure history, enabled again
	h.exec("UPDATE feeds SET disabled_reason = 'gone', consecutive_failures = 9, current_delay_s = 3600, next_fetch_at = 4102444800 WHERE id = ?", id)
	before := h.sched.wakes
	code, body, _ = h.api(c, "PATCH", path, `{"enabled":true}`)
	require.Equal(t, 200, code)
	require.Equal(t, true, body["enabled"])
	require.False(t, h.feedRow(id, "disabled_reason").Valid)
	require.Equal(t, "0", h.feedRow(id, "consecutive_failures").String)
	require.Equal(t, "0", h.feedRow(id, "current_delay_s").String)
	require.Equal(t, fmt.Sprint(h.clk.Now().Unix()), h.feedRow(id, "next_fetch_at").String)
	require.Equal(t, before+1, h.sched.wakes, "the scheduler is nudged")
	require.Len(t, feedChanged(t, sub), 1)

	// disabling an already disabled feed keeps its reason
	h.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'gone' WHERE id = ?", id)
	h.api(c, "PATCH", path, `{"enabled":false}`)
	require.Equal(t, "gone", h.feedRow(id, "disabled_reason").String)
}

func TestPatchFeedURLChange(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("http://a.example/feed")
	other := h.storeFeed("https://taken.example/rss")
	moved := h.storeFeed("https://new.example/feed")
	h.exec("UPDATE feeds SET url_original = 'http://was.example/old', url_original_key = 'was.example/old' WHERE id = ?", moved)
	h.exec(`UPDATE feeds SET etag = 'e', last_modified = 'lm', body_hash = 'bh', ttl_hint_s = 60, consecutive_failures = 4,
		current_delay_s = 900, next_fetch_at = 4102444800, redirect_to = 'https://r.example/x', redirect_kind = 'temporary', redirect_count = 2 WHERE id = ?`, id)
	path := "/api/feeds/" + sid(id)
	sub := h.events()

	// collisions: url_key, other scheme, url_original_key of another feed
	for _, u := range []string{"https://taken.example/rss", "http://TAKEN.example/rss#frag", "http://was.example/old"} {
		code, body, _ := h.api(c, "PATCH", path, jsonStr(map[string]any{"url": u}))
		require.Equal(t, 409, code, u)
		require.Equal(t, "url_exists", body["error"], u)
		if u == "http://was.example/old" {
			require.Equal(t, sid(moved), body["feed_id"])
		} else {
			require.Equal(t, sid(other), body["feed_id"])
		}
	}
	require.Equal(t, "http://a.example/feed", h.feedRow(id, "url").String, "a collision changes nothing")
	require.Equal(t, "e", h.feedRow(id, "etag").String)
	require.Empty(t, feedChanged(t, sub))

	wakes := h.sched.wakes
	code, body, _ := h.api(c, "PATCH", path, `{"url":" https://B.example:443/atom?x=1#top "}`)
	require.Equal(t, 200, code, body)
	require.Equal(t, "https://b.example/atom?x=1", body["url"])
	require.Equal(t, "http://a.example/feed", body["url_original"])
	require.Equal(t, "b.example/atom?x=1", h.feedRow(id, "url_key").String)
	require.Equal(t, "b.example", h.feedRow(id, "host").String)
	require.Equal(t, "a.example/feed", h.feedRow(id, "url_original_key").String)
	for _, col := range []string{"etag", "last_modified", "body_hash", "ttl_hint_s", "redirect_to", "redirect_kind"} {
		require.False(t, h.feedRow(id, col).Valid, col)
	}
	require.Equal(t, "0", h.feedRow(id, "redirect_count").String)
	require.Equal(t, "0", h.feedRow(id, "consecutive_failures").String)
	require.Equal(t, "0", h.feedRow(id, "current_delay_s").String)
	require.Equal(t, fmt.Sprint(h.clk.Now().Unix()), h.feedRow(id, "next_fetch_at").String)
	require.Equal(t, wakes+1, h.sched.wakes)
	require.Len(t, feedChanged(t, sub), 1)

	// the first URL stays the original through later edits; the old URLs still resolve
	h.api(c, "PATCH", path, `{"url":"https://c.example/feed"}`)
	require.Equal(t, "http://a.example/feed", h.feedRow(id, "url_original").String)
	require.Equal(t, id, h.feedByURL("http://a.example/feed"))

	// setting the very same URL is a no-op (no reset, no wake)
	h.exec("UPDATE feeds SET etag = 'keep' WHERE id = ?", id)
	wakes = h.sched.wakes
	h.api(c, "PATCH", path, `{"url":"https://c.example/feed"}`)
	require.Equal(t, "keep", h.feedRow(id, "etag").String)
	require.Equal(t, wakes, h.sched.wakes)

	// going back to a URL it used to have is not a collision with itself
	code, _, _ = h.api(c, "PATCH", path, `{"url":"http://a.example/feed"}`)
	require.Equal(t, 200, code)
}

func TestPatchFeedURLPrivateNetAllowance(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)

	code, _, _ := h.api(c, "PATCH", path, `{"url":"http://10.20.30.10/feed"}`)
	require.Equal(t, 400, code, "a literal LAN address is refused while the feed is not allowed on private nets")
	// allowing private nets in the same request makes the literal acceptable
	code, body, _ := h.api(c, "PATCH", path, `{"allow_private_net":true,"url":"http://10.20.30.10/feed"}`)
	require.Equal(t, 200, code, body)
	require.Equal(t, "http://10.20.30.10/feed", h.feedRow(id, "url").String)
	// and loosening is per feed: another feed still refuses it
	id2 := h.storeFeed("https://b.example/feed")
	code, _, _ = h.api(c, "PATCH", "/api/feeds/"+sid(id2), `{"url":"http://10.20.30.11/feed"}`)
	require.Equal(t, 400, code)
	// it is still checked for scheme and host
	code, _, _ = h.api(c, "PATCH", path, `{"url":"ftp://10.20.30.10/feed"}`)
	require.Equal(t, 400, code)
}

func TestPatchFeedFolderMoveRoundTripThroughBootstrap(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	folder := h.addFolder("Blogs")
	id := h.storeFeed("https://a.example/feed")
	code, _, _ := h.api(c, "PATCH", "/api/feeds/"+sid(id), `{"folder_id":"`+sid(folder)+`"}`)
	require.Equal(t, http.StatusOK, code)
	_, boot, _ := h.api(c, "GET", "/api/bootstrap", "")
	feeds := boot["feeds"].([]any)
	require.Len(t, feeds, 1)
	require.Equal(t, sid(folder), feeds[0].(map[string]any)["folder_id"])
}
