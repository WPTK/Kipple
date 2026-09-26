package imgproxy

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/image/vp8l"
)

// ---- synthetic worst-case images ----
//
// The decoders size their buffers from the headers alone, so a file whose
// every block is empty allocates exactly what a real photograph of the same
// shape does, and it is cheap to build at 24 megapixels.

type jpegSpec struct {
	w, h        int
	samp        [][2]int // per component: horizontal, vertical sampling
	progressive bool
	adobe       int // the APP14 transform byte, or -1 for no Adobe segment
	jfif        bool
	rgbIDs      bool // component ids 'R', 'G', 'B'
	pad         int  // bytes of COM padding (to clear the 150 KB floor)
	// adobeAfterScan puts the Adobe segment after the scan data instead of in
	// front of the frame (image/jpeg still honors it there).
	adobeAfterScan bool
	// restart adds a DRI segment (one MCU row per interval) and an RSTn
	// marker after every row of scan data.
	restart bool
}

// synthJPEG is a valid JPEG whose every 8x8 block is flat: one Huffman code
// of length 1 per table, so each block costs 2 bits (baseline: DC 0 and EOB)
// or 1 bit (a progressive file has a single interleaved DC scan, which is
// enough for the decoder to allocate every component's coefficients).
func synthJPEG(s jpegSpec) []byte {
	var b bytes.Buffer
	seg := func(m byte, p []byte) {
		b.Write([]byte{0xFF, m, byte((len(p) + 2) >> 8), byte(len(p) + 2)})
		b.Write(p)
	}
	adobe := func() { seg(0xEE, append([]byte("Adobe"), 0, 100, 0, 0, 0, 0, byte(s.adobe))) }
	b.Write([]byte{0xFF, 0xD8})
	if s.jfif {
		seg(0xE0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	}
	if s.adobe >= 0 && !s.adobeAfterScan {
		adobe()
	}
	for pad := s.pad; pad > 0; {
		n := min(pad, 65000)
		seg(0xFE, make([]byte, n))
		pad -= n
	}
	q := make([]byte, 65)
	for i := 1; i < len(q); i++ {
		q[i] = 1
	}
	seg(0xDB, q)
	n := len(s.samp)
	sof := []byte{8, byte(s.h >> 8), byte(s.h), byte(s.w >> 8), byte(s.w), byte(n)}
	maxH, maxV := 1, 1
	for i, hv := range s.samp {
		id := byte(i + 1)
		if s.rgbIDs {
			id = "RGB"[i]
		}
		sof = append(sof, id, byte(hv[0]<<4|hv[1]), 0)
		maxH, maxV = max(maxH, hv[0]), max(maxV, hv[1])
	}
	sofMarker := byte(0xC0)
	if s.progressive {
		sofMarker = 0xC2
	}
	seg(sofMarker, sof)
	dht := func(class byte) []byte {
		p := []byte{class << 4}
		counts := make([]byte, 16)
		counts[0] = 1
		return append(append(p, counts...), 0)
	}
	seg(0xC4, dht(0))
	seg(0xC4, dht(1))
	var mxx, myy, per int
	if n == 1 {
		mxx, myy, per = (s.w+7)/8, (s.h+7)/8, 1
	} else {
		mxx, myy = (s.w+8*maxH-1)/(8*maxH), (s.h+8*maxV-1)/(8*maxV)
		for _, hv := range s.samp {
			per += hv[0] * hv[1]
		}
	}
	if s.restart {
		seg(0xDD, []byte{byte(mxx >> 8), byte(mxx)})
	}
	sos := []byte{byte(n)}
	for i := range s.samp {
		sos = append(sos, sof[6+3*i], 0x00)
	}
	bits := 2
	if s.progressive {
		sos = append(sos, 0, 0, 0)
		bits = 1
	} else {
		sos = append(sos, 0, 63, 0)
	}
	seg(0xDA, sos)
	if s.restart {
		row := (mxx*per*bits + 7) / 8
		for y := 0; y < myy; y++ {
			b.Write(make([]byte, row+2))
			if y < myy-1 {
				b.Write([]byte{0xFF, 0xD0 + byte(y%8)})
			}
		}
		b.Write(make([]byte, 64))
	} else {
		b.Write(make([]byte, mxx*myy*per*bits/8+64))
	}
	if s.adobe >= 0 && s.adobeAfterScan {
		adobe()
	}
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

func pngChunk(kind string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(kind)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(kind))
	crc.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// synthPNG is a valid all-zero PNG (colorType ct at the given depth,
// optionally Adam7-interlaced and with a tRNS chunk).
func synthPNG(t testing.TB, w, h int, depth, ct byte, interlaced, trns bool, pad int) []byte {
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[ct]
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = depth, ct
	if interlaced {
		ihdr[12] = 1
	}
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	require.NoError(t, err)
	rows := func(pw, ph int) {
		if pw == 0 || ph == 0 {
			return
		}
		row := make([]byte, 1+(pw*channels*int(depth)+7)/8)
		for y := 0; y < ph; y++ {
			_, _ = zw.Write(row)
		}
	}
	if interlaced {
		for _, p := range [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
			rows((w-p[0]+p[2]-1)/p[2], (h-p[1]+p[3]-1)/p[3])
		}
	} else {
		rows(w, h)
	}
	require.NoError(t, zw.Close())
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, pngChunk("IHDR", ihdr)...)
	if ct == 3 {
		out = append(out, pngChunk("PLTE", make([]byte, 3*16))...)
	}
	if trns {
		switch ct {
		case 0:
			out = append(out, pngChunk("tRNS", make([]byte, 2))...)
		case 2:
			out = append(out, pngChunk("tRNS", make([]byte, 6))...)
		}
	}
	if pad > 0 {
		out = append(out, pngChunk("tEXt", append([]byte("pad\x00"), make([]byte, pad)...))...)
	}
	out = append(out, pngChunk("IDAT", z.Bytes())...)
	return append(out, pngChunk("IEND", nil)...)
}

// ---- VP8L (lossless WebP) encoder for tests ----

// bitWriter writes a VP8L (LSB-first) bit stream.
type bitWriter struct {
	b   []byte
	acc uint64
	n   uint
}

func (w *bitWriter) put(v uint32, n uint) {
	w.acc |= uint64(v) << w.n
	w.n += n
	for w.n >= 8 {
		w.b = append(w.b, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}

func (w *bitWriter) bytes() []byte {
	if w.n > 0 {
		w.b = append(w.b, byte(w.acc))
	}
	return append(w.b, make([]byte, 16)...)
}

// prefixCode is a canonical prefix code: the code and its length per symbol.
type prefixCode struct{ code, len []uint32 }

func canonicalCode(lengths []uint8) prefixCode {
	var count, next [17]uint32
	for _, l := range lengths {
		if l > 0 {
			count[l]++
		}
	}
	for l := 1; l < 16; l++ {
		next[l+1] = (next[l] + count[l]) << 1
	}
	c := prefixCode{code: make([]uint32, len(lengths)), len: make([]uint32, len(lengths))}
	for s, l := range lengths {
		if l > 0 {
			c.code[s], c.len[s] = next[l], uint32(l)
			next[l]++
		}
	}
	return c
}

// emit writes the code of sym, its most significant bit first.
func (w *bitWriter) emit(c prefixCode, sym int) {
	for i := int(c.len[sym]) - 1; i >= 0; i-- {
		w.put(c.code[sym]>>uint(i)&1, 1)
	}
}

// completeLengths is a complete code over n >= 2 symbols: lengths L-1 and L.
func completeLengths(n int) []uint8 {
	l := 1
	for 1<<l < n {
		l++
	}
	short := 1<<l - n
	out := make([]uint8, n)
	for i := range out {
		out[i] = uint8(l)
		if i < short {
			out[i] = uint8(l - 1)
		}
	}
	return out
}

// simpleTree is a one-symbol code: zero bits per use.
func (w *bitWriter) simpleTree(sym uint32) {
	w.put(1, 1) // simple
	w.put(0, 1) // one symbol
	if sym < 2 {
		w.put(0, 1)
		w.put(sym, 1)
	} else {
		w.put(1, 1)
		w.put(sym, 8)
	}
}

var codeLengthOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// normalTree writes a normal prefix code with the given lengths (which use
// at most two distinct non-zero values, as completeLengths makes), the runs
// coded with symbol 16 (repeat the previous length 3 to 6 times).
func (w *bitWriter) normalTree(lengths []uint8) prefixCode {
	w.put(0, 1)
	lo, hi := lengths[0], lengths[len(lengths)-1]
	var cl [19]uint8
	cl[16] = 1
	if lo == hi {
		cl[lo] = 1
	} else {
		cl[lo], cl[hi] = 2, 2
	}
	clc := canonicalCode(cl[:])
	nc := 4
	for i, s := range codeLengthOrder {
		if cl[s] > 0 {
			nc = max(nc, i+1)
		}
	}
	w.put(uint32(nc-4), 4)
	for i := 0; i < nc; i++ {
		w.put(uint32(cl[codeLengthOrder[i]]), 3)
	}
	w.put(0, 1) // the lengths run to the end of the alphabet
	for i := 0; i < len(lengths); {
		v := lengths[i]
		j := i
		for j < len(lengths) && lengths[j] == v {
			j++
		}
		w.emit(clc, int(v))
		for run := j - i - 1; run > 0; {
			if run < 3 {
				w.emit(clc, int(v))
				run--
				continue
			}
			c := min(6, run)
			if r := run - c; r == 1 || r == 2 {
				c = run - 3
			}
			w.emit(clc, 16)
			w.put(uint32(c-3), 2)
			run -= c
		}
		i = j
	}
	return canonicalCode(lengths)
}

// lz77Code is the prefix symbol and extra bits of a length or distance value.
func lz77Code(v int) (sym int, extra uint, bits uint32) {
	if v <= 4 {
		return v - 1, 0, 0
	}
	for s := 4; s < 40; s++ {
		e := uint(s-2) >> 1
		off := (2 + s&1) << e
		if v-1 >= off && v-1 < off+1<<e {
			return s, e, uint32(v - 1 - off)
		}
	}
	panic("lz77Code: value too large")
}

type vp8lSpec struct {
	w, h       int
	palette    bool // a two-color color-indexing transform: 8 pixels bundled to a word, unpacked by the decoder
	predictor  bool // a predictor transform with 4x4 tiles (the largest sub-image)
	crossColor bool // a cross-color transform with 4x4 tiles
	subGreen   bool
	cacheBits  int  // the main image's color cache (0: none)
	fullTrees  bool // the main group's codes cover their whole alphabets (the most tree memory)
	groups     int  // above 1: meta prefix codes with this many groups
	sparse     bool // with groups: every tile uses the last group, so the others are decoded and dropped
}

// synthVP8L is a valid VP8L stream of transparent black pixels, with the
// 5-byte header when header is set (an ALPH plane has none). Sub-images use
// one-symbol codes (zero bits per pixel); with full trees the main image is
// one literal pixel and back-references of up to 4,096 pixels.
func synthVP8L(s vp8lSpec, header bool) []byte {
	var bw bitWriter
	if header {
		bw.put(0x2f, 8)
		bw.put(uint32(s.w-1), 14)
		bw.put(uint32(s.h-1), 14)
		bw.put(0, 1)
		bw.put(0, 3)
	}
	tiles := func(n, bits int) int { return (n + 1<<bits - 1) >> bits }
	zeroSub := func() {
		bw.put(0, 1) // no color cache
		for i := 0; i < 5; i++ {
			bw.simpleTree(0)
		}
	}
	iw := s.w
	if s.predictor {
		bw.put(1, 1)
		bw.put(0, 2)
		bw.put(0, 3) // 4x4 tiles
		zeroSub()
	}
	if s.crossColor {
		bw.put(1, 1)
		bw.put(1, 2)
		bw.put(0, 3)
		zeroSub()
	}
	if s.subGreen {
		bw.put(1, 1)
		bw.put(2, 2)
	}
	if s.palette {
		bw.put(1, 1)
		bw.put(3, 2)
		bw.put(1, 8) // two colors
		zeroSub()
		iw = tiles(iw, 3)
	}
	bw.put(0, 1) // no more transforms
	if s.cacheBits > 0 {
		bw.put(1, 1)
		bw.put(uint32(s.cacheBits), 4)
	} else {
		bw.put(0, 1)
	}
	groups := max(1, s.groups)
	if groups > 1 {
		const hBits = 4
		bw.put(1, 1)
		bw.put(hBits-2, 3)
		bw.put(0, 1) // the meta image: no color cache
		g := bw.normalTree(completeLengths(vp8lLiterals + vp8lLengths))
		red := bw.normalTree(completeLengths(vp8lLiterals))
		bw.simpleTree(0)
		bw.simpleTree(0)
		bw.simpleTree(0)
		for t := 0; t < tiles(iw, hBits)*tiles(s.h, hBits); t++ {
			idx := t % groups // every group referenced
			if s.sparse {
				idx = groups - 1
			}
			bw.emit(g, idx&0xff)
			bw.emit(red, idx>>8)
		}
	} else {
		bw.put(0, 1)
	}
	green := vp8lLiterals + vp8lLengths
	if s.cacheBits > 0 {
		green += 1 << s.cacheBits
	}
	full := s.fullTrees || groups > 1
	var codes [5]prefixCode
	for i := 0; i < groups; i++ { // identical groups: the pixels read the same whichever group a tile uses
		if !full {
			for j := 0; j < 5; j++ {
				bw.simpleTree(0)
			}
			continue
		}
		for j, n := range [5]int{green, vp8lLiterals, vp8lLiterals, vp8lLiterals, vp8lDists} {
			codes[j] = bw.normalTree(completeLengths(n))
		}
	}
	if full {
		for j := 0; j < 4; j++ {
			bw.emit(codes[j], 0) // one literal pixel
		}
		for left := iw*s.h - 1; left > 0; {
			n := min(left, 4096)
			sym, extra, bits := lz77Code(n)
			bw.emit(codes[0], vp8lLiterals+sym)
			bw.put(bits, extra)
			bw.emit(codes[4], 1) // distance code 2: the pixel to the left
			left -= n
		}
	}
	return bw.bytes()
}

func webpChunk(id string, data []byte) []byte {
	out := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(data)))
	out = append(out, data...)
	if len(data)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func riffWebP(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := []byte("RIFF\x00\x00\x00\x00")
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

// synthWebPLossless is a VP8L WebP of transparent black pixels. With a
// two-color palette the decoder also allocates the unpacked copy.
func synthWebPLossless(w, h int, palette bool) []byte {
	return riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: w, h: h, palette: palette}, true)))
}

