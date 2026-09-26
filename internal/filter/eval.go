package filter

import (
	"strings"
	"sync"
)

// Result is the outcome of evaluating a set against one item.
type Result struct {
	// Star: a star rule fired. Star beats mute.
	Star bool
	// Muted: a mute rule fired and no star rule did. MutedBy is then the lowest id among the
	// firing mute rules (items.muted_by).
	Muted   bool
	MutedBy int64
	// MarkRead: a mark_read rule fired.
	MarkRead bool
	// Read is the effective read state a rule asks for: Muted or MarkRead. A rule can only add
	// read, never clear it.
	Read bool
	// Matched lists every rule that fired, whatever its action (highlight included), in
	// ascending id order. The caller bumps hit counts from it.
	Matched []int64
}

// Any reports whether any rule fired.
func (r Result) Any() bool { return len(r.Matched) > 0 }

const nField = 6

func fieldIndex(f Field) int {
	switch f {
	case FieldTitle:
		return 0
	case FieldAuthor:
		return 1
	case FieldContent:
		return 2
	case FieldURL:
		return 3
	case FieldCategory:
		return 4
	}
	return 5
}

// view is one field normalized one way (fold x lower), with its word set built on demand.
type view struct {
	built, setBuilt bool
	text            string
	set             map[string]struct{}
}

// scratch is the per-item cache. It is pooled: the word-set maps are the only allocations
// worth keeping.
type scratch struct {
	item    *Item
	raw     [nField][2]string // [field][0 text-kind, 1 regex-kind]
	canon   [nField]string    // canon(raw regex-kind text), for fold-case prefilters
	canonOK [nField]bool
	rawOK   [nField][2]bool
	views   [nField][4]view // index fold<<1 | lower
	matched []int64
}

var scratchPool = sync.Pool{New: func() any { return new(scratch) }}

// release drops the references to the item's strings and returns sc to the pool.
func (sc *scratch) release() {
	sc.item = nil
	sc.raw = [nField][2]string{}
	sc.canon = [nField]string{}
	for f := range sc.views {
		for v := range sc.views[f] {
			sc.views[f][v].text = ""
		}
	}
	scratchPool.Put(sc)
}

func (sc *scratch) reset(it *Item) {
	sc.item = it
	sc.rawOK = [nField][2]bool{}
	sc.canonOK = [nField]bool{}
	for f := range sc.views {
		for v := range sc.views[f] {
			vw := &sc.views[f][v]
			if vw.setBuilt {
				clear(vw.set)
			}
			vw.built, vw.setBuilt, vw.text = false, false, ""
		}
	}
	sc.matched = sc.matched[:0]
}

// rawText is the field's text, truncated to the scan limit of the rule kind.
func (sc *scratch) rawText(f Field, regex bool) string {
	fi := fieldIndex(f)
	k := 0
	if regex {
		k = 1
	}
	if sc.rawOK[fi][k] {
		return sc.raw[fi][k]
	}
	it := sc.item
	limit := MaxFieldScan
	var s string
	switch f {
	case FieldTitle:
		s = it.Title
	case FieldAuthor:
		s = it.Author
	case FieldURL:
		s = it.URL
	case FieldFeed:
		s = it.FeedTitle
	case FieldContent:
		s = it.Content
		limit = MaxTextContentScan
		if regex {
			limit = MaxRegexContentScan
		}
	case FieldCategory:
		sep := "\x1f" // text kind: a phrase never spans two categories
		if regex {
			sep = "\n"
		}
		s = strings.Join(it.Categories, sep)
	}
	s = truncate(s, limit)
	sc.raw[fi][k], sc.rawOK[fi][k] = s, true
	return s
}

func (sc *scratch) view(f Field, fold, lower bool) *view {
	idx := 0
	if fold {
		idx |= 2
	}
	if lower {
		idx |= 1
	}
	v := &sc.views[fieldIndex(f)][idx]
	if !v.built {
		v.text = normalize(sc.rawText(f, false), fold, lower)
		v.built = true
	}
	return v
}

