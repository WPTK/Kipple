package imgproxy

import (
	"bufio"
	"encoding/binary"
	"io"
)

// The WebP side of the decode-memory model (design §7.4, Limits).
//
// Lossy (VP8) frames are sized by their dimensions alone (the key frame's own
// size must be the one DecodeConfig reported). A lossless (VP8L) stream is
// not: golang.org/x/image/vp8l keeps one set of five Huffman trees per
// "prefix code group", a file with meta prefix codes may read up to 2,600 of
// them, and with an 11-bit color cache a kept group holds about 50 KB of trees
// and leaves about 25 KB of garbage, whatever the pixel count (about 200 MB
// for an 801x1000 picture with 600 KB of tree data); a group read and dropped
// still leaves about 12 KB. The walk below reads a VP8L stream (the image, or
// a compressed ALPH plane, which is one too) as far as the decoder's
// allocations are decided by it: the transforms and their sub-images, which
// are decoded symbol by symbol (their values are not kept), then the main
// image's color cache and meta flag. For meta prefix codes the entropy image
// is walked and, up to 65,536 tiles, decoded, so the groups read, kept and
// dropped are counted exactly as the decoder counts them; a larger entropy
// image is priced as 2,600 kept groups. The file is refused when the total is
// over the decode ceiling, so many-group files are refused and a few groups
// are not.
// Anything the walk cannot follow exactly is refused too: an incomplete or
// over-full Huffman code, a repeated transform, an invalid back-reference, a
// short stream. With complete codes every bit has one meaning, so the walk
// reads the stream the way the decoder does.

// webpDecodeCost is what golang.org/x/image/webp allocates to decode the w x h
// file in r, or false when the file is not one the model can account for.
func webpDecodeCost(r io.ReaderAt, size int64, w, h int) (int64, bool) {
	px := int64(w) * int64(h)
	mb := 256 * int64((w+15)/16) * int64((h+15)/16)
	lossy := mb*3/2 + mb/32 // YCbCr padded to macroblocks, plus per-macroblock filter state
	var hdr [12]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WEBP" {
		return 0, false
	}
	var ch [8]byte
	off := int64(12)
	chunk := func() (string, int64, bool) {
		if off+8 > size {
			return "", 0, false
		}
		if _, err := r.ReadAt(ch[:], off); err != nil {
			return "", 0, false
		}
		l := int64(binary.LittleEndian.Uint32(ch[4:8]))
		if off+8+l > size {
			return "", 0, false
		}
		return string(ch[0:4]), l, true
	}
	id, l, ok := chunk()
	if !ok {
		return 0, false
	}
	switch id {
	case "VP8 ":
		if !vp8FrameIs(r, off+8, l, w, h) {
			return 0, false
		}
		return lossy + l, true // the frame's partitions are read whole
	case "VP8L":
		return vp8lChunkCost(r, off+8, l, w, h)
	case "VP8X":
	default:
		return 0, false
	}
	var x [10]byte
	if l != 10 {
		return 0, false
	}
	if _, err := r.ReadAt(x[:], off+8); err != nil {
		return 0, false
	}
	wantAlpha := x[0]&0x10 != 0
	var alpha int64
	for i := 0; i < maxSegments; i++ {
		off += 8 + l + l&1
		if id, l, ok = chunk(); !ok {
			return 0, false
		}
		switch id {
		case "ALPH":
			if !wantAlpha || l < 1 {
				return 0, false // the decoder refuses an ALPH it did not ask for
			}
			wantAlpha = false
			var c [1]byte
			if _, err := r.ReadAt(c[:], off+8); err != nil {
				return 0, false
			}
			switch c[0] & 0x03 {
			case 0:
				alpha = px // the raw plane
			case 1:
				// A VP8L stream without its header, then the plane copied out of it.
				n, ok := vp8lStreamCost(io.NewSectionReader(r, off+9, l-1), w, h)
				if !ok {
					return 0, false
				}
				alpha = n + px
			default:
				return 0, false
			}
		case "VP8 ":
			if wantAlpha || !vp8FrameIs(r, off+8, l, w, h) {
				return 0, false
			}
			return lossy + l + alpha, true
		case "VP8L":
			if alpha != 0 {
				return 0, false // the decoder refuses an alpha plane with a lossless frame
			}
			return vp8lChunkCost(r, off+8, l, w, h)
		}
	}
	return 0, false
}