// synthVP8 is a lossy key frame whose partitions are all zero: every
// macroblock decodes (as B_PRED with DC sub-blocks and no coefficients).
func synthVP8(w, h int) []byte {
	mbs := ((w + 15) / 16) * ((h + 15) / 16)
	first := 2*mbs + 1024
	b := []byte{byte(0x10 | (first&7)<<5), byte(first >> 3), byte(first >> 11), 0x9d, 0x01, 0x2a,
		byte(w), byte(w>>8) & 0x3f, byte(h), byte(h>>8) & 0x3f}
	b = append(b, make([]byte, first)...)
	return append(b, make([]byte, 4*mbs+1024)...)
}

// synthWebPAlpha is a VP8X WebP: an ALPH plane (compressed as VP8L when
// alpha is set, raw otherwise) and a lossy frame.
func synthWebPAlpha(w, h int, alpha *vp8lSpec) []byte {
	x := make([]byte, 10)
	x[0] = 0x10
	x[4], x[5], x[6] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	x[7], x[8], x[9] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	var a []byte
	if alpha != nil {
		a = append([]byte{1}, synthVP8L(*alpha, false)...)
	} else {
		a = append([]byte{0}, make([]byte, w*h)...)
	}
	return riffWebP(webpChunk("VP8X", x), webpChunk("ALPH", a), webpChunk("VP8 ", synthVP8(w, h)))
}

