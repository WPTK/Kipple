// Package opml imports and exports the subscription list (design §7.6).
package opml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// NS is the namespace of the kipple:* override attributes.
const NS = "https://kipple.invalid/opml/1"

// Attrs are the validated kipple:* overrides of one feed. Nil means "not set".
type Attrs struct {
	Interval         *int
	Retention        *int // 0 = unlimited
	Fulltext         *bool
	Dedup            *string
	UserAgent        *string
	IgnoreHTTPCache  *bool
	DisableHTTP2     *bool
	AllowInsecureTLS *bool
	AllowPrivateNet  *bool
	Enabled          *bool
}

// Feed is one parsed subscription, in document order.
type Feed struct {
	URL      string // xmlUrl, entity-decoded and trimmed
	Title    string // text preferred over title; may be empty
	SiteURL  string
	Folder   []string // the folder as its chain of names from the top level; empty = root (Uncategorized)
	Attrs    Attrs
	BadAttrs []string // kipple:* attributes that failed validation
	// cut: the file put the feed in a folder too deep to keep, and Folder is the deepest kept
	// ancestor. Import never moves an existing feed there.
	cut bool
}

// Check is the refusal of a document with no feeds. Folders and no feeds would only make empty folders, and is not the
// export of a reader. Import calls it first; a caller that only counts (the restore wizard) calls it itself. Parse
// does not, so an exported empty library still parses.
func (d *Doc) Check() error {
	if len(d.Feeds) == 0 {
		return &EmptyError{Folders: true}
	}
	return nil
}

// Doc is a parsed OPML document: its folder tree and its feeds.
type Doc struct {
	// Folders holds every named outline that is not a feed, pure containers and empty folders
	// included, as its chain of names from the top level, in document order (a parent before its
	// children). Among siblings, names that differ only by ASCII case (SQLite's NOCASE) are one
	// folder (first spelling kept); the same name under different parents is a different folder.
	Folders [][]string
	Feeds   []Feed // document order
	// FoldersMergedCase lists case-variant spellings merged into an earlier sibling, by path.
	FoldersMergedCase []MergedCase
	// FoldersRefused lists the folders that were not kept, by path, with the reason. Parse reports
	// outlines nested deeper than store.MaxFolderDepth; Import adds the folders the folder writer
	// refuses. What a refused folder holds goes into its deepest kept ancestor.
	FoldersRefused []RefusedFolder
}

// MergedCase records a folder merged into a sibling whose name differs only by case.
type MergedCase struct {
	Kept   string `json:"kept"`
	Merged string `json:"merged"`
}

// RefusedFolder is a folder that was not created; its feeds went into its deepest kept ancestor.
type RefusedFolder struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	chain  []string
}

// MergedPath records a folder of the file that resolved to an existing folder with the same full
// path but other levels: the file's Music > AC > DC filed into a folder literally named "AC/DC" inside
// Music (full paths are unique, so both cannot exist). Each side is the chain of names from the top.
type MergedPath struct {
	Kept   []string `json:"kept"`
	Merged []string `json:"merged"`
}

// foldCase lowercases ASCII letters only: SQLite's NOCASE, the rule folder names are compared by.
func foldCase(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; c >= 'A' && c <= 'Z' {
					b[j] = c + 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// Path joins a folder chain with '/', the way the Reader API names a folder. It is for reports
// only: a '/' inside a name makes a path ambiguous, so the code compares chains, never paths.
func Path(chain []string) string { return strings.Join(chain, "/") }

// refusal is the report text of a folder writer error.
func refusal(err error) string { return strings.TrimPrefix(err.Error(), "store: ") }

type outline struct {
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []outline  `xml:"outline"`
}

type document struct {
	XMLName xml.Name
	Body    struct {
		Outlines []outline `xml:"outline"`
	} `xml:"body"`
}

func (o outline) get(name string) string {
	for _, a := range o.Attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			if name == "xmlUrl" || name == "htmlUrl" {
				// URLs are already correctly unescaped by the XML decoder; a
				// second pass would corrupt "?a=1&section=x" style queries.
				return strings.TrimSpace(a.Value)
			}
			return decode(a.Value)
		}
	}
	return ""
}

