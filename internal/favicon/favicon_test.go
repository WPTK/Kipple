package favicon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func pngOf(t testing.TB, side int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

// icoOf wraps a PNG in a one-entry ICO file.
func icoOf(t testing.TB, side int) []byte {
	t.Helper()
	p := pngOf(t, side)
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, 1})
	b.Write([]byte{byte(side), byte(side), 0, 0})
	_ = binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
	_ = binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(p)), 22})
	b.Write(p)
	return b.Bytes()
}

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><script>alert(1)</script></svg>`

// allowPrivate is the guarded transport with the per-feed private-network
// opt-in on, which httptest's loopback servers need.
var allowPrivate = fetch.NewClient(fetch.ClientOptions{}).Transport(true, false, false)

func lookup(t *testing.T, site string) (Icon, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return Lookup(ctx, Request{SiteURL: site, Transport: allowPrivate, UserAgent: "kipple-test"})
}

func TestLookupPicksTheBestLinkAndFollowsRedirects(t *testing.T) {
	i16, i32, apple := pngOf(t, 16), pngOf(t, 32), pngOf(t, 180)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/blog/home", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/blog/home", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head>
			<link rel="stylesheet" href="/s.css">
			<link rel="icon" type="image/svg+xml" href="/logo.svg">
			<link rel="mask-icon" href="/mask.svg">
			<link rel="shortcut icon" href="/i16.png" sizes="16x16">
			<link rel="apple-touch-icon" href="/apple.png">
			<link rel="icon" href="icons/i32.png" sizes="32x32">
			</head><body><link rel="icon" href="/late.png" sizes="64x64"></body></html>`))
	})
	mux.HandleFunc("/blog/icons/i32.png", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/real32.png", http.StatusFound)
	})
	serve := func(b []byte, ct string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write(b)
		}
	}
	mux.HandleFunc("/real32.png", serve(i32, "application/octet-stream")) // the label is ignored
	mux.HandleFunc("/i16.png", serve(i16, "image/png"))
	mux.HandleFunc("/apple.png", serve(apple, "image/png"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	icon, err := lookup(t, srv.URL+"/")
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/real32.png", icon.SourceURL)
	require.Equal(t, "image/png", icon.ContentType)
	require.Equal(t, i32, icon.Data)
	require.Len(t, icon.Hash, 16)
}

func TestLookupFallsBackToFaviconICO(t *testing.T) {
	ico := icoOf(t, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><head><title>x</title></head><body></body></html>`))
		case "/favicon.ico":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(ico)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	icon, err := lookup(t, srv.URL)
	require.NoError(t, err)
	require.Equal(t, "image/x-icon", icon.ContentType)
	require.Equal(t, srv.URL+"/favicon.ico", icon.SourceURL)
}

// A home page that fails is not the end: /favicon.ico is still tried.
func TestLookupTriesFaviconWhenThePageFails(t *testing.T) {
	ico := icoOf(t, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write(ico)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	icon, err := lookup(t, srv.URL+"/deep/page")
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/favicon.ico", icon.SourceURL)
}

func TestLookupRejectsOversizeWrongTypeAndRedirectLoops(t *testing.T) {
	big := append(pngOf(t, 32), make([]byte, maxIconBytes)...)
	fallback := pngOf(t, 48)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<head>
				<link rel=icon sizes=64x64 href=/big.png>
				<link rel=icon sizes=48x48 href=/fake.png>
				<link rel=icon sizes=40x40 href=/loop>
				<link rel=icon sizes=16x16 href=/never.png>`))
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(big)
		case "/fake.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(svg))
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/favicon.ico":
			_, _ = w.Write(fallback)
		case "/never.png":
			hits.Add(1)
			_, _ = w.Write(fallback)
		}
	}))
	t.Cleanup(srv.Close)
	icon, err := lookup(t, srv.URL)
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/favicon.ico", icon.SourceURL, "every link failed; the fallback was kept")
	require.Equal(t, fallback, icon.Data)
	require.Zero(t, hits.Load(), "at most maxTries fetches per lookup")
}

