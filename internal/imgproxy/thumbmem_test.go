package imgproxy

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	b.Write([]byte{0xFF, 0xD8})
	if s.jfif {
		seg(0xE0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	}
	if s.adobe >= 0 {
		seg(0xEE, append([]byte("Adobe"), 0, 100, 0, 0, 0, 0, byte(s.adobe)))
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
	var blocks int
	if n == 1 {
		blocks = ((s.w + 7) / 8) * ((s.h + 7) / 8)
	} else {
		mxx, myy := (s.w+8*maxH-1)/(8*maxH), (s.h+8*maxV-1)/(8*maxV)
		per := 0
		for _, hv := range s.samp {
			per += hv[0] * hv[1]
		}
		blocks = mxx * myy * per
	}
	b.Write(make([]byte, blocks*bits/8+64))
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
	bpp := (channels*int(depth) + 7) / 8
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
	_ = bpp
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

// synthWebPLossless is a valid VP8L WebP of transparent black pixels: every
// prefix code has a single symbol (zero bits per pixel). With a two-color
// palette (a color-indexing transform that bundles 8 pixels per byte) the
// decoder also allocates the unpacked copy: the lossless worst case.
func synthWebPLossless(w, h int, palette bool) []byte {
	var bw bitWriter
	trees := func() {
		for i := 0; i < 5; i++ {
			bw.put(1, 1) // simple code
			bw.put(0, 1) // one symbol
			bw.put(0, 1) // 1-bit symbol
			bw.put(0, 1) // symbol 0
		}
	}
	bw.put(0x2f, 8)
	bw.put(uint32(w-1), 14)
	bw.put(uint32(h-1), 14)
	bw.put(0, 1)
	bw.put(0, 3)
	if palette {
		bw.put(1, 1) // a transform
		bw.put(3, 2) // color indexing
		bw.put(1, 8) // two colors
		bw.put(0, 1) // the palette image: no color cache
		trees()
	}
	bw.put(0, 1) // no more transforms
	bw.put(0, 1) // no color cache
	bw.put(0, 1) // no meta prefix codes
	trees()
	payload := bw.bytes()
	if len(payload)%2 == 1 {
		payload = append(payload, 0)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBPVP8L")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
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
	return []memCase{
		{"jpeg baseline 4:2:0 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, adobe: -1, jfif: true})},
		{"jpeg baseline 4:4:4 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: rgb, adobe: -1, jfif: true})},
		{"jpeg baseline 4:1:1 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{4, 1}, {1, 1}, {1, 1}}, adobe: -1, jfif: true})},
		{"jpeg baseline flex 4:2:2/4:4:0 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{2, 2}, {2, 1}, {1, 2}}, adobe: -1, jfif: true})},
		{"jpeg baseline CMYK 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, adobe: 0})},
		{"jpeg baseline YCCK 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}, {2, 2}}, adobe: 2})},
		{"jpeg baseline Adobe RGB 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: rgb, adobe: 0})},
		{"jpeg baseline gray 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{1, 1}}, adobe: -1, jfif: true})},
		{"jpeg progressive 4:4:4 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: rgb, progressive: true, adobe: -1, jfif: true})},
		{"jpeg progressive CMYK 12 MP", "image/jpeg", synthJPEG(jpegSpec{w: 4240, h: 2832, samp: [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, progressive: true, adobe: 0})},
		{"jpeg progressive 4:2:0 24 MP", "image/jpeg", synthJPEG(jpegSpec{w: 6000, h: 4000, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, progressive: true, adobe: -1, jfif: true})},
		{"png 16-bit RGBA Adam7 12 MP", "image/png", synthPNG(t, 4240, 2832, 16, 6, true, false, 0)},
		{"png 16-bit gray+tRNS Adam7 8 MP", "image/png", synthPNG(t, 3464, 2310, 16, 0, true, true, 0)},
		{"png 8-bit RGB 24 MP", "image/png", synthPNG(t, 6000, 4000, 8, 2, false, false, 0)},
		{"png paletted 24 MP", "image/png", synthPNG(t, 6000, 4000, 4, 3, false, false, 0)},
		{"webp lossless 16 MP", "image/webp", synthWebPLossless(4900, 3266, false)},
		{"webp lossless palette 16 MP", "image/webp", synthWebPLossless(4900, 3266, true)},
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
			require.Less(t, p.need, 3*total, "and not so loose that it refuses what would fit")
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
	require.Equal(t, [4]int{2, 1, 1, 2}, f.h)
	require.True(t, f.adobe)
	require.EqualValues(t, 2, f.transform)
	require.False(t, f.jfif)

	_, ok = parseJPEG(bytes.NewReader([]byte("\xff\xd8\xff\xe0\x00\x10JFIF")), 12)
	require.False(t, ok, "no frame header: the caller assumes the worst")
	require.Equal(t, int64(100*60*25), jpegDecodeCost(bytes.NewReader([]byte("nope")), 4, 100, 60))
}