// doubleEscapedAmp matches a literal "&amp;" chain left after XML decoding
// (some exporters write "&amp;amp;"): only chains with their semicolons are undone.
var doubleEscapedAmp = regexp.MustCompile(`&(?:amp;)+`)

// decode undoes double-escaped ampersands on top of the XML
// decoder's own unescaping. Legacy no-semicolon entities are left alone.
func decode(s string) string {
	if strings.Contains(s, "&amp;") {
		s = doubleEscapedAmp.ReplaceAllString(s, "&")
	}
	return strings.TrimSpace(s)
}

func (o outline) name() string {
	if t := o.get("text"); t != "" {
		return t
	}
	return o.get("title")
}

// ErrNotOPML is Parse's refusal of well-formed XML (an RSS feed, a web page) whose root is not <opml>, and of a file
// with no element at all (empty, or JSON).
var ErrNotOPML = errors.New("opml: not an OPML document")

// EmptyError is Parse's refusal of a file that holds no feeds: an empty file (Folders false), or one whose outlines
// are folders only (Folders true), which would only make empty folders and is not the export of a reader.
type EmptyError struct{ Folders bool }

func (e *EmptyError) Error() string {
	if e.Folders {
		return "opml: the file has folders but no feeds"
	}
	return "opml: the file is empty"
}

// Refusal maps a Parse error that refuses the file's content to the code and plain-language message every caller
// (web import, Reader API, command line, restore wizard) reports, and false for any other error. It is the one place
// the refusals are named.
func Refusal(err error) (code, message string, ok bool) {
	var entity *UndefinedEntityError
	var empty *EmptyError
	switch {
	case errors.Is(err, ErrNotOPML):
		return "not_opml", "that file is not an OPML file", true
	case errors.As(err, &entity):
		return "opml_entity", strings.TrimPrefix(entity.Error(), "opml: "), true
	case errors.As(err, &empty):
		if empty.Folders {
			return "empty_opml", "that OPML file has no feeds in it", true
		}
		return "empty_opml", "that file is empty", true
	}
	return "", "", false
}

// UndefinedEntityError is Parse's refusal of a file that names an entity neither XML nor HTML defines (`&x;`): the
// decoder would keep it as literal text and it would become a folder or feed name. A DOCTYPE's own entities are never
// expanded, so a reference to one is refused too.
type UndefinedEntityError struct {
	Name string // without the & and ;
	Line int
}

func (e *UndefinedEntityError) Error() string {
	return fmt.Sprintf("opml: &%s; on line %d is not an entity XML or HTML defines", e.Name, e.Line)
}

// entityRef is a character reference as the decoder reads one: an XML name (letters, digits, . _ : -) or a number.
var entityRef = regexp.MustCompile(`&(#[A-Za-z0-9]+|[\pL_:][\pL\pN._:-]*);`)

// referenceRunes is what the reference m ("&name;", "&#38;") stands for, and false when neither XML nor HTML defines it.
// A name counts only when HTML defines it with its semicolon, not by the prefix of an old name.
func referenceRunes(name string) (string, bool) {
	if name[0] == '#' {
		base, digits := 10, name[1:]
		if digits[0] == 'x' || digits[0] == 'X' {
			base, digits = 16, digits[1:]
		}
		n, err := strconv.ParseInt(digits, base, 32)
		if err != nil || n == 0 || !utf8.ValidRune(rune(n)) {
			return "", false
		}
		return string(rune(n)), true
	}
	full := "&" + name + ";"
	decoded := html.UnescapeString(full)
	if decoded == full {
		return "", false
	}
	// html.UnescapeString also reads the old names that need no semicolon by their prefix ("&ampx;" is "&" and "x;").
	for i := 2; i < len(name); i++ {
		if head := html.UnescapeString("&" + name[:i]); head != "&"+name[:i] && decoded == head+name[i:]+";" {
			return "", false
		}
	}
	return decoded, true
}

