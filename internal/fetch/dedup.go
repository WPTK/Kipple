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

// textFallbackUID is the last-resort uid: 'h:' + h(title 0x1f text).
func textFallbackUID(title, text string) string { return "h:" + H(title+sep+text) }

// AssignUIDs sets Item.UID and Item.LinkHash for every item and applies the
// in-document duplicate rules. It returns the fetch notes (currently only
// "guid_duplicates: k/n") and the items to keep: in link and link_title modes
// an item whose uid repeats an earlier one in the same document is dropped,
// because (feed_id, uid) is unique in the store. Auto mode never drops: a
// repeated guid is re-keyed as design §4.8 says (Miniflux behaviour).
//
// Items must already carry RawLink, GUID, Title and ContentText.
func AssignUIDs(items []Item, mode string) (kept []Item, notes []string) {
	seenUID := make(map[string]bool, len(items))
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

		var uid string
		switch mode {
		case DedupLink:
			uid = linkUID(it)
		case DedupLinkTitle:
			if it.RawLink != "" {
				uid = "l:" + H(it.RawLink+sep+it.Title)
			} else {
				uid = textFallbackUID(it.Title, it.ContentText)
			}
		default: // auto
			switch {
			case guid != "":
				n := guidCount[guid]
				uid = "g:" + H(guid)
				if n > 0 {
					if it.RawLink != "" {
						uid = "g:" + H(guid+"|"+it.RawLink)
					} else {
						uid = "g:" + H(guid+"|"+strconv.Itoa(n))
					}
					// Pathological: same guid and same link repeated. Keep
					// bumping the counter until the key is free.
					for k := n + 1; seenUID[uid]; k++ {
						uid = "g:" + H(guid+"|"+it.RawLink+"|"+strconv.Itoa(k))
					}
				}
			case it.RawLink != "":
				uid = "l:" + H(it.RawLink)
			default:
				uid = textFallbackUID(it.Title, it.ContentText)
			}
		}
		if guid != "" {
			guidCount[guid]++
		}

		if seenUID[uid] {
			continue // only reachable in link/link_title modes or identical h: items
		}
		seenUID[uid] = true
		it.UID = uid
		kept = append(kept, *it)
	}

	// Guid-less feeds (Atom without id, RSS without guid) are normal and key on
	// the link, so only repeated non-empty guids count, against the non-empty ones.
	if guided > 0 && badGUIDs*100 > 5*guided {
		notes = append(notes, fmt.Sprintf("guid_duplicates: %d/%d", badGUIDs, guided))
	}
	return kept, notes
}

func linkUID(it *Item) string {
	if it.RawLink != "" {
		return "l:" + H(it.RawLink)
	}
	return textFallbackUID(it.Title, it.ContentText)
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
