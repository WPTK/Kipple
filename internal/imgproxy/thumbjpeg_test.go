package imgproxy

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// jpegDecoderState is what image/jpeg allocates besides the pictures the
// model prices (its decoder struct, Huffman and quantization tables, read
// buffer): part of costFixed in thumbCost.
const jpegDecoderState = 64 << 10

// jpegBases are small well-formed JPEGs of every shape the model prices
// differently, synthetic (flat blocks) and real (encoder output, whose
// entropy-coded data has "FF 00" stuffing).
func jpegBases(t testing.TB) map[string][]byte {
	rgb := [][2]int{{1, 1}, {1, 1}, {1, 1}}
	cmyk := [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}
	return map[string][]byte{
		"baseline 4:2:0 JFIF":        synthJPEG(jpegSpec{w: 97, h: 61, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, adobe: -1, jfif: true}),
		"progressive 4:4:4 JFIF":     synthJPEG(jpegSpec{w: 97, h: 61, samp: rgb, progressive: true, adobe: -1, jfif: true}),
		"baseline CMYK":              synthJPEG(jpegSpec{w: 97, h: 61, samp: cmyk, adobe: 0}),
		"progressive YCCK":           synthJPEG(jpegSpec{w: 97, h: 61, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}, {2, 2}}, progressive: true, adobe: 2}),
		"baseline gray":              synthJPEG(jpegSpec{w: 97, h: 61, samp: [][2]int{{1, 1}}, adobe: -1, jfif: true}),
		"baseline flex":              synthJPEG(jpegSpec{w: 97, h: 61, samp: [][2]int{{2, 2}, {2, 1}, {1, 2}}, adobe: -1, jfif: true}),
		"baseline RGB ids":           synthJPEG(jpegSpec{w: 97, h: 61, samp: rgb, adobe: -1, rgbIDs: true}),
		"baseline Adobe after scan":  synthJPEG(jpegSpec{w: 97, h: 61, samp: rgb, adobe: 0, adobeAfterScan: true}),
		"progressive CMYK":           synthJPEG(jpegSpec{w: 97, h: 61, samp: cmyk, progressive: true, adobe: 0}),
		"baseline 4:2:0 restarts":    synthJPEG(jpegSpec{w: 97, h: 61, samp: [][2]int{{2, 2}, {1, 1}, {1, 1}}, adobe: -1, jfif: true, restart: true}),
		"encoded noise 4:2:0":        jpegOf(t, noise(120, 80, false), 90),
		"encoded noise gray quality": jpegOf(t, noise(64, 64, false), 100),
	}
}

// jpegMarkerOffsets lists where each marker after SOI starts in a
// well-formed file (the positions a segment can be inserted at).
func jpegMarkerOffsets(data []byte) []int {
	var out []int
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return out
		}
		out = append(out, i)
		m := data[i+1]
		if m == 0xD9 {
			return out
		}
		i += 2 + int(binary.BigEndian.Uint16(data[i+2:]))
		if m == 0xDA { // skip the entropy-coded data
			for i+1 < len(data) && !(data[i] == 0xFF && data[i+1] != 0 && (data[i+1] < 0xD0 || data[i+1] > 0xD7)) {
				i++
			}
		}
	}
	return out
}

func jpegSeg(m byte, p []byte) []byte {
	return append([]byte{0xFF, m, byte((len(p) + 2) >> 8), byte(len(p) + 2)}, p...)
}

func insertAt(data []byte, at int, ins []byte) []byte {
	out := append([]byte{}, data[:at]...)
	out = append(out, ins...)
	return append(out, data[at:]...)
}