// resolveEntities rewrites the named character references inside the start tags of b (where OPML keeps all its
// data) that are not XML's own five as numeric references, so every HTML name decodes, and refuses a name neither XML
// nor HTML defines. Comments, the DOCTYPE and text are left alone. A file the tokenizer cannot read is returned as it
// is, for the decoder to report.
func resolveEntities(b []byte) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) { return in, nil }
	var out bytes.Buffer
	last := int64(0)
	for {
		start := dec.InputOffset()
		tok, err := dec.RawToken()
		if err != nil {
			break
		}
		if _, ok := tok.(xml.StartElement); !ok {
			continue
		}
		end := dec.InputOffset()
		seg := b[start:end]
		if !bytes.Contains(seg, []byte("&")) {
			continue
		}
		var bad *UndefinedEntityError
		fixed := entityRef.ReplaceAllFunc(seg, func(m []byte) []byte {
			name := string(m[1 : len(m)-1])
			switch name {
			case "amp", "lt", "gt", "quot", "apos":
				return m
			}
			s, ok := referenceRunes(name)
			if !ok {
				if bad == nil {
					bad = &UndefinedEntityError{Name: name, Line: 1 + bytes.Count(b[:start], []byte("\n"))}
				}
				return m
			}
			var r []byte
			for _, c := range s {
				r = fmt.Appendf(r, "&#%d;", c)
			}
			return r
		})
		if bad != nil {
			return nil, bad
		}
		out.Write(b[last:start])
		out.Write(fixed)
		last = end
	}
	if last == 0 {
		return b, nil
	}
	out.Write(b[last:])
	return out.Bytes(), nil
}

// Parse reads an OPML document. Every named outline without an xmlUrl is a
// folder and nests as it does in the file, down to store.MaxFolderDepth levels;
// an unnamed wrapper is transparent.
func Parse(r io.Reader) (*Doc, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("opml: %w", err)
	}
	if len(bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))) == 0 {
		return nil, &EmptyError{}
	}
	// Decode to UTF-8 first (BOM, XML declaration, ISO-8859-1, UTF-16...), which
	// also rewrites the declaration so the XML decoder needs no CharsetReader.
	body, err := resolveEntities(fetch.DecodeBody(raw, "").Body)
	if err != nil {
		return nil, err
	}
	// Non-strict, so the bare "&" of a hand-written URL query still reads.
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) { return in, nil }
	var d document
	if err := dec.Decode(&d); errors.Is(err, io.EOF) {
		return nil, ErrNotOPML
	} else if err != nil {
		return nil, fmt.Errorf("opml: %w", err)
	}
	// The decoder accepts any well-formed XML or HTML; only an <opml> root is an OPML document.
	if !strings.EqualFold(d.XMLName.Local, "opml") {
		return nil, ErrNotOPML
	}
	doc := &Doc{}
	type node struct {
		chain []string
		kids  map[string]*node // foldCase(name) -> child folder
	}
	tooDeep := map[string]bool{} // chainKey(foldCase) of each too-deep outline reported
	// cut is true below an outline too deep to keep: everything there goes into parent, the
	// deepest kept ancestor, and only that topmost outline is reported (the way Import reports a
	// refused folder once, at its top). Reporting each outline below it would build a path per
	// level, quadratic in a file nested thousands deep.
	var walk func(list []outline, parent *node, cut bool)
	walk = func(list []outline, parent *node, cut bool) {
		for _, o := range list {
			if u := o.get("xmlUrl"); u != "" {
				f := Feed{URL: u, Title: o.name(), SiteURL: o.get("htmlUrl"), Folder: parent.chain, cut: cut}
				f.Attrs, f.BadAttrs = parseAttrs(o.Attrs)
				doc.Feeds = append(doc.Feeds, f)
				continue
			}
			name := o.name()
			if name == "" || cut {
				walk(o.Children, parent, cut)
				continue
			}
			chain := make([]string, len(parent.chain)+1)
			copy(chain, parent.chain)
			chain[len(parent.chain)] = name
			if len(chain) > store.MaxFolderDepth {
				// Too deep to keep: what it holds goes into the deepest kept ancestor. The cap also
				// bounds the chains Parse builds (the XML decoder nests up to 10000 levels).
				if k := foldCase(chainKey(chain)); !tooDeep[k] {
					tooDeep[k] = true
					doc.FoldersRefused = append(doc.FoldersRefused, RefusedFolder{Path(chain), refusal(store.ErrFolderDepth), chain})
				}
				walk(o.Children, parent, true)
				continue
			}
			key := foldCase(name)
			n, ok := parent.kids[key]
			if !ok {
				n = &node{chain: chain, kids: map[string]*node{}}
				parent.kids[key] = n
				doc.Folders = append(doc.Folders, chain)
			} else if n.chain[len(n.chain)-1] != name {
				doc.FoldersMergedCase = appendMerged(doc.FoldersMergedCase, Path(n.chain), Path(chain))
			}
			walk(o.Children, n, false)
		}
	}
	walk(d.Body.Outlines, &node{kids: map[string]*node{}}, false)
	return doc, nil
}