// ---- measuring ----

// allocDuring is every byte f allocates (runtime TotalAlloc, garbage
// included): an upper bound of how far f can push the heap, whatever the GC
// does meanwhile. peak is the highest HeapAlloc a 1 ms sampler saw (it
// includes garbage not yet collected, so it is informational).
func allocDuring(f func()) (total, peak int64) {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var hi atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > hi.Load() {
				hi.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	f()
	close(stop)
	wg.Wait()
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	// The sampler's own ReadMemStats calls allocate nothing measurable.
	return int64(end.TotalAlloc - base.TotalAlloc), int64(hi.Load()) - int64(base.HeapAlloc)
}

// allocOf is every byte f allocates, without the sampler (small decodes).
func allocOf(f func()) int64 {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return int64(b.TotalAlloc - a.TotalAlloc)
}

func skipMemoryTests(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("the race detector changes allocations; memory bounds are checked by the plain test run")
	}
	if testing.Short() {
		t.Skip("decodes images of up to 24 megapixels")
	}
}

type memCase struct {
	name string
	ct   string
	data []byte
}

func memCases(t *testing.T) []memCase {
	rgb := [][2]int{{1, 1}, {1, 1}, {1, 1}}
	cmyk := [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}
	allTransforms := &vp8lSpec{w: 4000, h: 4000, predictor: true, crossColor: true, subGreen: true, palette: true, cacheBits: 11, fullTrees: true}
	return []memCase{
		{"jpeg baseline 4:2:0 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, adobe: -1, jfif: true})},
		{"jpeg baseline 4:4:4 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: rgb, adobe: -1, jfif: true})},
		{"jpeg baseline 4:1:1 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{4, 1}, {1, 1}, {1, 1}}, adobe: -1, jfif: true})},
		{"jpeg baseline flex 4:2:2/4:4:0 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{2, 2}, {2, 1}, {1, 2}}, adobe: -1, jfif: true})},
		{"jpeg baseline CMYK 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: cmyk, adobe: 0})},
		{"jpeg baseline YCCK 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}, {2, 2}}, adobe: 2})},
		{"jpeg baseline Adobe RGB 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: rgb, adobe: 0})},
		{"jpeg baseline gray 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{1, 1}}, adobe: -1, jfif: true})},
		{"jpeg baseline 4:2:0 restart intervals 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, adobe: -1, jfif: true, restart: true})},
		// Review item 2: an Adobe APP14 after the scan still makes the decoder convert to RGB.
		{"jpeg baseline 4:4:4 Adobe RGB after the scan 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4000, h: 3000, samp: rgb, adobe: 0, adobeAfterScan: true})},
		{"jpeg progressive 4:4:4 Adobe RGB after the scan 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4000, h: 3000, samp: rgb, progressive: true, adobe: 0, adobeAfterScan: true})},
		{"jpeg progressive 4:4:4 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: rgb, progressive: true, adobe: -1, jfif: true})},
		{"jpeg progressive CMYK 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: cmyk, progressive: true, adobe: 0})},
		{"jpeg progressive 4:2:0 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, progressive: true, adobe: -1, jfif: true})},
		{"png 16-bit RGBA Adam7 12 MP", "image/png", synthPNG(t, 4240, 2832, 16, 6, true, false, 0)},
		{"png 16-bit gray+tRNS Adam7 8 MP", "image/png", synthPNG(t, 3464, 2310, 16, 0, true, true, 0)},
		{"png 8-bit RGB 24 MP", "image/png", synthPNG(t, 6000, 4000, 8, 2, false, false, 0)},
		{"png paletted 24 MP", "image/png", synthPNG(t, 6000, 4000, 4, 3, false, false, 0)},
		{"webp lossless 16 MP", "image/webp", synthWebPLossless(4900, 3266, false)},
		{"webp lossless palette 16 MP", "image/webp", synthWebPLossless(4900, 3266, true)},
		{"webp lossless all transforms, 11-bit cache, full trees 16 MP", "image/webp", riffWebP(webpChunk("VP8L", synthVP8L(*allTransforms, true)))},
		{"webp lossless predictor+cross-color, 11-bit cache, full trees 16 MP", "image/webp", riffWebP(webpChunk("VP8L",
			synthVP8L(vp8lSpec{w: 4000, h: 4000, predictor: true, crossColor: true, cacheBits: 11, fullTrees: true}, true)))},
		{"webp lossless meta codes 40 groups, 11-bit cache 16 MP", "image/webp", riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: 4000, h: 4000, cacheBits: 11, groups: 40}, true)))},
		{"webp lossless meta codes 3 groups + palette 16 MP", "image/webp", riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: 4000, h: 4000, palette: true, groups: 3}, true)))},
		{"webp lossless meta codes 300 groups 1200x900", "image/webp", riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: 1200, h: 900, cacheBits: 11, groups: 300}, true)))},
		{"webp lossy + VP8L alpha with 8 groups 16 MP", "image/webp", synthWebPAlpha(4000, 4000, &vp8lSpec{w: 4000, h: 4000, palette: true, groups: 8})},
		{"webp lossy 24 MP", "image/webp", riffWebP(webpChunk("VP8 ", synthVP8(6000, 4000)))},
		{"webp lossy + raw alpha 16 MP", "image/webp", synthWebPAlpha(4000, 4000, nil)},
		{"webp lossy + VP8L alpha (all transforms, full trees) 16 MP", "image/webp", synthWebPAlpha(4000, 4000, allTransforms)},
	}
}

// TestThumbCostBoundsRealAllocation decodes worst-case files of every shape
// the decoders treat differently and checks that thumbCost is an upper bound
// of everything the transcode really allocates. It is not scaled down: a
// bound that holds at 24 MP holds below it.
func TestThumbCostBoundsRealAllocation(t *testing.T) {
	skipMemoryTests(t)
	for _, tc := range memCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(tc.data)
			p, err := planThumb(r, int64(len(tc.data))+thumbMinSource, tc.ct, ThumbWidth, 1<<30)
			require.NoError(t, err)
			var rerr error
			total, peak := allocDuring(func() { _, _, rerr = render(r, int64(len(tc.data)), p) })
			if rerr != nil {
				var pe *passError
				require.ErrorAs(t, rerr, &pe)
				require.NotEqual(t, "decode failed", pe.reason, "the synthetic file must decode")
			}
			t.Logf("%dx%d: allocated %.1f MiB (heap peak sample +%.1f MiB), estimate %.1f MiB (%.2fx), ceiling %d MiB",
				p.w, p.h, mib(total), mib(peak), mib(p.need), float64(p.need)/float64(total), defaultDecodeCeiling>>20)
			require.LessOrEqual(t, total, p.need, "the estimate is an upper bound of the real allocation")
			// With meta prefix codes every group is priced at full trees for its
			// alphabets, so those estimates may exceed the real allocation by more.
			if !strings.Contains(tc.name, "meta codes") && !strings.Contains(tc.name, "alpha with 8 groups") {
				require.Less(t, p.need, 3*total, "and not so loose that it refuses what would fit")
			}
		})
	}
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }

// TestThumbWorstAdmittedStaysUnderCeiling finds, for the costliest shapes,
// the largest picture the ceiling admits, transcodes it through the real
// limits and checks the allocation stays under the ceiling; one step larger is
// refused (and served as the original).
func TestThumbWorstAdmittedStaysUnderCeiling(t *testing.T) {
	skipMemoryTests(t)
	cmyk := [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}
	rgb := [][2]int{{1, 1}, {1, 1}, {1, 1}}
	kinds := []struct {
		name string
		ct   string
		make func(w, h int) []byte
	}{
		{"jpeg progressive 4:4:4", "image/jpeg", func(w, h int) []byte {
			return synthJPEG(jpegSpec{w: w, h: h, samp: rgb, progressive: true, adobe: -1, jfif: true, pad: thumbMinSource})
		}},
		{"jpeg progressive CMYK", "image/jpeg", func(w, h int) []byte {
			return synthJPEG(jpegSpec{w: w, h: h, samp: cmyk, progressive: true, adobe: 0, pad: thumbMinSource})
		}},
		{"jpeg baseline CMYK", "image/jpeg", func(w, h int) []byte {
			return synthJPEG(jpegSpec{w: w, h: h, samp: cmyk, adobe: 0, pad: thumbMinSource})
		}},
		{"png 16-bit RGBA Adam7", "image/png", func(w, h int) []byte {
			return synthPNG(t, w, h, 16, 6, true, false, thumbMinSource)
		}},
		// Lossless WebP: the padding chunk after the image is never read by the decoder.
		{"webp lossless all transforms, 11-bit cache, full trees", "image/webp", func(w, h int) []byte {
			s := vp8lSpec{w: w, h: h, predictor: true, crossColor: true, subGreen: true, palette: true, cacheBits: 11, fullTrees: true}
			return riffWebP(webpChunk("VP8L", synthVP8L(s, true)), webpChunk("PADD", make([]byte, thumbMinSource)))
		}},
		{"webp lossless meta codes 40 groups, 11-bit cache", "image/webp", func(w, h int) []byte {
			s := vp8lSpec{w: w, h: h, cacheBits: 11, groups: 40}
			return riffWebP(webpChunk("VP8L", synthVP8L(s, true)), webpChunk("PADD", make([]byte, thumbMinSource)))
		}},
	}
	lim := testLimits()
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			need := func(w int) int64 {
				data := k.make(w, w*2/3)
				p, err := planThumb(bytes.NewReader(data), int64(len(data)), k.ct, ThumbWidth, defaultThumbPixels)
				require.NoError(t, err)
				return p.need
			}
			// The widest 3:2 picture (in steps of 16 px) the ceiling admits.
			lo, hi := 1600, 6000
			for hi-lo > 16 {
				mid := (lo + hi) / 2 / 16 * 16
				if need(mid) <= lim.ceiling {
					lo = mid
				} else {
					hi = mid
				}
			}
			data := k.make(lo, lo*2/3)
			var out []byte
			var err error
			total, _ := allocDuring(func() { out, _, err = transcode(bytes.NewReader(data), int64(len(data)), k.ct, lim) })
			require.NoError(t, err, "admitted at %dx%d", lo, lo*2/3)
			cfg, _ := decodeCfg(t, out)
			require.Equal(t, ThumbWidth, cfg.Width)
			t.Logf("largest admitted %dx%d (%.1f MP): estimate %.1f MiB, allocated %.1f MiB", lo, lo*2/3, float64(lo*(lo*2/3))/1e6, mib(need(lo)), mib(total))
			require.LessOrEqual(t, total, int64(defaultDecodeCeiling), "the worst admitted transcode stays under the ceiling")

			big := k.make(hi, hi*2/3)
			_, _, err = transcode(bytes.NewReader(big), int64(len(big)), k.ct, lim)
			var pe *passError
			require.ErrorAs(t, err, &pe)
			require.Contains(t, pe.reason, "MiB", "one step larger is refused before decoding")
		})
	}
}