func TestLookupFailsWhenNothingIsUsable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(svg))
			return
		}
		_, _ = w.Write([]byte(`<link rel=icon href=/page.png>`)) // every path answers this HTML
	}))
	t.Cleanup(srv.Close)
	_, err := lookup(t, srv.URL)
	require.ErrorContains(t, err, "favicon.ico")
	require.ErrorIs(t, err, errNotIcon)
}

func TestLookupOversizeByContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.Header().Set("Content-Length", "999999")
			_, _ = w.Write(make([]byte, 1024))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	_, err := lookup(t, srv.URL)
	require.ErrorIs(t, err, ErrTooLarge)
}

// Without the per-feed opt-in the guard refuses the loopback address at dial
// time: nothing reaches the server, not even the /favicon.ico fallback.
func TestLookupBlocksPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(pngOf(t, 32))
	}))
	t.Cleanup(srv.Close)
	guarded := fetch.NewClient(fetch.ClientOptions{}).Transport(false, false, false)
	_, err := Lookup(context.Background(), Request{SiteURL: srv.URL, Transport: guarded, UserAgent: "x"})
	var be *fetch.BlockedError
	require.True(t, errors.As(err, &be), "got %v", err)
	require.Zero(t, hits.Load())
}

func TestLookupRetriesARefusedUserAgent(t *testing.T) {
	ico := icoOf(t, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "browser" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write(ico)
		}
	}))
	t.Cleanup(srv.Close)
	icon, err := Lookup(context.Background(), Request{SiteURL: srv.URL, Transport: allowPrivate, UserAgent: "kipple", RetryUA: "browser"})
	require.NoError(t, err)
	require.Equal(t, "image/x-icon", icon.ContentType)
}

func TestPageURL(t *testing.T) {
	for _, c := range []struct{ site, feed, want string }{
		{"https://blog.example.com/about#x", "https://feeds.example.net/rss", "https://blog.example.com/about"},
		{"", "https://feeds.example.net/a/rss?x=1", "https://feeds.example.net/"},
		{"javascript:alert(1)", "http://example.com:8080/feed", "http://example.com:8080/"},
		{"/relative", "https://example.com/feed", "https://example.com/"},
	} {
		got, err := PageURL(c.site, c.feed)
		require.NoError(t, err, c)
		require.Equal(t, c.want, got, c)
	}
	_, err := PageURL("", "file:///etc/passwd")
	require.Error(t, err)
}

func TestNextCheck(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	require.Equal(t, now.Add(RecheckAfter), NextCheck(now, true, 0))
	for failures, want := range map[int]time.Duration{
		1: 6 * time.Hour, 2: 12 * time.Hour, 3: 24 * time.Hour, 4: 48 * time.Hour, 5: 96 * time.Hour, 6: RecheckAfter, 40: RecheckAfter,
	} {
		require.Equal(t, now.Add(want), NextCheck(now, false, failures), failures)
	}
}

func TestSniff(t *testing.T) {
	ct, err := sniff(pngOf(t, 32))
	require.NoError(t, err)
	require.Equal(t, "image/png", ct)
	ct, err = sniff(icoOf(t, 16))
	require.NoError(t, err)
	require.Equal(t, "image/x-icon", ct)

	gif1x1 := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	for name, b := range map[string][]byte{
		"svg":         []byte(svg),
		"svg+xmldecl": []byte(`<?xml version="1.0"?>` + svg),
		"html":        []byte("<!doctype html><title>x</title>"),
		"empty":       nil,
		"pixel":       gif1x1,
		"png header":  []byte("\x89PNG\r\n\x1a\ngarbage"),
		"huge png":    pngOf(t, maxSide+1),
		"bad ico":     {0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 32, 0, 0xff, 0xff, 0, 0, 22, 0, 0, 0},
		"ico zero":    {0, 0, 1, 0, 0, 0},
	} {
		_, err := sniff(b)
		require.Error(t, err, name)
	}
}

