package imgproxy

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/image/webp"
)

// libwebpFiles is the lossless and lossy+alpha WebP files golang.org/x/image
// ships as its own test data (encoded by libwebp, so with the transforms,
// color caches and meta prefix codes a real encoder writes), read from the
// module cache: nothing is committed or downloaded. It is empty when the
// module directory cannot be found.
func libwebpFiles(t testing.TB) map[string][]byte {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	out, err := exec.Command(gobin, "list", "-m", "-f", "{{.Dir}}", "golang.org/x/image").Output() // #nosec G204 -- fixed arguments
	if err != nil {
		return nil
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "testdata")
	names, _ := filepath.Glob(filepath.Join(dir, "*.webp"))
	files := map[string][]byte{}
	for _, n := range names {
		base := filepath.Base(n)
		if !strings.Contains(base, "lossless") && !strings.Contains(base, "with-alpha") {
			continue
		}
		b, err := os.ReadFile(n) // #nosec G304 -- the module cache's own test data
		if err == nil {
			files[base] = b
		}
	}
	return files
}

func sortedKeys(m map[string][]byte) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// TestVP8LWalkBoundsTheDecoderOnLibwebpFiles checks the lossless walk on
// files a real encoder wrote: every one is accepted (none is refused as
// unusual), and the price is an upper bound of what golang.org/x/image/webp
// really allocates to decode it.
func TestVP8LWalkBoundsTheDecoderOnLibwebpFiles(t *testing.T) {
	files := libwebpFiles(t)
	if len(files) == 0 {
		t.Skip("golang.org/x/image test data not found in the module cache")
	}
	for _, name := range sortedKeys(files) {
		data := files[name]
		t.Run(name, func(t *testing.T) {
			cfg, err := webp.DecodeConfig(bytes.NewReader(data))
			require.NoError(t, err)
			cost, ok := webpDecodeCost(bytes.NewReader(data), int64(len(data)), cfg.Width, cfg.Height)
			require.True(t, ok, "a libwebp file is one the walk can follow")
			require.GreaterOrEqual(t, cost, int64(cfg.Width)*int64(cfg.Height), "at least the picture")
			if raceEnabled || testing.Short() {
				return
			}
			var derr error
			dec := allocOf(func() { _, derr = webp.Decode(bytes.NewReader(data)) })
			require.NoError(t, derr)
			t.Logf("%dx%d, %d bytes: walk %.1f KiB, decoder allocated %.1f KiB (%.2fx)",
				cfg.Width, cfg.Height, len(data), float64(cost)/1024, float64(dec)/1024, float64(cost)/float64(dec))
			// The walk is the decode alone; the transcode adds 5/4 and 3 MiB on
			// top. Only the decoder's own few kilobytes (bufio, RIFF reader,
			// decoder state) are allowed over it.
			require.LessOrEqual(t, dec, cost+16<<10, "the walk is an upper bound of the decoder's allocation")
		})
	}
}

// TestVP8LWalkOnDamagedLibwebpFiles: every truncation of a real lossless file
// inside its image chunk is refused, and random byte changes never panic the walk; when a changed
// file is still accepted and still decodes, the price still bounds the
// decoder.
func TestVP8LWalkOnDamagedLibwebpFiles(t *testing.T) {
	files := libwebpFiles(t)
	if len(files) == 0 {
		t.Skip("golang.org/x/image test data not found in the module cache")
	}
	measure := !raceEnabled && !testing.Short()
	for _, name := range sortedKeys(files) {
		data := files[name]
		cfg, err := webp.DecodeConfig(bytes.NewReader(data))
		require.NoError(t, err)
		w, h := cfg.Width, cfg.Height
		// Up to the end of the image chunk (a pad byte or trailing metadata
		// the decoder never reads may go).
		end := 12
		for end+8 <= len(data) {
			id, l := string(data[end:end+4]), int(binary.LittleEndian.Uint32(data[end+4:]))
			end += 8 + l
			if id == "VP8 " || id == "VP8L" {
				break
			}
			end += l & 1
		}
		require.LessOrEqual(t, end, len(data))
		step := max(1, end/500)
		for n := 0; n < end; n += step {
			_, ok := webpDecodeCost(bytes.NewReader(data[:n]), int64(n), w, h)
			require.False(t, ok, "%s truncated to %d bytes", name, n)
		}
		rng := rand.New(rand.NewSource(int64(len(data))))
		accepted, bounded := 0, 0
		for i := 0; i < 300; i++ {
			m := append([]byte(nil), data...)
			// Mostly in the part the walk reads (headers, transforms, entropy image).
			for k := 0; k < 1+rng.Intn(3); k++ {
				m[rng.Intn(min(len(m), 12+rng.Intn(2048)))] ^= byte(1 + rng.Intn(255))
			}
			cost, ok := webpDecodeCost(bytes.NewReader(m), int64(len(m)), w, h)
			if !ok {
				continue
			}
			accepted++
			require.Positive(t, cost)
			if !measure {
				continue
			}
			var derr error
			dec := allocOf(func() { _, derr = webp.Decode(bytes.NewReader(m)) })
			if derr != nil {
				continue
			}
			bounded++
			require.LessOrEqual(t, dec, cost+16<<10, "%s, mutation %d: an accepted file is priced at least at its decode", name, i)
		}
		t.Logf("%s: %d of 300 mutations accepted, %d of them decoded within the price", name, accepted, bounded)
	}
}