// TestThumbRefuses24MPCMYK is the crash-loop case from the review: a 24 MP
// CMYK JPEG was estimated at 72 MiB and admitted, but needs about 230 MB.
func TestThumbRefuses24MPCMYK(t *testing.T) {
	data := synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, adobe: 0})
	var pe *passError
	var err error
	total, _ := allocDuring(func() { _, _, err = transcode(bytes.NewReader(data), int64(len(data)), "image/jpeg", testLimits()) })
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.reason, "MiB")
	if !raceEnabled {
		require.Less(t, total, int64(8<<20), "refused from the headers, before any decode")
	}
	p, err := planThumb(bytes.NewReader(data), int64(len(data)), "image/jpeg", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	require.Greater(t, p.need, int64(200<<20), fmt.Sprintf("estimate %d MiB", p.need>>20))
}

func TestParseJPEGReadsFrameAndMarkers(t *testing.T) {
	data := synthJPEG(jpegSpec{w: 100, h: 60, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}, {2, 2}}, progressive: true, adobe: 2, pad: 70000})
	f, ok := parseJPEG(bytes.NewReader(data), int64(len(data)))
	require.True(t, ok, "found past a 70 KB segment")
	require.True(t, f.progressive)
	require.Equal(t, 4, f.n)
	require.Equal(t, [4]int{2, 1, 1, 2}, f.hs)
	require.Equal(t, 100, f.w)
	require.Equal(t, 60, f.h)
	require.True(t, f.adobe)
	require.EqualValues(t, 2, f.transform)
	require.False(t, f.jfif)

	_, ok = parseJPEG(bytes.NewReader([]byte("\xff\xd8\xff\xe0\x00\x10JFIF")), 12)
	require.False(t, ok, "no frame header: refused, not guessed")
	_, ok = parseJPEG(bytes.NewReader(data[:len(data)-2]), int64(len(data)-2))
	require.False(t, ok, "no EOI: refused")

	// Restart markers inside scan data are part of it; a trailer after EOI is never read.
	rst := synthJPEG(jpegSpec{w: 200, h: 120, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, adobe: -1, jfif: true, restart: true})
	rst = append(rst, "trailing bytes the decoder never reads"...)
	f, ok = parseJPEG(bytes.NewReader(rst), int64(len(rst)))
	require.True(t, ok)
	require.Equal(t, 200, f.w)
}

// ---- review item 1: the "FF 00" frame substitution ----