func TestIconLinksRankingAndFiltering(t *testing.T) {
	page := `<html><head>
		<base href="/sub/">
		<base href="https://ignored.example.org/">
		<link rel="icon" href="a16.png" sizes="16x16">
		<link rel="icon" href="a.png">
		<link rel="ICON" href="a192.png" sizes="192x192">
		<link rel="apple-touch-icon-precomposed" href="apple.png">
		<link rel="icon" href="a48.png" sizes="16x16 48X48">
		<link rel="icon" href="a48.png#dup" sizes="48x48">
		<link rel="icon" href="vector.SVG">
		<link rel="icon" type="image/svg+xml" href="v">
		<link rel="icon" type="text/html" href="h">
		<link rel="icon" href="data:image/png;base64,AAAA">
		<link rel="icon" href="javascript:alert(1)">
		<link rel="icon" href="">
		<link rel="alternate icon" href="//cdn.example.net/alt.ico">
		<link rel="mask-icon" href="mask">
		<link rel="fluid-icon" href="fluid.png">
		</head><body><link rel="icon" href="body.png"></body></html>`
	got := iconLinks([]byte(page), "https://example.com/blog/post")
	var urls []string
	for _, c := range got {
		urls = append(urls, c.URL)
	}
	require.Equal(t, []string{
		"https://example.com/sub/a48.png",   // in band, closest to 64
		"https://example.com/sub/apple.png", // 180 by convention, in band
		"https://example.com/sub/a.png",     // unknown size
		"https://cdn.example.net/alt.ico",   // unknown size, later
		"https://example.com/sub/a192.png",  // out of band by 12
		"https://example.com/sub/a16.png",   // out of band by 16
	}, urls)
}

func TestIconLinksBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, `<link rel=icon href="/i%d.png">`, i)
	}
	require.Len(t, iconLinks([]byte(b.String()), "https://example.com/"), maxLinks)
	require.Empty(t, iconLinks([]byte(`<link rel=icon href=/x.png>`), "ftp://example.com/"))
}

// Issue #157: a candidate is offered only if its string parses back as an
// absolute http(s) URL. An unbracketed IPv6-style host from a scheme-relative
// href resolved to "http://::", which url.Parse rejects.
func TestIconLinksOnlyOfferURLsThatReparse(t *testing.T) {
	for _, tc := range []struct {
		name, body, base string
		want             []string
	}{
		{"fuzz seed", `<link rel=iCon href=//::>`, "http://0", nil},
		{"https base", `<link rel=icon href=//::>`, "https://example.com/", nil},
		{"with a path", `<link rel=icon href=//::/i.png>`, "https://example.com/", nil},
		{"three colons", `<link rel=icon href=//:::>`, "https://example.com/", nil},
		{"good sibling kept", `<link rel=icon href=//::><link rel=icon href=/i.png>`, "http://0", []string{"http://0/i.png"}},
		{"bad base href ignored", `<base href=//::><link rel=icon href=/i.png>`, "https://example.com/p", []string{"https://example.com/i.png"}},
		{"bad page URL", `<link rel=icon href=/i.png>`, "http://::", nil},
		{"bracketed IPv6 is fine", `<link rel=icon href=//[::1]/i.png>`, "https://example.com/", []string{"https://[::1]/i.png"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range iconLinks([]byte(tc.body), tc.base) {
				u, err := url.Parse(c.URL)
				require.NoError(t, err, c.URL)
				require.True(t, httpURL(u), c.URL)
				got = append(got, c.URL)
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseSizes(t *testing.T) {
	for in, want := range map[string]int{
		"": 0, "any": 0, "16x16": 16, "16x16 32x32": 32, "32X24": 32, "0x0": 0, "x": 0, "-1x5": 0,
		"99999x1": 0, "57x57 any 114x114": 114,
	} {
		require.Equal(t, want, parseSizes(in), in)
	}
}

func TestAPageInACodingNobodyAskedForIsNotScanned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<link rel="icon" href="/i.png">`))
	}))
	t.Cleanup(srv.Close)
	_, err := lookup(t, srv.URL)
	require.Error(t, err)
}