func appendMerged(l []MergedCase, kept, merged string) []MergedCase {
	for _, m := range l {
		if m.Kept == kept && m.Merged == merged {
			return l
		}
	}
	return append(l, MergedCase{kept, merged})
}

// Valid retention caps (design feeds.retention CHECK); 0 = unlimited.
var validRetention = map[int]bool{0: true, 50: true, 100: true, 250: true, 500: true, 1000: true}

func parseAttrs(attrs []xml.Attr) (Attrs, []string) {
	var a Attrs
	var bad []string
	for _, at := range attrs {
		if at.Name.Space != NS {
			continue
		}
		v := strings.TrimSpace(at.Value)
		ok := true
		switch at.Name.Local {
		case "interval":
			n, err := atoi(v)
			ok = err == nil && n >= 5 && n <= 10080
			if ok {
				a.Interval = &n
			}
		case "retention":
			n, err := atoi(v)
			if v == "unlimited" {
				n, err = 0, nil
			}
			ok = err == nil && validRetention[n]
			if ok {
				a.Retention = &n
			}
		case "dedup":
			ok = v == "auto" || v == "link" || v == "link_title"
			if ok {
				a.Dedup = &v
			}
		case "user_agent":
			// The feed PATCH rule: up to 500 characters, no control characters
			// (the value becomes a request header).
			ok = len(v) <= maxUserAgentLen && !hasControl(v)
			if ok && v != "" {
				a.UserAgent = &v
			}
		case "fulltext":
			a.Fulltext, ok = parseBool(v)
		case "ignore_http_cache":
			a.IgnoreHTTPCache, ok = parseBool(v)
		case "disable_http2":
			a.DisableHTTP2, ok = parseBool(v)
		case "allow_insecure_tls":
			a.AllowInsecureTLS, ok = parseBool(v)
		case "allow_private_net":
			a.AllowPrivateNet, ok = parseBool(v)
		case "enabled":
			a.Enabled, ok = parseBool(v)
		default:
			ok = false
		}
		if !ok {
			bad = append(bad, "kipple:"+at.Name.Local+"="+at.Value)
		}
	}
	return a, bad
}

// maxUserAgentLen matches the feed PATCH limit for user_agent (internal/api).
const maxUserAgentLen = 500

// hasControl is the feed PATCH control-character rule: below 0x20 except tab, or DEL.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return true
		}
	}
	return false
}

func atoi(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || fmt.Sprint(n) != s {
		return 0, fmt.Errorf("not an integer: %q", s)
	}
	return n, nil
}

func parseBool(s string) (*bool, bool) {
	var b bool
	switch s {
	case "1", "true":
		b = true
	case "0", "false":
	default:
		return nil, false
	}
	return &b, true
}

// urlKey normalizes a feed URL to its scheme-less key, for in-document dedup.
func urlKey(u string) (string, error) { return feedurl.Key(u) }
