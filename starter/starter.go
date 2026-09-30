package starter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/feedurl"
)

// File is feeds.json (design 7.2).
type File struct {
	Version    int        `json:"version"`
	Categories []Category `json:"categories"`
}

// Category is one group of recommendations; with folders on, one folder.
type Category struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Feeds []Feed `json:"feeds"`
}

// Feed is one recommendation. Checked pre-ticks it in the wizard.
type Feed struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Site        string `json:"site,omitempty"`
	Description string `json:"description,omitempty"`
	Checked     bool   `json:"checked"`
	Lang        string `json:"lang,omitempty"`
}

// MaxFeeds bounds the whole list.
const MaxFeeds = 300

var (
	idRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	langRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// privateSuffixes are names that never resolve publicly.
var privateSuffixes = []string{".local", ".lan", ".internal", ".home.arpa", ".localhost"}

// Parse decodes and validates a feeds.json document (design 7.3): strict
// fields, version 1, at least one category, at most MaxFeeds feeds, unique ids,
// public https URLs and bounded texts.
func Parse(b []byte) (*File, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("starter: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("starter: trailing data after the document")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Validate checks every rule of design 7.3.
func (f *File) Validate() error {
	if f.Version != 1 {
		return fmt.Errorf("starter: version must be 1, got %d", f.Version)
	}
	if len(f.Categories) == 0 {
		return errors.New("starter: at least one category is needed")
	}
	cats := map[string]bool{}
	ids := map[string]bool{}
	keys := map[string]string{}
	n := 0
	for _, c := range f.Categories {
		if !idRE.MatchString(c.ID) {
			return fmt.Errorf("starter: category id %q must match %s", c.ID, idRE)
		}
		if cats[c.ID] {
			return fmt.Errorf("starter: category id %q is used twice", c.ID)
		}
		cats[c.ID] = true
		if err := checkText("category "+c.ID+" title", c.Title, 1, 50); err != nil {
			return err
		}
		for _, fd := range c.Feeds {
			n++
			if n > MaxFeeds {
				return fmt.Errorf("starter: more than %d feeds", MaxFeeds)
			}
			if !idRE.MatchString(fd.ID) {
				return fmt.Errorf("starter: feed id %q must match %s", fd.ID, idRE)
			}
			if ids[fd.ID] {
				return fmt.Errorf("starter: feed id %q is used twice", fd.ID)
			}
			ids[fd.ID] = true
			if err := checkText("feed "+fd.ID+" title", fd.Title, 1, 100); err != nil {
				return err
			}
			if err := checkText("feed "+fd.ID+" description", fd.Description, 0, 200); err != nil {
				return err
			}
			if fd.Lang != "" && !langRE.MatchString(fd.Lang) {
				return fmt.Errorf("starter: feed %s: lang %q is not a BCP 47 tag", fd.ID, fd.Lang)
			}
			if err := CheckPublicHTTPS(fd.URL); err != nil {
				return fmt.Errorf("starter: feed %s url: %w", fd.ID, err)
			}
			if fd.Site != "" {
				if err := CheckPublicHTTPS(fd.Site); err != nil {
					return fmt.Errorf("starter: feed %s site: %w", fd.ID, err)
				}
			}
			key, _, err := feedurl.KeyAndNormalize(fd.URL)
			if err != nil {
				return fmt.Errorf("starter: feed %s url: %w", fd.ID, err)
			}
			if other, dup := keys[key]; dup {
				return fmt.Errorf("starter: feeds %s and %s have the same URL", other, fd.ID)
			}
			keys[key] = fd.ID
		}
	}
	return nil
}

// checkText bounds a text in characters and refuses control characters and
// invalid UTF-8.
func checkText(what, s string, lo, hi int) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("starter: %s is not valid UTF-8", what)
	}
	if n := utf8.RuneCountInString(s); n < lo || n > hi {
		return fmt.Errorf("starter: %s must be %d to %d characters", what, lo, hi)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return fmt.Errorf("starter: %s has control characters", what)
	}
	return nil
}

// CheckPublicHTTPS accepts an absolute https URL on a public DNS name: no user
// info, no fragment, port absent or 443, and the host not an IP literal,
// localhost or a private-use suffix.
func CheckPublicHTTPS(raw string) error {
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return errors.New("spaces or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	switch {
	case u.Scheme != "https":
		return errors.New("must be https")
	case u.Opaque != "" || u.Host == "":
		return errors.New("not an absolute URL")
	case u.User != nil:
		return errors.New("must not contain user info")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return errors.New("must not have a fragment")
	case u.Port() != "" && u.Port() != "443":
		return errors.New("port must be absent or 443")
	case host == "" || host != strings.ToLower(host):
		return errors.New("host must be lowercase")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return errors.New("host must be a DNS name, not an IP address")
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return errors.New("host must be a public DNS name")
	}
	for _, suf := range privateSuffixes {
		if strings.HasSuffix(host, suf) {
			return errors.New("host must be a public DNS name")
		}
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.ContainsFunc(label, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
		}) {
			return errors.New("host must be a DNS name of a-z, 0-9 and '-' labels")
		}
	}
	return nil
}