// craftedFF00JPEG is the reviewer's file: SOI, a stray "FF 00 00 06" (the old
// header walk took it for a segment of length 6 and landed inside the next
// comment), a COM segment whose payload is a fake one-component SOF0 and an
// SOS, then the real file after its SOI. image/jpeg skips "FF 00" as junk,
// skips the comment and decodes the real frame.
//
// With fakeEOI the comment also ends in "FF D9", so a walk that took the
// stray "FF 00" for a segment would stop at the fake end of image and never
// see the real frame header.
func craftedFF00JPEG(real []byte, w, h int, fakeEOI ...bool) []byte {
	payload := []byte{0xFF, 0xC0, 0, 11, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 1, 1, 0x11, 0,
		0xFF, 0xDA, 0, 8, 1, 1, 0, 0, 63, 0}
	if len(fakeEOI) > 0 && fakeEOI[0] {
		payload = append(payload, 0, 0, 0xFF, 0xD9)
	}
	out := []byte{0xFF, 0xD8, 0xFF, 0x00, 0x00, 0x06, 0xFF, 0xFE, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	out = append(out, payload...)
	return append(out, real[2:]...)
}

func TestThumbRefusesTheFF00FrameSubstitution(t *testing.T) {
	const w, h = 4000, 2666
	real := synthJPEG(jpegSpec{w: w, h: h, samp: [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, progressive: true, adobe: 0, pad: thumbMinSource})
	crafted := craftedFF00JPEG(real, w, h)
	cfg, format := decodeCfg(t, crafted)
	require.Equal(t, "jpeg", format)
	require.Equal(t, w, cfg.Width, "the decoder reads the real frame")
	require.Equal(t, color.CMYKModel, cfg.ColorModel, "the real frame is 4-component")

	_, ok := parseJPEG(bytes.NewReader(crafted), int64(len(crafted)))
	require.False(t, ok, "a stray FF 00 is not a marker: the file is refused, not guessed")
	withEOI := craftedFF00JPEG(real, w, h, true)
	cfg, _ = decodeCfg(t, withEOI)
	require.Equal(t, w, cfg.Width)
	_, ok = parseJPEG(bytes.NewReader(withEOI), int64(len(withEOI)))
	require.False(t, ok, "nor with a fake end of image in the comment")
	var pe *passError
	var err error
	total, _ := allocDuring(func() {
		_, _, err = transcode(bytes.NewReader(crafted), int64(len(crafted)), "image/jpeg", testLimits())
	})
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.reason, "unusual")
	if !raceEnabled {
		require.Less(t, total, int64(4<<20), "refused from the stream walk, before any decode")
	}
	// The real file is priced honestly (and refused over the ceiling).
	p, err := planThumb(bytes.NewReader(real), int64(len(real)), "image/jpeg", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	require.Greater(t, p.need, int64(300<<20))
	if raceEnabled || testing.Short() {
		return
	}
	// What the decoder would have allocated had the old estimate admitted it.
	dec := allocOf(func() { _, _, _ = image.Decode(bytes.NewReader(crafted)) })
	t.Logf("crafted %dx%d FF 00 file: refused; the decode alone allocates %.1f MiB (honest estimate of the real frame %.1f MiB)", w, h, mib(dec), mib(p.need))
}

// ---- review item 3: lossless WebP prefix code groups ----

func TestThumbPricesLosslessWebPWithMetaPrefixCodes(t *testing.T) {
	// The reviewer's shape: 801x1000 with 2,600 groups of full trees and an
	// 11-bit color cache, about 600 KB of tree data.
	spec := vp8lSpec{w: 801, h: 1000, cacheBits: 11, groups: 2600}
	data := riffWebP(webpChunk("VP8L", synthVP8L(spec, true)))
	_, _, err := transcode(bytes.NewReader(data), int64(len(data)), "image/webp", testLimits())
	var pe *passError
	require.ErrorAs(t, err, &pe, "2,600 groups of full trees are over the ceiling, whatever the pixel count")
	require.Contains(t, pe.reason, "decoding needs about")
	p, err := planThumb(bytes.NewReader(data), int64(len(data)), "image/webp", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	require.Greater(t, p.need, int64(defaultDecodeCeiling))

	// The same image in a VP8X container's compressed alpha plane.
	alpha := synthWebPAlpha(801, 1000, &spec)
	_, _, err = transcode(bytes.NewReader(alpha), int64(len(alpha)), "image/webp", testLimits())
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.reason, "decoding needs about")

	// A few groups are priced, not refused (libwebp writes them in the alpha
	// plane of most lossy+alpha files): 1600x1000 has 100x63 tiles of 16 px.
	few := riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: 1600, h: 1000, groups: 5}, true)))
	p, err = planThumb(bytes.NewReader(few), int64(len(few))+thumbMinSource, "image/webp", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	require.Greater(t, p.need, 5*vp8lGroupCost(0))
	tiny := synthWebPAlpha(900, 40, &vp8lSpec{w: 900, h: 40, groups: 3})
	_, err = planThumb(bytes.NewReader(tiny), int64(len(tiny))+thumbMinSource, "image/webp", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err, "a small alpha plane with a few groups is admitted")

	// Without meta codes the same trees and cache are admitted and priced.
	one := riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: 1600, h: 1000, cacheBits: 11, fullTrees: true, predictor: true}, true)))
	p, err = planThumb(bytes.NewReader(one), int64(len(one))+thumbMinSource, "image/webp", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	require.Positive(t, p.need)

	if raceEnabled || testing.Short() {
		return
	}
	var derr error
	dec := allocOf(func() { _, _, derr = image.Decode(bytes.NewReader(data)) })
	require.NoError(t, derr, "the crafted file is a valid WebP")
	t.Logf("801x1000 VP8L with %d groups (%d KB): refused; the decode alone allocates %.1f MiB", spec.groups, len(data)>>10, mib(dec))
}

func TestVP8LWalkRefusesWhatItCannotFollow(t *testing.T) {
	ok := func(s vp8lSpec) bool {
		_, ok := vp8lStreamCost(bytes.NewReader(synthVP8L(s, false)), s.w, s.h)
		return ok
	}
	require.True(t, ok(vp8lSpec{w: 300, h: 200, predictor: true, crossColor: true, subGreen: true, palette: true, cacheBits: 11, fullTrees: true}))
	require.True(t, ok(vp8lSpec{w: 300, h: 200, groups: 3}), "meta prefix codes are priced")
	require.True(t, ok(vp8lSpec{w: 300, h: 200, groups: 3, predictor: true, palette: true, cacheBits: 4}))
	// A short stream.
	s := synthVP8L(vp8lSpec{w: 300, h: 200, predictor: true}, false)
	_, good := vp8lStreamCost(bytes.NewReader(s[:3]), 300, 200)
	require.False(t, good)
	// An incomplete code (lengths 1 and 2 over two symbols) is refused.
	_, good = buildHuff([]uint8{1, 2})
	require.False(t, good)
	_, good = buildHuff([]uint8{1, 1})
	require.True(t, good)
	h, good := buildHuff([]uint8{0, 0, 5, 0})
	require.True(t, good, "one used symbol: zero bits, whatever its length")
	require.True(t, h.single)
}

