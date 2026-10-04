package main

import (
	"math/rand"
	"strings"
)

// head is the most frequent vocabulary, so common-term searches have realistic hit rates.
var head = strings.Fields(`the of and to in a is that for it as was with be by on not he this are or his from at which
but have an had they you were their one all we can her has there been if more when will would who so no
market update release software government people city company year report new data security network system
service public school health energy climate research story world power water open game music review price
engine project team user build model device policy review court election season travel video camera phone
server cloud code design cost food science law bank trade`)

// Planted terms: rare appears in exactly rareDocs items, mid in about one item in midEvery.
const (
	rareTerm = "zyxquorn"
	rareDocs = 5
	midTerm  = "photosynthesis"
	midEvery = 330
)

var syll = strings.Fields(`ba be bi bo bu ca ce co cu da de di do du fa fe fi fo ga ge gi go ha he hi ho ka ke ki ko la le li lo lu
ma me mi mo mu na ne ni no nu pa pe pi po ra re ri ro ru sa se si so su ta te ti to tu va ve vi vo wa we xo za ze
an en in on un ar er or ur al el il ol st tr pl gr cr br`)

// vocab is the word list: head words first (Zipf ranks), then deterministic synthetic words.
func vocab(size int) []string {
	r := rand.New(rand.NewSource(7)) // #nosec G404 -- deterministic fixture data, not security
	seen := map[string]bool{rareTerm: true, midTerm: true}
	words := append([]string(nil), head...)
	for _, w := range head {
		seen[w] = true
	}
	for len(words) < size {
		n := 2 + r.Intn(3)
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(syll[r.Intn(len(syll))])
		}
		w := b.String()
		if !seen[w] {
			seen[w] = true
			words = append(words, w)
		}
	}
	return words
}

type textGen struct {
	words []string
}

func newTextGen() *textGen { return &textGen{words: vocab(30000)} }

// sentence appends one sentence of n words.
func (g *textGen) sentence(b *strings.Builder, r *rand.Rand, z *rand.Zipf, n int) {
	for i := 0; i < n; i++ {
		w := g.words[z.Uint64()]
		if i == 0 {
			b.WriteString(strings.ToUpper(w[:1]))
			b.WriteString(w[1:])
		} else {
			b.WriteByte(' ')
			b.WriteString(w)
		}
	}
	b.WriteString(". ")
	_ = r
}

// title is 5 to 11 words.
func (g *textGen) title(r *rand.Rand, z *rand.Zipf) string {
	n := 5 + r.Intn(7)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = g.words[z.Uint64()]
	}
	s := strings.Join(parts, " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// article returns HTML and plain text of about words words, plus the word count.
// plant is a term to insert once (empty for none).
func (g *textGen) article(r *rand.Rand, z *rand.Zipf, words int, plant string) (html, text string, n int) {
	var h, t strings.Builder
	planted := plant == ""
	for n < words {
		pw := 40 + r.Intn(90)
		var p strings.Builder
		for w := 0; w < pw; {
			s := 7 + r.Intn(14)
			g.sentence(&p, r, z, s)
			w += s
		}
		para := strings.TrimSpace(p.String())
		if !planted {
			para = para + " " + plant + "."
			planted = true
		}
		n += pw
		t.WriteString(para)
		t.WriteString("\n\n")
		switch r.Intn(10) {
		case 0:
			h.WriteString("<h2>" + g.title(r, z) + "</h2>")
			h.WriteString("<p>" + para + "</p>")
		case 1:
			h.WriteString(`<p><a href="https://example.com/a/` + g.words[z.Uint64()] + `">` + para + `</a></p>`)
		default:
			h.WriteString("<p>" + para + "</p>")
		}
	}
	return h.String(), strings.TrimSpace(t.String()), n
}