// vp8lBomb is a VP8L WebP of w x h whose stream is a header and at most a few
// bytes, padded with an unknown chunk the decoder never reads so it clears the
// 150 KiB floor.
func vp8lBomb(w, h int, stream func(bw *bitWriter)) []byte {
	var bw bitWriter
	bw.put(0x2f, 8)
	bw.put(uint32(w-1), 14)
	bw.put(uint32(h-1), 14)
	bw.put(0, 1)
	bw.put(0, 3)
	stream(&bw)
	return riffWebP(webpChunk("VP8L", bw.bytes()), webpChunk("PADD", make([]byte, thumbMinSource)))
}

// TestThumbRefusesVP8LBombHeaders: lossless headers that promise far more
// memory than the file holds are refused from the bytes, before the decoder
// allocates anything.
func TestThumbRefusesVP8LBombHeaders(t *testing.T) {
	zeroSub := func(bw *bitWriter) {
		bw.put(0, 1) // no color cache
		for i := 0; i < 5; i++ {
			bw.simpleTree(0)
		}
	}
	for _, tc := range []struct {
		name, reason string
		data         []byte
	}{
		// The largest size a VP8L header can name: 268 MP.
		{"16384x16384 header", "megapixels", vp8lBomb(16384, 16384, func(bw *bitWriter) { bw.put(0, 1) })},
		{"just over 24 MP", "megapixels", vp8lBomb(5000, 4801, func(bw *bitWriter) { bw.put(0, 1) })},
		// 24 MP of NRGBA (96 MiB) plus the unpacked copy of a two-color palette.
		{"24 MP palette", "decoding needs about", vp8lBomb(6000, 4000, func(bw *bitWriter) {
			bw.put(1, 1)
			bw.put(3, 2)
			bw.put(1, 8)
			zeroSub(bw)
			bw.put(0, 1) // no more transforms
			bw.put(0, 1) // no color cache
			bw.put(0, 1) // no meta codes
		})},
		// Meta prefix codes with 4x4 tiles: a 1.5 million tile entropy image,
		// too large to count its groups, so it is priced at 2,600 groups.
		{"24 MP meta codes, 1.5 M tiles", "decoding needs about", vp8lBomb(6000, 4000, func(bw *bitWriter) {
			bw.put(0, 1) // no transform
			bw.put(0, 1) // no color cache
			bw.put(1, 1) // meta prefix codes
			bw.put(0, 3) // 4x4 tiles
			zeroSub(bw)
		})},
		// A predictor transform whose sub-image the stream does not hold.
		{"truncated predictor sub-image", "unusual", vp8lBomb(4000, 4000, func(bw *bitWriter) {
			bw.put(1, 1)
			bw.put(0, 2)
			bw.put(0, 3)
			bw.put(0, 1)
			bw.put(0, 1) // a normal code, then nothing
		})},
		// A color cache of 12 bits, which the format does not allow.
		{"12-bit color cache", "unusual", vp8lBomb(4000, 3000, func(bw *bitWriter) {
			bw.put(0, 1)
			bw.put(1, 1)
			bw.put(12, 4)
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pe *passError
			var err error
			total, _ := allocDuring(func() { _, _, err = runTranscode(t, tc.data, "image/webp") })
			require.ErrorAs(t, err, &pe)
			require.Contains(t, pe.reason, tc.reason)
			if !raceEnabled {
				// The header scan (256 KiB), the walk's reader and a small entropy
				// image; nothing near the size of the picture.
				require.Less(t, total, int64(2<<20), "refused before any decode")
			}
		})
	}
}