// FuzzVP8LWalk: the walk never panics or runs away, whatever the bit stream
// (go test runs the seeds; go test -fuzz explores).
func FuzzVP8LWalk(f *testing.F) {
	for _, s := range []vp8lSpec{
		{w: 40, h: 30},
		{w: 40, h: 30, predictor: true, crossColor: true, subGreen: true, palette: true, cacheBits: 5, fullTrees: true},
		{w: 40, h: 30, cacheBits: 11, fullTrees: true},
		{w: 40, h: 30, groups: 3},
	} {
		f.Add(synthVP8L(s, false))
	}
	f.Fuzz(func(t *testing.T, stream []byte) {
		if n, ok := vp8lStreamCost(bytes.NewReader(stream), 40, 30); ok && n <= 0 {
			t.Fatal("an accepted stream has a cost")
		}
	})
}

// ---- review round 2, item 2: what a prefix code group costs ----

// TestVP8LWalkBoundsTheDecoderOnGroupHeavyFiles checks the walk's price
// against what golang.org/x/image/vp8l really allocates (TotalAlloc, garbage
// and allocator size classes included) for files whose memory is almost all
// prefix code groups, where the per-symbol constant decides the result. It
// is the walk alone, without the transcode's 5/4 factor and fixed 3 MiB, so
// only the decoder's own few kilobytes (its bufio reader, state and the image
// header) are allowed on top.
func TestVP8LWalkBoundsTheDecoderOnGroupHeavyFiles(t *testing.T) {
	skipMemoryTests(t)
	for _, tc := range []struct {
		name string
		s    vp8lSpec
	}{
		{"256 groups of full trees, 11-bit cache", vp8lSpec{w: 256, h: 256, cacheBits: 11, groups: 256}},
		{"256 groups of full trees, 10-bit cache", vp8lSpec{w: 256, h: 256, cacheBits: 10, groups: 256}},
		{"256 groups of full trees, no cache", vp8lSpec{w: 256, h: 256, groups: 256}},
		{"one full group, 11-bit cache", vp8lSpec{w: 16, h: 16, cacheBits: 11, fullTrees: true}},
		// Groups no tile uses are still read; with an index of 1,000 or more,
		// or past the tile count, the decoder keeps only the used ones.
		{"2,600 groups decoded, 1 kept, 16 tiles, 11-bit cache", vp8lSpec{w: 64, h: 64, cacheBits: 11, groups: 2600, sparse: true}},
		{"2,600 groups decoded, 1 kept, 16 tiles, no cache", vp8lSpec{w: 64, h: 64, groups: 2600, sparse: true}},
		// Below both limits every group up to the largest index is kept.
		{"1,000 groups kept, 1 used, 1,024 tiles", vp8lSpec{w: 512, h: 512, cacheBits: 11, groups: 1000, sparse: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := synthVP8L(tc.s, true)
			walk, ok := vp8lStreamCost(bytes.NewReader(data[5:]), tc.s.w, tc.s.h)
			require.True(t, ok)
			var derr error
			dec := allocOf(func() { _, derr = vp8l.Decode(bytes.NewReader(data)) })
			require.NoError(t, derr, "the synthetic file must decode")
			t.Logf("walk %.2f MiB, decoder allocated %.2f MiB (%.2fx)", mib(walk), mib(dec), float64(walk)/float64(dec))
			require.LessOrEqual(t, dec, walk+16<<10, "the walk is an upper bound of the decoder's allocation")
		})
	}
}

// TestVP8LSubImageKeepsWhatTheDecoderDecodes checks the entropy-image decode
// the group count relies on: random streams of literals, back-references
// (plain and mapped distances, overlapping copies) and color cache hits are
// walked with keep and decoded by golang.org/x/image/vp8l as a whole image,
// and the ARGB values must agree pixel for pixel.
func TestVP8LSubImageKeepsWhatTheDecoderDecodes(t *testing.T) {
	const w, h = 37, 23
	for seed := int64(1); seed <= 60; seed++ {
		ccBits := []int{0, 1, 3, 11}[seed%4]
		// The same pixel data twice: as a whole VP8L image (header, no
		// transform, cache, no meta codes) and as a bare sub-image.
		gen := func(image bool) []byte {
			rng := rand.New(rand.NewSource(seed))
			var bw bitWriter
			if image {
				bw.put(0x2f, 8)
				bw.put(w-1, 14)
				bw.put(h-1, 14)
				bw.put(0, 4)
				bw.put(0, 1) // no transform
			}
			if ccBits > 0 {
				bw.put(1, 1)
				bw.put(uint32(ccBits), 4)
			} else {
				bw.put(0, 1)
			}
			if image {
				bw.put(0, 1) // no meta prefix codes
			}
			green := vp8lLiterals + vp8lLengths
			if ccBits > 0 {
				green += 1 << ccBits
			}
			var codes [5]prefixCode
			for j, n := range [5]int{green, vp8lLiterals, vp8lLiterals, vp8lLiterals, vp8lDists} {
				codes[j] = bw.normalTree(completeLengths(n))
			}
			for p := 0; p < w*h; {
				switch k := rng.Intn(10); {
				case p > 0 && k < 3: // a back-reference
					length := min(1+rng.Intn(3*w), w*h-p)
					code := 1 + rng.Intn(120+p)
					if int64(p)-vp8lDistance(w, uint32(code)) < 0 {
						code = 120 + 1 + rng.Intn(p) // a plain distance of 1 to p
					}
					sym, extra, bits := lz77Code(length)
					bw.emit(codes[0], vp8lLiterals+sym)
					bw.put(bits, extra)
					sym, extra, bits = lz77Code(code)
					bw.emit(codes[4], sym)
					bw.put(bits, extra)
					p += length
				case ccBits > 0 && k < 6: // a color cache hit
					bw.emit(codes[0], vp8lLiterals+vp8lLengths+rng.Intn(1<<ccBits))
					p++
				default: // a literal, from few values so the cache fills with repeats
					bw.emit(codes[0], rng.Intn(4))
					for j := 1; j < 4; j++ {
						bw.emit(codes[j], rng.Intn(3)*100)
					}
					p++
				}
			}
			return bw.bytes()
		}
		m, err := vp8l.Decode(bytes.NewReader(gen(true)))
		require.NoError(t, err, "seed %d", seed)
		pix := m.(*image.NRGBA).Pix
		keep := make([]uint32, w*h)
		b := &vp8lBits{r: bufio.NewReader(bytes.NewReader(gen(false)))}
		_, ok := vp8lSubImage(b, w, h, 0, keep)
		require.True(t, ok, "seed %d", seed)
		for i, v := range keep {
			want := uint32(pix[4*i+3])<<24 | uint32(pix[4*i])<<16 | uint32(pix[4*i+1])<<8 | uint32(pix[4*i+2])
			require.Equal(t, want, v, "seed %d, cache %d bits, pixel %d", seed, ccBits, i)
		}
	}
}