// mutateJPEG returns data changed in one of the ways a hostile or broken file
// differs from a well-formed one.
func mutateJPEG(rng *rand.Rand, data []byte) (string, []byte) {
	offs := jpegMarkerOffsets(data)
	at := offs[rng.IntN(len(offs))]
	// Before the first scan (after it, stray bytes are scan data, as they are to the decoder).
	var head []int
	for _, o := range offs {
		head = append(head, o)
		if data[o+1] == 0xDA {
			break
		}
	}
	hat := head[rng.IntN(len(head))]
	fakeSOF := func() []byte {
		n := []int{1, 3, 4}[rng.IntN(3)]
		p := []byte{8, byte(rng.IntN(4)), byte(rng.IntN(256)), byte(rng.IntN(4)), byte(rng.IntN(256)), byte(n)}
		for i := 0; i < n; i++ {
			p = append(p, byte(i+1), 0x11, 0)
		}
		return jpegSeg(0xC0|byte(rng.IntN(3)), p)
	}
	sofAt := -1
	for _, o := range offs {
		if m := data[o+1]; m >= 0xC0 && m <= 0xC2 {
			sofAt = o
		}
	}
	switch k := rng.IntN(13); k {
	case 0:
		junk := make([]byte, 1+rng.IntN(4))
		for i := range junk {
			junk[i] = byte(rng.IntN(256))
		}
		if rng.IntN(2) == 0 {
			return "junk bytes in the headers", insertAt(data, 2+rng.IntN(hat-1), junk)
		}
		return "junk bytes anywhere", insertAt(data, 2+rng.IntN(len(data)-2), junk)
	case 1:
		return "FF 00 in the headers", insertAt(data, hat, []byte{0xFF, 0x00})
	case 2:
		l := int(binary.BigEndian.Uint16(data[sofAt+2:]))
		return "duplicate SOF", insertAt(data, at, append([]byte{}, data[sofAt:sofAt+2+l]...))
	case 3:
		return "COM with a fake SOF and SOS", insertAt(data, at, jpegSeg(0xFE, append(fakeSOF(), 0xFF, 0xDA, 0, 8, 1, 1, 0, 0, 63, 0)))
	case 4:
		return "truncated", data[:2+rng.IntN(len(data)-2)]
	case 5:
		tail := make([]byte, 1+rng.IntN(64))
		for i := range tail {
			tail[i] = byte(rng.IntN(256))
		}
		return "garbage after EOI", append(append([]byte{}, data...), tail...)
	case 6:
		out := append([]byte{}, data...)
		out[2+rng.IntN(len(out)-2)] = byte(rng.IntN(256))
		return "a flipped byte", out
	case 7:
		return "Adobe APP14 anywhere", insertAt(data, at, jpegSeg(0xEE, append([]byte("Adobe"), 0, 100, 0, 0, 0, 0, byte(rng.IntN(3)))))
	case 8:
		p := []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00")
		if rng.IntN(2) == 0 {
			p = []byte("JFXX\x00\x10")
		}
		return "APP0 anywhere", insertAt(data, at, jpegSeg(0xE0, p))
	case 9:
		fake := append(fakeSOF(), 0xFF, 0xDA, 0, 8, 1, 1, 0, 0, 63, 0)
		if rng.IntN(2) == 0 {
			fake = append(fake, 0, 0, 0xFF, 0xD9) // a fake end of image too
		}
		prefix := append([]byte{0xFF, 0xD8, 0xFF, 0x00, 0x00, 0x06}, jpegSeg(0xFE, fake)...)
		return "the reviewer's FF 00 frame substitution", append(prefix, data[2:]...)
	case 10:
		return "fill bytes before a marker", insertAt(data, at, bytes.Repeat([]byte{0xFF}, 1+rng.IntN(3)))
	case 11:
		return "a bare restart marker in the headers", insertAt(data, hat, []byte{0xFF, 0xD0 + byte(rng.IntN(8))})
	default:
		return "a second frame header after the scans", insertAt(data, offs[len(offs)-1], fakeSOF())
	}
}

// TestJPEGStrictParserAgreesOrRefuses mutates well-formed headers the ways a
// hostile file would (junk, stray "FF 00", a duplicate SOF, comments holding a
// fake frame, truncation, trailing garbage, flipped bytes, Adobe and JFIF
// markers anywhere) and checks the property the thumbnailer relies on: the
// strict walk either refuses the file or agrees with image.DecodeConfig on
// its size AND prices at least what decoding it really allocates.
func TestJPEGStrictParserAgreesOrRefuses(t *testing.T) {
	measure := !raceEnabled
	rng := rand.New(rand.NewPCG(7, 26))
	accepted, refused := map[string]int{}, map[string]int{}
	var worst float64
	for name, base := range jpegBases(t) {
		_, ok := parseJPEG(bytes.NewReader(base), int64(len(base)))
		require.True(t, ok, "base %s is well-formed", name)
		require.NotEmpty(t, jpegMarkerOffsets(base))
		check := func(kind string, m []byte) {
			f, ok := parseJPEG(bytes.NewReader(m), int64(len(m)))
			if !ok {
				refused[kind]++
				return
			}
			accepted[kind]++
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(m))
			require.NoError(t, err, "%s/%s: accepted but DecodeConfig fails", name, kind)
			require.Equal(t, [2]int{cfg.Width, cfg.Height}, [2]int{f.w, f.h}, "%s/%s: the walk and DecodeConfig disagree", name, kind)
			if !measure {
				return
			}
			cost := jpegDecodeCost(f) + jpegDecoderState
			got := allocOf(func() { _, _ = jpeg.Decode(bytes.NewReader(m)) })
			worst = max(worst, float64(got)/float64(cost))
			require.LessOrEqual(t, got, cost, "%s/%s: decoding allocates %d bytes, the model prices %d", name, kind, got, cost)
		}
		check("unchanged", base)
		for i := 0; i < 120; i++ {
			kind, m := mutateJPEG(rng, base)
			check(kind, m)
		}
	}
	t.Logf("accepted %v; refused %v; worst allocation/price %.2f", accepted, refused, worst)
	for _, kind := range []string{"FF 00 in the headers", "duplicate SOF", "junk bytes in the headers", "the reviewer's FF 00 frame substitution", "a second frame header after the scans", "a bare restart marker in the headers"} {
		require.Zero(t, accepted[kind], "%s is always refused", kind)
	}
	for _, kind := range []string{"COM with a fake SOF and SOS", "garbage after EOI", "Adobe APP14 anywhere", "APP0 anywhere", "fill bytes before a marker"} {
		require.Zero(t, refused[kind], "%s is well-formed and accepted", kind)
	}
}

// FuzzParseJPEG: whatever the bytes, an accepted file's size is the one
// image.DecodeConfig reads (go test runs the seeds; go test -fuzz explores).
func FuzzParseJPEG(f *testing.F) {
	for _, b := range jpegBases(f) {
		f.Add(b)
	}
	real := synthJPEG(jpegSpec{w: 400, h: 266, samp: [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, progressive: true, adobe: 0})
	f.Add(craftedFF00JPEG(real, 400, 266))
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, ok := parseJPEG(bytes.NewReader(data), int64(len(data)))
		if !ok {
			return
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("accepted, but DecodeConfig fails: %v", err)
		}
		if cfg.Width != fr.w || cfg.Height != fr.h {
			t.Fatalf("walk %dx%d, DecodeConfig %dx%d", fr.w, fr.h, cfg.Width, cfg.Height)
		}

	})
}