// vp8FrameIs reports whether the VP8 chunk at off (l bytes) starts with a key
// frame header of exactly w x h (RFC 6386 section 9.1: a 3-byte frame tag with
// the key frame bit clear, the start code 9d 01 2a, then 14-bit width and
// height, whose top two bits are scaling and ignored). The lossy cost is priced
// from w x h, which for a VP8X file is the canvas, not the frame the decoder
// sizes its buffers from, so a frame of any other size (or no key frame) is
// refused rather than trusted to the decoder's own check.
func vp8FrameIs(r io.ReaderAt, off, l int64, w, h int) bool {
	var b [10]byte
	if l < int64(len(b)) {
		return false
	}
	if _, err := r.ReadAt(b[:], off); err != nil {
		return false
	}
	if b[0]&1 != 0 || b[3] != 0x9d || b[4] != 0x01 || b[5] != 0x2a {
		return false
	}
	fw := int(b[7]&0x3f)<<8 | int(b[6])
	fh := int(b[9]&0x3f)<<8 | int(b[8])
	return fw == w && fh == h
}

// vp8lChunkCost checks a VP8L chunk's 5-byte header (magic, the size planThumb
// decoded, version 0) and walks the stream after it.
func vp8lChunkCost(r io.ReaderAt, off, l int64, w, h int) (int64, bool) {
	var b [5]byte
	if l < 5 {
		return 0, false
	}
	if _, err := r.ReadAt(b[:], off); err != nil || b[0] != 0x2f {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(b[1:5])
	if int(v&0x3fff)+1 != w || int(v>>14&0x3fff)+1 != h || v>>29 != 0 {
		return 0, false
	}
	return vp8lStreamCost(io.NewSectionReader(r, off+5, l-5), w, h)
}

// vp8lBits is the VP8L bit reader (least significant bit first). A read past
// the end sets bad, and the walk refuses.
type vp8lBits struct {
	r   *bufio.Reader
	acc uint64
	n   uint
	bad bool
}

func (b *vp8lBits) read(n uint) uint32 {
	for b.n < n {
		c, err := b.r.ReadByte()
		if err != nil {
			b.bad = true
			return 0
		}
		b.acc |= uint64(c) << b.n
		b.n += 8
	}
	v := uint32(b.acc & (1<<n - 1))
	b.acc >>= n
	b.n -= n
	return v
}

// vp8lHuff is a canonical Huffman code, or a code of one symbol (zero bits).
type vp8lHuff struct {
	single  bool
	sym     uint32
	simple2 [2]uint32 // a two-symbol simple code: bit 0 and bit 1
	simple  bool
	count   [16]int
	symbols []uint32
}

func (h *vp8lHuff) next(b *vp8lBits) (uint32, bool) {
	switch {
	case h.single:
		return h.sym, true
	case h.simple:
		return h.simple2[b.read(1)], !b.bad
	}
	code, first, index := 0, 0, 0
	for l := 1; l < len(h.count); l++ {
		code |= int(b.read(1))
		if b.bad {
			return 0, false
		}
		c := h.count[l]
		if code-c < first {
			return h.symbols[index+code-first], true
		}
		index += c
		first = (first + c) << 1
		code <<= 1
	}
	return 0, false
}

// buildHuff makes a code from code lengths (0 = unused). It accepts only a
// complete code, or a single used symbol (which the decoder, too, reads with
// zero bits whatever its length).
func buildHuff(lengths []uint8) (*vp8lHuff, bool) {
	h := &vp8lHuff{}
	used := 0
	for s, l := range lengths {
		if l > 15 {
			return nil, false
		}
		if l > 0 {
			used++
			h.count[l]++
			h.sym = uint32(s)
		}
	}
	switch used {
	case 0:
		return nil, false
	case 1:
		h.single = true
		return h, true
	}
	left := 1
	for l := 1; l < len(h.count); l++ {
		left = left<<1 - h.count[l]
		if left < 0 {
			return nil, false
		}
	}
	if left != 0 {
		return nil, false // incomplete: a code the stream may use but no symbol answers to
	}
	var offs [16]int // where each length's symbols start, in canonical order
	for l := 2; l < len(h.count); l++ {
		offs[l] = offs[l-1] + h.count[l-1]
	}
	h.symbols = make([]uint32, used)
	for s, l := range lengths {
		if l > 0 {
			h.symbols[offs[l]] = uint32(s)
			offs[l]++
		}
	}
	return h, true
}

var vp8lCodeLengthOrder = [19]uint8{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// readHuff reads one prefix code for an alphabet of n symbols (section 3.7.2).
func readHuff(b *vp8lBits, n int) (*vp8lHuff, bool) {
	if b.read(1) != 0 { // simple code
		two := b.read(1) == 1
		bits := uint(1)
		if b.read(1) == 1 {
			bits = 8
		}
		s0 := b.read(bits)
		if b.bad || int(s0) >= n {
			return nil, false
		}
		if !two {
			return &vp8lHuff{single: true, sym: s0}, true
		}
		s1 := b.read(8)
		if b.bad || int(s1) >= n {
			return nil, false
		}
		return &vp8lHuff{simple: true, simple2: [2]uint32{s0, s1}}, true
	}
	nc := int(b.read(4)) + 4
	if nc > len(vp8lCodeLengthOrder) {
		return nil, false
	}
	var cl [19]uint8
	for i := 0; i < nc; i++ {
		cl[vp8lCodeLengthOrder[i]] = uint8(b.read(3))
	}
	if b.bad {
		return nil, false
	}
	clh, ok := buildHuff(cl[:])
	if !ok {
		return nil, false
	}
	lengths := make([]uint8, n)
	maxSymbol := n
	if b.read(1) != 0 {
		ms := int(b.read(2+2*uint(b.read(3)))) + 2
		if ms > n {
			return nil, false
		}
		maxSymbol = ms
	}
	prev := uint8(8)
	for s := 0; s < n && maxSymbol > 0; maxSymbol-- {
		c, ok := clh.next(b)
		if !ok {
			return nil, false
		}
		if c < 16 {
			lengths[s] = uint8(c)
			s++
			if c != 0 {
				prev = uint8(c)
			}
			continue
		}
		var rep int
		var v uint8
		switch c {
		case 16:
			rep, v = 3+int(b.read(2)), prev
		case 17:
			rep = 3 + int(b.read(3))
		default:
			rep = 11 + int(b.read(7))
		}
		if b.bad || s+rep > n {
			return nil, false
		}
		for ; rep > 0; rep-- {
			lengths[s] = v
			s++
		}
	}
	if b.bad {
		return nil, false
	}
	return buildHuff(lengths)
}

const (
	vp8lLiterals = 256
	vp8lLengths  = 24
	vp8lDists    = 40
	// vp8lMaxGroups is the most prefix code groups libwebp writes and the
	// decoder accepts (an index of 2,600 or more is refused).
	vp8lMaxGroups = 2600
	// vp8lRemapFrom is the group index from which the decoder keeps only the
	// groups a tile uses (it does too when the index reaches the tile count).
	vp8lRemapFrom = 1000
	// vp8lTrackTiles is the largest entropy image whose values the walk keeps
	// (4 bytes per tile) to count the groups exactly; a larger one is priced
	// at vp8lMaxGroups kept groups.
	vp8lTrackTiles = 1 << 16
)

// vp8lSymbols is the number of symbols in a group's five alphabets.
func vp8lSymbols(ccBits uint32) int64 {
	n := int64(vp8lLiterals + vp8lLengths + 3*vp8lLiterals + vp8lDists)
	if ccBits > 0 {
		n += 1 << ccBits
	}
	return n
}

// vp8lGroupCost bounds what golang.org/x/image/vp8l allocates for one prefix
// code group it keeps, with a color cache of ccBits. The group itself is five
// hTrees (a 24-byte slice header and a 512-byte look-up table each). Per tree
// of an alphabet of n symbols, a normal code allocates the code lengths
// (decodeHuffmanTree, 4 bytes per symbol, garbage), the canonical codes
// (codeLengthsToCodes, 4 bytes per symbol, garbage) and the nodes (8 bytes
// each, 2k-1 of them for k used symbols, at most 16 bytes per symbol, kept):
// 24 bytes per symbol. The code-length code adds its own nodes and codes
// (about 400 bytes; its tree is on the stack), counted as 1 KiB per tree,
// and the color cache (allocated once per image, not per group) is counted
// again per group; those two over-counts absorb the allocator's size-class
// rounding (a 2,328-symbol node array rounds up by 3.7 KB), which
// TestVP8LWalkBoundsTheDecoderOnGroupHeavyFiles measures. A simple code
// allocates less.
func vp8lGroupCost(ccBits uint32) int64 {
	cc := int64(0)
	if ccBits > 0 {
		cc = 1 << ccBits
	}
	return 5*(24+512) + 24*vp8lSymbols(ccBits) + 5*1024 + 4*cc
}

// vp8lDroppedGroupCost bounds a group the decoder reads and drops (an index
// no tile uses, when it keeps only the used ones): the code lengths of each
// normal code (4 bytes per symbol, garbage) and the code-length code, but no
// tree.
func vp8lDroppedGroupCost(ccBits uint32) int64 {
	return 4*vp8lSymbols(ccBits) + 5*1024
}

// vp8lGroupsCost prices the groups the main image reads, given the largest
// group index its entropy image uses, how many distinct indexes it uses and
// its tile count, the way decodeHuffmanGroups decides: every group up to the
// largest index is read; below vp8lRemapFrom and the tile count all of them
// are kept, otherwise only the used ones (plus a 2-byte index map per group).
func vp8lGroupsCost(maxIdx, distinct, tiles int64, ccBits uint32) int64 {
	read := maxIdx + 1
	if maxIdx < vp8lRemapFrom && maxIdx < tiles {
		return read * vp8lGroupCost(ccBits)
	}
	return distinct*vp8lGroupCost(ccBits) + (read-distinct)*vp8lDroppedGroupCost(ccBits) + 2*read
}

// vp8lStreamCost walks a VP8L stream after its 5-byte header (w x h pixels):
// the transforms and their sub-images, then the main image's color cache and
// meta prefix codes. It returns the bytes the decoder allocates, or false.
func vp8lStreamCost(r io.Reader, w, h int) (int64, bool) {
	b := &vp8lBits{r: bufio.NewReaderSize(r, 4096)}
	var cost int64
	var seen [4]bool
	iw, ih := int64(w), int64(h)
	for b.read(1) != 0 {
		t := b.read(2)
		if b.bad || seen[t] {
			return 0, false // the decoder refuses a repeated transform
		}
		seen[t] = true
		switch t {
		case 0, 1: // predictor, cross-color: a sub-image of tiles
			bits := b.read(3) + 2
			c, ok := vp8lSubImage(b, nTiles(iw, bits), nTiles(ih, bits), 0, nil)
			if !ok {
				return 0, false
			}
			cost += c
		case 3: // color indexing: the palette, and pixels bundled 2, 4 or 8 to a word when it is small
			colors := int64(b.read(8)) + 1
			c, ok := vp8lSubImage(b, colors, 1, 4*256, nil)
			if !ok {
				return 0, false
			}
			cost += c
			var bits uint32
			switch {
			case colors <= 2:
				bits = 3
			case colors <= 4:
				bits = 2
			case colors <= 16:
				bits = 1
			}
			if bits > 0 {
				cost += 4 * iw * ih // the unpacked copy made by the inverse transform
				iw = nTiles(iw, bits)
			}
		}
		if b.bad {
			return 0, false
		}
	}
	ccBits := uint32(0)
	if b.read(1) != 0 {
		if ccBits = b.read(4); ccBits < 1 || ccBits > 11 {
			return 0, false
		}
	}
	groups := vp8lGroupCost(ccBits)
	if b.read(1) != 0 {
		// Meta prefix codes: an entropy image maps tiles to groups (its red and
		// green are the index). The decoder reads every group up to the largest
		// index, refuses an index of 2,600 or more, and keeps either all of
		// them or only the used ones (vp8lGroupsCost). The entropy image is a
		// sub-image, walked like the others; when it is small enough its
		// values are kept and the groups counted exactly. Otherwise 2,600 kept
		// groups bound it: read groups never exceed 2,600, and a kept group
		// costs more than a dropped one.
		bits := b.read(3) + 2
		tw, th := nTiles(iw, bits), nTiles(ih, bits)
		tiles := tw * th
		var keep []uint32
		if tiles <= vp8lTrackTiles {
			keep = make([]uint32, tiles)
		}
		c, ok := vp8lSubImage(b, tw, th, 0, keep)
		if !ok {
			return 0, false
		}
		cost += c
		if keep == nil {
			groups = vp8lMaxGroups*vp8lGroupCost(ccBits) + 2*vp8lMaxGroups
		} else {
			var used [vp8lMaxGroups]bool
			maxIdx, distinct := int64(0), int64(0)
			for _, argb := range keep {
				i := int64(argb >> 8 & 0xffff)
				if i >= vp8lMaxGroups {
					return 0, false // the decoder refuses it too
				}
				if !used[i] {
					used[i] = true
					distinct++
				}
				maxIdx = max(maxIdx, i)
			}
			groups = vp8lGroupsCost(maxIdx, distinct, tiles, ccBits)
		}
	}
	if b.bad {
		return 0, false
	}
	return cost + 4*iw*ih + groups, true
}

func nTiles(size int64, bits uint32) int64 { return (size + 1<<bits - 1) >> bits }

// vp8lSubImage walks an entropy-coded sub-image (no meta codes) of w x h
// pixels symbol by symbol and returns what the decoder allocates for it. With
// keep (w*h long) it also decodes the pixels into it as ARGB, the way
// decodePix does: literals, back-references and color cache lookups.
func vp8lSubImage(b *vp8lBits, w, h, minCap int64, keep []uint32) (int64, bool) {
	ccBits := uint32(0)
	if b.read(1) != 0 {
		if ccBits = b.read(4); ccBits < 1 || ccBits > 11 {
			return 0, false
		}
	}
	green := vp8lLiterals + vp8lLengths
	if ccBits > 0 {
		green += 1 << ccBits
	}
	var g [5]*vp8lHuff
	for i, n := range [5]int{green, vp8lLiterals, vp8lLiterals, vp8lLiterals, vp8lDists} {
		var ok bool
		if g[i], ok = readHuff(b, n); !ok {
			return 0, false
		}
	}
	total := w * h
	if keep != nil && int64(len(keep)) != total {
		return 0, false
	}
	var cache []uint32
	cached := int64(0)
	if keep != nil && ccBits > 0 {
		cache = make([]uint32, 1<<ccBits)
	}
	for p := int64(0); p < total; {
		s, ok := g[0].next(b)
		if !ok {
			return 0, false
		}
		switch {
		case s < vp8lLiterals:
			var c [3]uint32 // red, blue, alpha
			for i, t := range g[1:4] {
				if c[i], ok = t.next(b); !ok {
					return 0, false
				}
			}
			if keep != nil {
				keep[p] = c[2]<<24 | c[0]<<16 | s<<8 | c[1]
			}
			p++
		case s < vp8lLiterals+vp8lLengths:
			length := int64(lz77Param(b, s-vp8lLiterals))
			ds, ok := g[4].next(b)
			if !ok {
				return 0, false
			}
			dist := vp8lDistance(w, lz77Param(b, ds))
			if b.bad || p-dist < 0 || p+length > total {
				return 0, false // the decoder refuses it too
			}
			if keep != nil {
				for i := p; i < p+length; i++ { // forward, so an overlapping copy repeats
					keep[i] = keep[i-dist]
				}
			}
			p += length
		default:
			// A color cache index (always in range: the alphabet is sized by
			// the cache). The decoder inserts every earlier pixel first.
			if keep != nil {
				for ; cached < p; cached++ {
					cache[keep[cached]*0x1e35a7bd>>(32-ccBits)] = keep[cached]
				}
				keep[p] = cache[s-vp8lLiterals-vp8lLengths]
			}
			p++
		}
	}
	pix := max(4*total, minCap)
	cc := int64(0)
	if ccBits > 0 {
		cc = 4 << ccBits
	}
	return pix + cc + vp8lGroupCost(ccBits), !b.bad
}

// lz77Param is a length or distance from its prefix symbol and extra bits (section 5.2.2).
func lz77Param(b *vp8lBits, sym uint32) uint32 {
	if sym < 4 {
		return sym + 1
	}
	extra := (sym - 2) >> 1
	offset := (2 + sym&1) << extra
	return offset + b.read(uint(extra)) + 1
}

// vp8lDistanceMap is the distance code table of section 5.2.2 (dy<<4 | 8-dx).
var vp8lDistanceMap = [120]uint8{
	0x18, 0x07, 0x17, 0x19, 0x28, 0x06, 0x27, 0x29, 0x16, 0x1a,
	0x26, 0x2a, 0x38, 0x05, 0x37, 0x39, 0x15, 0x1b, 0x36, 0x3a,
	0x25, 0x2b, 0x48, 0x04, 0x47, 0x49, 0x14, 0x1c, 0x35, 0x3b,
	0x46, 0x4a, 0x24, 0x2c, 0x58, 0x45, 0x4b, 0x34, 0x3c, 0x03,
	0x57, 0x59, 0x13, 0x1d, 0x56, 0x5a, 0x23, 0x2d, 0x44, 0x4c,
	0x55, 0x5b, 0x33, 0x3d, 0x68, 0x02, 0x67, 0x69, 0x12, 0x1e,
	0x66, 0x6a, 0x22, 0x2e, 0x54, 0x5c, 0x43, 0x4d, 0x65, 0x6b,
	0x32, 0x3e, 0x78, 0x01, 0x77, 0x79, 0x53, 0x5d, 0x11, 0x1f,
	0x64, 0x6c, 0x42, 0x4e, 0x76, 0x7a, 0x21, 0x2f, 0x75, 0x7b,
	0x31, 0x3f, 0x63, 0x6d, 0x52, 0x5e, 0x00, 0x74, 0x7c, 0x41,
	0x4f, 0x10, 0x20, 0x62, 0x6e, 0x30, 0x73, 0x7d, 0x51, 0x5f,
	0x40, 0x72, 0x7e, 0x61, 0x6f, 0x50, 0x71, 0x7f, 0x60, 0x70,
}

// vp8lDistance turns a distance code into a pixel distance for a picture w wide.
func vp8lDistance(w int64, code uint32) int64 {
	if code > uint32(len(vp8lDistanceMap)) {
		return int64(code) - int64(len(vp8lDistanceMap))
	}
	d := int64(vp8lDistanceMap[code-1])
	if v := (d>>4)*w + 8 - d&0xf; v >= 1 {
		return v
	}
	return 1
}