func TestVP8LGroupsAreCountedTheWayTheDecoderCounts(t *testing.T) {
	g, d := vp8lGroupCost(11), vp8lDroppedGroupCost(11)
	require.EqualValues(t, 3136, vp8lSymbols(11))
	require.EqualValues(t, 5*536+24*3136+5*1024+4*2048, g, "24 bytes per symbol: code lengths, canonical codes, nodes")
	require.EqualValues(t, 4*3136+5*1024, d)
	// Below index 1,000 and the tile count, every group up to the largest index is kept.
	require.Equal(t, 5*g, vp8lGroupsCost(4, 1, 100, 11))
	// From index 1,000, or at the tile count, only the used ones.
	require.Equal(t, 2*g+2598*d+2*2600, vp8lGroupsCost(2599, 2, 4000, 11))
	require.Equal(t, g+9*d+2*10, vp8lGroupsCost(9, 1, 9, 11))

	// An entropy image naming group 2,600 is refused (the decoder refuses it too).
	s := vp8lSpec{w: 64, h: 64, groups: 2601, sparse: true}
	data := synthVP8L(s, true)
	_, ok := vp8lStreamCost(bytes.NewReader(data[5:]), s.w, s.h)
	require.False(t, ok)
	_, err := vp8l.Decode(bytes.NewReader(data))
	require.Error(t, err)
	// A legitimate few-group file is still priced and admitted.
	few := riffWebP(webpChunk("VP8L", synthVP8L(vp8lSpec{w: 1600, h: 1000, cacheBits: 10, groups: 12}, true)))
	p, err := planThumb(bytes.NewReader(few), int64(len(few))+thumbMinSource, "image/webp", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	require.Greater(t, p.need, 12*vp8lGroupCost(10))
}

// ---- review round 2, item 1: the lossy frame inside a VP8X container ----

// vp8xWebP is a VP8X WebP with a cw x ch canvas (no alpha) and the given VP8 frame.
func vp8xWebP(cw, ch int, frame []byte) []byte {
	x := make([]byte, 10)
	x[4], x[5], x[6] = byte(cw-1), byte((cw-1)>>8), byte((cw-1)>>16)
	x[7], x[8], x[9] = byte(ch-1), byte((ch-1)>>8), byte((ch-1)>>16)
	return riffWebP(webpChunk("VP8X", x), webpChunk("VP8 ", frame))
}

func TestWebPLossyFrameMustMatchTheCanvas(t *testing.T) {
	cost := func(data []byte, w, h int) bool {
		_, ok := webpDecodeCost(bytes.NewReader(data), int64(len(data)), w, h)
		return ok
	}
	// The valid shapes are priced.
	valid := vp8xWebP(640, 480, synthVP8(640, 480))
	cfg, format := decodeCfg(t, valid)
	require.Equal(t, "webp", format)
	require.Equal(t, 640, cfg.Width)
	require.True(t, cost(valid, 640, 480), "a VP8X canvas with the frame it names")
	require.True(t, cost(riffWebP(webpChunk("VP8 ", synthVP8(640, 480))), 640, 480), "a plain lossy file")
	require.True(t, cost(synthWebPAlpha(640, 480, nil), 640, 480), "a VP8X file with an alpha plane")

	// The two scaling bits above each 14-bit dimension are ignored, as the
	// decoder ignores them: a legitimate file that sets them is still priced.
	scaled := synthVP8(640, 480)
	scaled[7] |= 0x40
	scaled[9] |= 0x80
	require.True(t, cost(riffWebP(webpChunk("VP8 ", scaled)), 640, 480), "scaling bits are not part of the size")

	// A small canvas around a 4000x4000 frame: DecodeConfig reports the canvas,
	// so pricing it from the canvas would be about 1,600x short. Refused.
	crafted := vp8xWebP(100, 100, synthVP8(4000, 4000))
	cfg, _ = decodeCfg(t, crafted)
	require.Equal(t, 100, cfg.Width, "DecodeConfig reads the canvas")
	require.False(t, cost(crafted, 100, 100), "a frame larger than the canvas")
	require.False(t, cost(vp8xWebP(4000, 4000, synthVP8(100, 100)), 4000, 4000), "or smaller")
	x := make([]byte, 10)
	x[0] = 0x10
	x[4], x[7] = 99, 99
	withAlpha := riffWebP(webpChunk("VP8X", x), webpChunk("ALPH", append([]byte{0}, make([]byte, 100*100)...)), webpChunk("VP8 ", synthVP8(4000, 4000)))
	require.False(t, cost(withAlpha, 100, 100), "behind an alpha plane too")

	// Not a key frame, or no start code: refused.
	inter := synthVP8(640, 480)
	inter[0] |= 1
	require.False(t, cost(riffWebP(webpChunk("VP8 ", inter)), 640, 480))
	bad := synthVP8(640, 480)
	bad[3] = 0
	require.False(t, cost(vp8xWebP(640, 480, bad), 640, 480))
	require.False(t, cost(vp8xWebP(640, 480, synthVP8(640, 480)[:9]), 640, 480), "a truncated frame header")

	// Through the whole transcode: refused, the original is served.
	var pe *passError
	_, _, err := transcode(bytes.NewReader(crafted), int64(len(crafted)), "image/webp", testLimits())
	require.ErrorAs(t, err, &pe)
	// golang.org/x/image/webp (v0.46.0) refuses the mismatch too, after the
	// frame header and before any frame buffer: the walk no longer relies on it.
	_, _, err = image.Decode(bytes.NewReader(crafted))
	require.Error(t, err)
}
