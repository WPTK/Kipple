package starter

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The committed feeds.json passes every rule (this test is the CI gate).
func TestEmbeddedListIsValid(t *testing.T) {
	f, err := Load()
	require.NoError(t, err)
	require.NotEmpty(t, f.Categories)
	n := 0
	for _, c := range f.Categories {
		n += len(c.Feeds)
	}
	require.Positive(t, n)
}

// The list is the owner's curated set: this pins its size and shape so an
// accidental edit (a dropped feed, an http URL, a duplicate id) fails here.
// To change the list on purpose, edit starter/feeds.json and update wantFeeds.
func TestEmbeddedListIsThePinnedSet(t *testing.T) {
	const wantFeeds = 10
	wantCategories := []string{"Design & UI", "Art", "Tech", "Automotive", "Books", "Aviation"}
	f, err := Load()
	require.NoError(t, err)
	var cats []string
	ids := map[string]bool{}
	urls := map[string]bool{}
	n := 0
	for _, c := range f.Categories {
		cats = append(cats, c.Title)
		require.NotEmpty(t, c.Feeds, c.ID)
		for _, fd := range c.Feeds {
			n++
			require.False(t, ids[fd.ID], "duplicate id %s", fd.ID)
			ids[fd.ID] = true
			require.False(t, urls[fd.URL], "duplicate url %s", fd.URL)
			urls[fd.URL] = true
			u, err := url.Parse(fd.URL)
			require.NoError(t, err, fd.ID)
			require.Equal(t, "https", u.Scheme, fd.ID)
			require.NotEmpty(t, u.Hostname(), fd.ID)
			require.NoError(t, CheckPublicHTTPS(fd.Site), fd.ID)
			require.True(t, fd.Checked, "%s: every starter feed is ticked by default", fd.ID)
			require.NotContains(t, fd.URL, "example.", fd.ID)
		}
	}
	require.Equal(t, wantFeeds, n)
	require.Equal(t, wantCategories, cats)
}

func doc(feeds ...string) string {
	return `{"version":1,"categories":[{"id":"c","title":"C","feeds":[` + strings.Join(feeds, ",") + `]}]}`
}

func feed(id, u string) string {
	return fmt.Sprintf(`{"id":%q,"title":"T","url":%q,"checked":false}`, id, u)
}

func TestParseRejects(t *testing.T) {
	good := feed("a", "https://news.example.com/feed.xml")
	_, err := Parse([]byte(doc(good)))
	require.NoError(t, err)
	for name, in := range map[string]string{
		"unknown field":     `{"version":1,"categories":[],"extra":1}`,
		"feed unknown":      doc(`{"id":"a","title":"T","url":"https://a.example.com/f","checked":false,"weight":3}`),
		"version":           `{"version":2,"categories":[{"id":"c","title":"C","feeds":[]}]}`,
		"no categories":     `{"version":1,"categories":[]}`,
		"trailing":          doc(good) + `{}`,
		"http":              doc(feed("a", "http://news.example.com/feed.xml")),
		"ip literal":        doc(feed("a", "https://192.0.2.10/feed.xml")),
		"ipv6 literal":      doc(feed("a", "https://[2001:db8::1]/feed.xml")),
		"localhost":         doc(feed("a", "https://localhost/feed.xml")),
		"single label":      doc(feed("a", "https://intranet/feed.xml")),
		".local":            doc(feed("a", "https://nas.local/feed.xml")),
		".lan":              doc(feed("a", "https://nas.lan/feed.xml")),
		".internal":         doc(feed("a", "https://svc.internal/feed.xml")),
		".home.arpa":        doc(feed("a", "https://nas.home.arpa/feed.xml")),
		"userinfo":          doc(feed("a", "https://u:p@news.example.com/feed.xml")),
		"fragment":          doc(feed("a", "https://news.example.com/feed.xml#x")),
		"port":              doc(feed("a", "https://news.example.com:8443/feed.xml")),
		"relative":          doc(feed("a", "/feed.xml")),
		"uppercase host":    doc(feed("a", "https://NEWS.example.com/feed.xml")),
		"bad id":            doc(feed("A", "https://news.example.com/feed.xml")),
		"duplicate id":      doc(good, feed("a", "https://other.example.com/feed.xml")),
		"duplicate url key": doc(good, feed("b", "https://news.example.com/feed.xml")),
		"same url http key": doc(good, feed("b", "https://news.example.com:443/feed.xml")),
		"empty title":       doc(`{"id":"a","title":"","url":"https://a.example.com/f","checked":false}`),
		"control title":     doc(`{"id":"a","title":"a\u0007b","url":"https://a.example.com/f","checked":false}`),
		"long description":  doc(fmt.Sprintf(`{"id":"a","title":"T","url":"https://a.example.com/f","description":%q,"checked":false}`, strings.Repeat("x", 201))),
		"bad lang":          doc(`{"id":"a","title":"T","url":"https://a.example.com/f","checked":false,"lang":"english!"}`),
		"bad site":          doc(`{"id":"a","title":"T","url":"https://a.example.com/f","site":"http://a.example.com/","checked":false}`),
		"dup category":      `{"version":1,"categories":[{"id":"c","title":"C","feeds":[]},{"id":"c","title":"D","feeds":[]}]}`,
		"long category":     `{"version":1,"categories":[{"id":"c","title":"` + strings.Repeat("x", 51) + `","feeds":[]}]}`,
	} {
		_, err := Parse([]byte(in))
		require.Error(t, err, name)
	}
	var many []string
	for i := 0; i <= MaxFeeds; i++ {
		many = append(many, feed(fmt.Sprintf("f%d", i), fmt.Sprintf("https://n%d.example.com/feed.xml", i)))
	}
	_, err = Parse([]byte(doc(many...)))
	require.ErrorContains(t, err, "more than 300")
	_, err = Parse([]byte(doc(many[:MaxFeeds]...)))
	require.NoError(t, err)
}

func FuzzStarterFeeds(f *testing.F) {
	f.Add([]byte(doc(feed("a", "https://news.example.com/feed.xml"))))
	f.Add([]byte(doc(feed("a", "https://192.0.2.1/f"))))
	f.Add([]byte(doc(feed("a", "http://a.example.com/f"))))
	f.Add([]byte(`{"version":1}`))
	f.Add([]byte(feedsJSON))
	f.Fuzz(func(t *testing.T, b []byte) {
		out, err := Parse(b)
		if err != nil {
			return
		}
		// Whatever passes is https on a public DNS name, and round-trips.
		for _, c := range out.Categories {
			for _, fd := range c.Feeds {
				for _, raw := range []string{fd.URL, fd.Site} {
					if raw == "" && raw == fd.Site {
						continue
					}
					u, err := url.Parse(raw)
					require.NoError(t, err)
					require.Equal(t, "https", u.Scheme)
					require.Nil(t, u.User)
					require.Contains(t, u.Hostname(), ".")
					require.NoError(t, CheckPublicHTTPS(raw))
				}
			}
		}
		b2, err := json.Marshal(out)
		require.NoError(t, err)
		_, err = Parse(b2)
		require.NoError(t, err, "a valid list re-validates after a round trip")
	})
}
