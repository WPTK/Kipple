package fetch

import (
	"bytes"
	"cmp"
	"io"
)

// guardState is where a tagGuard is in the markup it is passing through.
type guardState uint8

const (
	gText       guardState = iota // character data
	gLT                           // after "<"
	gTag                          // inside a start or end tag
	gPI                           // inside an instruction, "<?" up to "?>"
	gBang                         // after "<!"
	gCmtOpen                      // after "<!-", the second "-" is due
	gComment                      // inside a comment
	gCDOpen                       // inside "<![", matching "CDATA["
	gCDATA                        // inside a CDATA section
	gDir                          // inside a directive, "<!DOCTYPE" and the like
	gDirLT                        // in a directive, after an unquoted "<", matching "!--"
	gDirComment                   // in a directive, inside a comment
)

// tagGuard passes a reader through and fails it when one start or end tag passes MaxTagBytes, not counting the
// bytes inside quoted attribute values. The XML decoder in
// encoding/xml reads a whole tag before it returns it, so a limit on the tokens it yields comes too late: the
// guard tracks where the tags are in the bytes as the decoder asks for them, and ends the stream with errTagTooLong
// at the byte past the limit, so the decoder never holds more than MaxTagBytes of markup of one tag.
//
// It follows the same rules as the decoder (encoding/xml rawToken, non-strict) for what ends a comment, a CDATA
// section, an instruction, a directive and a quoted attribute value, so that a ">" inside one of them does not
// end a tag, and the markup of a comment is not counted as a tag. Where the decoder would stop on a syntax error
// the guard may be in a different state; nothing it reads after that matters, as the decoder reads nothing more.
type tagGuard struct {
	r      io.Reader
	limit  int // the most bytes in one tag; 0 is MaxTagBytes (a test sets a small one)
	st     guardState
	n      int  // gTag: bytes of the tag so far outside quoted values; gCDOpen and gDirLT: bytes matched
	quote  byte // gTag and gDir: the quote that is open, or 0
	depth  int  // gDir: unquoted "<" opened and not closed
	p0, p1 byte // the last two bytes, in gComment, gCDATA and gDirComment; p1 alone in gPI
	err    error
}

func (g *tagGuard) Read(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	n, err := g.r.Read(p)
	for i := 0; i < n; i++ {
		if g.st == gText { // most of a feed is text: jump to the next "<"
			j := bytes.IndexByte(p[i:n], '<')
			if j < 0 {
				break
			}
			i += j
		}
		if g.step(p[i]) {
			g.err = errTagTooLong
			return i, g.err
		}
	}
	return n, err
}

// step takes one byte and reports whether it is the byte that takes a tag past MaxTagBytes.
func (g *tagGuard) step(b byte) bool {
	switch g.st {
	case gText:
		if b == '<' {
			g.st = gLT
		}
	case gLT:
		switch b {
		case '?':
			g.st, g.p1 = gPI, 0
		case '!':
			g.st = gBang
		default: // "</" or the first byte of a name
			g.st, g.n, g.quote = gTag, 2, 0
		}
	case gTag:
		// The bytes inside a quoted value are not counted (the quotes themselves are): one value is one string,
		// whatever its length, and it is the markup around the values that makes an attribute cost memory.
		if g.quote != 0 && b != g.quote {
			break
		}
		if g.n++; g.n > cmp.Or(g.limit, MaxTagBytes) {
			return true
		}
		switch {
		case g.quote != 0:
			g.quote = 0
		case b == '"' || b == '\'':
			g.quote = b
		case b == '>':
			g.st = gText
		}
	case gPI:
		if g.p1 == '?' && b == '>' {
			g.st = gText
		}
		g.p1 = b
	case gBang:
		switch b {
		case '-':
			g.st = gCmtOpen
		case '[':
			g.st, g.n = gCDOpen, 0
		default: // the decoder keeps this first byte without looking at it
			g.st, g.quote, g.depth = gDir, 0, 0
		}
	case gCmtOpen:
		g.st = gText // a second "-" is next; anything else is a syntax error
		if b == '-' {
			g.st, g.p0, g.p1 = gComment, 0, 0
		}
	case gComment:
		if g.p0 == '-' && g.p1 == '-' {
			g.st = gText // ">" ends it; anything else is a syntax error
			break
		}
		g.p0, g.p1 = g.p1, b
	case gCDOpen:
		if b != "CDATA["[g.n] {
			g.st = gText // a syntax error
			break
		}
		if g.n++; g.n == len("CDATA[") {
			g.st, g.p0, g.p1 = gCDATA, 0, 0
		}
	case gCDATA:
		if g.p0 == ']' && g.p1 == ']' && b == '>' {
			g.st = gText
			break
		}
		g.p0, g.p1 = g.p1, b
	case gDir:
		g.directiveByte(b, true)
	case gDirLT:
		if b == "!--"[g.n] {
			if g.n++; g.n == len("!--") {
				g.st, g.p0, g.p1 = gDirComment, 0, 0
			}
			break
		}
		g.depth++
		g.st = gDir
		g.directiveByte(b, false)
	case gDirComment:
		if g.p0 == '-' && g.p1 == '-' && b == '>' {
			g.st = gDir
			break
		}
		g.p0, g.p1 = g.p1, b
	}
	return false
}

// directiveByte is one byte of a directive, as the decoder reads it: quotes hide "<" and ">", an unquoted "<" opens
// a level unless it starts a comment, and the ">" at level 0 ends it. end is false for the byte that followed an
// unquoted "<" that did not start a comment, which the decoder reads again without the end test.
func (g *tagGuard) directiveByte(b byte, end bool) {
	if end && g.quote == 0 && b == '>' && g.depth == 0 {
		g.st = gText
		return
	}
	switch {
	case b == g.quote:
		g.quote = 0
	case g.quote != 0:
	case b == '\'' || b == '"':
		g.quote = b
	case b == '>':
		g.depth--
	case b == '<':
		g.st, g.n = gDirLT, 0
	}
}