func (v *view) wordSet() map[string]struct{} {
	if !v.setBuilt {
		if v.set == nil {
			v.set = make(map[string]struct{}, 64)
		}
		for _, w := range words(v.text) {
			v.set[w] = struct{}{}
		}
		v.setBuilt = true
	}
	return v.set
}

// textFires reports whether any term matches in any of the rule's fields.
func (c *Compiled) textFires(sc *scratch) bool {
	tm := c.text
	for _, f := range c.fields {
		v := sc.view(f, tm.fold, tm.lower)
		if v.text == "" {
			continue
		}
		if len(tm.singles) > 0 {
			set := v.wordSet()
			if len(set) < len(tm.singles) {
				for w := range set {
					if _, ok := tm.singleSet[w]; ok {
						return true
					}
				}
			} else {
				for _, s := range tm.singles {
					if _, ok := set[s]; ok {
						return true
					}
				}
			}
		}
		for i := range tm.others {
			t := &tm.others[i]
			if t.tok != "" {
				if _, ok := v.wordSet()[t.tok]; !ok {
					continue
				}
			}
			if containsTerm(v.text, t.text, tm.whole) {
				return true
			}
		}
	}
	return false
}

// couldMatch is the regex prefilter: false means the pattern cannot match s (field f).
func (sc *scratch) couldMatch(p *cre, f Field, s string) bool {
	if p.req == nil {
		return true
	}
	for _, l := range p.req {
		if l.fold {
			fi := fieldIndex(f)
			if !sc.canonOK[fi] {
				sc.canon[fi], sc.canonOK[fi] = canon(s), true
			}
			if strings.Contains(sc.canon[fi], l.s) {
				return true
			}
		} else if strings.Contains(s, l.s) {
			return true
		}
	}
	return false
}

func (c *Compiled) regexFires(sc *scratch) bool {
	for _, f := range c.fields {
		s := sc.rawText(f, true)
		if s == "" {
			continue
		}
		for i := range c.res {
			p := &c.res[i]
			if !sc.couldMatch(p, f, s) {
				continue
			}
			if p.re.MatchString(s) {
				return true
			}
		}
	}
	return false
}

func (c *Compiled) fires(sc *scratch) bool {
	var m bool
	if c.text != nil {
		m = c.textFires(sc)
	} else {
		m = c.regexFires(sc)
	}
	return m != c.rule.Invert
}

// Fires reports whether the rule fires on the item, ignoring its scope and Enabled flag
// (the preview evaluates an unsaved rule against items of a scope it chooses).
func (c *Compiled) Fires(it Item) bool {
	sc := scratchPool.Get().(*scratch)
	sc.reset(&it)
	ok := c.fires(sc)
	sc.release()
	return ok
}

// Applies reports whether the rule's scope covers the item (Enabled is not looked at).
func (c *Compiled) Applies(it Item) bool { return c.applies(&it) }

// Evaluate runs every enabled rule in scope against the item and combines the outcomes
// (package comment, "Precedence"). A nil or empty set returns the zero Result. It is safe to
// call from many goroutines.
func (s *Set) Evaluate(it Item) Result {
	var res Result
	if s == nil || len(s.rules) == 0 {
		return res
	}
	sc := scratchPool.Get().(*scratch)
	sc.reset(&it)
	var mutedBy int64
	for _, c := range s.rules {
		if !c.rule.Enabled || !c.applies(&it) || !c.fires(sc) {
			continue
		}
		sc.matched = append(sc.matched, c.rule.ID)
		switch c.rule.Action {
		case ActionStar:
			res.Star = true
		case ActionMute:
			if mutedBy == 0 {
				mutedBy = c.rule.ID // rules run in ascending id order: the first is the lowest
			}
			res.Muted = true
		case ActionMarkRead:
			res.MarkRead = true
		}
	}
	if res.Star {
		res.Muted = false
	} else if res.Muted {
		res.MutedBy = mutedBy
	}
	res.Read = res.Muted || res.MarkRead
	if len(sc.matched) > 0 {
		res.Matched = append([]int64(nil), sc.matched...)
	}
	sc.release()
	return res
}
