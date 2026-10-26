package api

import (
	"testing"
)

// FuzzStatsDelete: any body posted to /api/stats/delete (the one stats endpoint that takes a JSON body, capped
// at 1 MiB) is answered without a server error. Refusals are 4xx; a delete or a dry run is 200.
func FuzzStatsDelete(f *testing.F) {
	for _, s := range []string{
		``, `{}`, `null`, `[]`, `"x"`, `{"all":true}`, `{"all":true,"dry_run":true}`, `{"all":true,"confirm":"DELETE ALL"}`,
		`{"all":true,"from":"2026-01-01"}`, `{"from":"2026-01-01","to":"2026-01-31"}`, `{"from":"2026-01-31","to":"2026-01-01"}`,
		`{"from":"2026-02-30","to":"2026-03-01","dry_run":true}`, `{"from":"0000-01-01","to":"9999-12-31","dry_run":true}`,
		`{"from":"2026-01-01","to":"2026-01-01","extra":1}`, `{"from":1,"to":2}`, `{"all":"yes"}`, `{"from":"2026-01-01"`,
		`{"from":"2026-01-01","to":"2026-01-02"} {}`, "\x00", `{"all":true,"confirm":"delete all"}`,
	} {
		f.Add(s)
	}
	h := newHarness(f)
	c := h.login()
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 1<<16 {
			return
		}
		h.t = t
		rec := h.do("POST", "/api/stats/delete", body, withCookie(c))
		if rec.Code >= 500 {
			t.Fatalf("status %d for body %q: %s", rec.Code, body, rec.Body.String())
		}
	})
}
