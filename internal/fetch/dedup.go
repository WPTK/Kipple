package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Dedup modes (feeds.dedup_mode).
const (
	DedupAuto      = "auto"
	DedupLink      = "link"
	DedupLinkTitle = "link_title"
)

const sep = "\x1f"

// H is sha256 truncated to 32 hex characters (design §4.8 h(x)).
func H(x string) string {
	sum := sha256.Sum256([]byte(x))
	return hex.EncodeToString(sum[:16])
}

// hashChunked is H for a long string, read in pieces so the string is not copied whole.
func hashChunked(x string) string {
	h := sha256.New()
	var buf [32 << 10]byte
	for len(x) > 0 {
		n := copy(buf[:], x)
		h.Write(buf[:n])
		x = x[n:]
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// textFallbackUID is the last-resort uid: 'h:' + h(title 0x1f text).
func textFallbackUID(title, text string) string { return "h:" + H(title+sep+text) }

// fallbackUID is the last-resort uid of an entry with no guid and no link: its title and its text, and for an entry
// whose HTML was cut, a hash of the whole text before the cut, so two entries alike in the part that was kept stay two.
func (it *Item) fallbackUID() string {
	if it.cutKey == "" {
		return textFallbackUID(it.Title, it.ContentText)
	}
	return textFallbackUID(it.Title, it.ContentText+sep+it.cutKey)
}

// AssignUIDs sets Item.UID, Item.AltUID and Item.LinkHash for every item and
// applies the in-document duplicate rules. It returns the fetch notes
// (currently only "guid_duplicates: k/n") and the items to keep: an item whose
// uid repeats an earlier one in the same document is dropped, because
// (feed_id, uid) is unique in the store. In auto mode that only happens to
// entries nothing tells apart: a repeated guid is keyed by guid and link for
// every occurrence, and a link shared by several guid-less entries is keyed by
// link and title for every one of them (design §4.8).
//
// Items must already carry RawLink, GUID, Title and ContentText.
func AssignUIDs(items []Item, mode string) (kept []Item, notes []string) {
	if len(items) > 0 {
		kept = make([]Item, 0, len(items)) // at most every item is kept; an empty document still returns nil
	}
	seenUID := make(map[string]bool, len(items))
	guidTotal := make(map[string]int, len(items)) // occurrences per guid in the whole document
	linkTotal := make(map[string]int, len(items)) // occurrences per link among the guid-less items
	for i := range items {
		if g := strings.TrimSpace(items[i].GUID); g != "" {
			guidTotal[g]++
		} else if items[i].RawLink != "" {
			linkTotal[items[i].RawLink]++
		}
	}
	guidCount := make(map[string]int, len(items))
	badGUIDs, guided := 0, 0 // duplicated guids among the non-empty ones

	for i := range items {
		it := &items[i]
		guid := strings.TrimSpace(it.GUID)
		if guid != "" {
			guided++
			if guidCount[guid] > 0 {
				badGUIDs++
			}
		}

		var uid, alt string
		switch mode {
		case DedupLink:
			uid = linkUID(it)
		case DedupLinkTitle:
			if it.RawLink != "" {
				uid = "l:" + H(it.RawLink+sep+it.Title)
			} else {
				uid = it.fallbackUID()
			}
		default: // auto
			switch {
			case guid != "":
				n := guidCount[guid]
				uid = "g:" + H(guid)
				if it.RawLink != "" {
					alt = "g:" + H(guid+"|"+it.RawLink)
				}
				if guidTotal[guid] > 1 {
					// Every occurrence of a repeated guid is keyed by its link, so
					// a uid never depends on where the item sits in the document. It
					// does depend on whether the guid is repeated here: alt is the
					// key on the other side of that line, and the commit carries a
					// row stored under it over to uid.
					if it.RawLink != "" {
						uid, alt = alt, uid
					} else {
						uid = "g:" + H(guid+"|"+strconv.Itoa(n))
					}
					// Pathological: same guid and same link repeated. Keep
					// bumping the counter until the key is free.
					for k := n + 1; seenUID[uid]; k++ {
						uid = "g:" + H(guid+"|"+it.RawLink+"|"+strconv.Itoa(k))
						alt = "" // a counter key has no stable other form
					}
				}
			case linkTotal[it.RawLink] > 1:
				// Every guid-less entry that shares its link with another one (a feed
				// whose entries all point at one page) is keyed by link and title, the
				// link_title key, so none is dropped for its link and none replaces
				// another's row. Like a repeated guid, this depends on whether the
				// link is shared in this document; alt is the key on the other side.
				uid = "l:" + H(it.RawLink+sep+it.Title)
				alt = "l:" + H(it.RawLink)
			case it.RawLink != "":
				uid = "l:" + H(it.RawLink)
				alt = "l:" + H(it.RawLink+sep+it.Title)
			default:
				uid = it.fallbackUID()
			}
		}
		if guid != "" {
			guidCount[guid]++
		}

		if seenUID[uid] {
			continue // link and link_title modes; in auto mode the same link and title twice, or identical h: items
		}
		seenUID[uid] = true
		it.UID, it.AltUID = uid, alt
		kept = append(kept, *it)
	}

	// Guid-less feeds (Atom without id, RSS without guid) are normal and key on
	// the link, so only repeated non-empty guids count, against the non-empty ones
	// (none counted means none repeated).
	if badGUIDs*100 > 5*guided {
		notes = append(notes, fmt.Sprintf("guid_duplicates: %d/%d", badGUIDs, guided))
	}
	return kept, notes
}

// CarriesTombstone reports whether a trimmed item's tombstone stored under AltUID can be taken to be this entry's:
// the key names the guid, or the link and the title. A link key without the title is shared by every entry of
// that page, so a tombstone under it may belong to another.
func (it Item) CarriesTombstone() bool {
	if strings.HasPrefix(it.AltUID, "g:") {
		return true
	}
	return it.AltUID != "" && it.AltUID == "l:"+H(it.RawLink+sep+it.Title)
}

func linkUID(it *Item) string {
	if it.RawLink != "" {
		return "l:" + H(it.RawLink)
	}
	return it.fallbackUID()
}

// ContentHash is h(title | url | author | sanitized content_html). Fields are
// joined with 0x1f. Dates are excluded so a re-dated item is not "changed".
func ContentHash(title, url, author, sanitizedHTML string) string {
	return H(title + sep + url + sep + author + sep + sanitizedHTML)
}

// TextHash is h(title | content_text).
func TextHash(title, contentText string) string {
	return H(title + sep + contentText)
}
