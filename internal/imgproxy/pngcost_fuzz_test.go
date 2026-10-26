package imgproxy

import (
	"bytes"
	"image"
	_ "image/png"
	"testing"
)

// FuzzPNGCost: the PNG allocation model never panics on arbitrary bytes and, for a file whose header
// image.DecodeConfig accepts, prices the decode at no less than the decoded picture. It prices only; it never
// decodes (a decode in a fuzz body stalls the Go fuzzer on Windows). Pictures over 1<<20 px are skipped.
func FuzzPNGCost(f *testing.F) {
	for _, s := range []struct {
		depth, ct        byte
		interlaced, trns bool
		w, h             int
	}{
		{8, 6, false, false, 40, 30}, {8, 2, false, false, 40, 30}, {8, 0, false, false, 40, 30}, {16, 0, false, false, 40, 30},
		{16, 0, false, true, 40, 30}, {1, 0, false, false, 40, 30}, {8, 3, false, false, 40, 30}, {4, 3, true, false, 33, 21},
		{16, 2, true, true, 33, 21}, {16, 6, false, false, 9, 9}, {8, 4, false, false, 40, 30}, {16, 4, true, false, 17, 5},
		{8, 2, false, true, 1, 1},
	} {
		f.Add(synthPNG(f, s.w, s.h, s.depth, s.ct, s.interlaced, s.trns, 0))
	}
	f.Add(synthPNG(f, 40, 30, 8, 6, false, false, 300))
	f.Add(bombPNG(100, 100))
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width*cfg.Height > 1<<20 {
			return
		}
		n, ok := decodeCost(bytes.NewReader(data), int64(len(data)), "png", cfg.Width, cfg.Height)
		if !ok {
			t.Fatal("a PNG with a readable header was not priced")
		}
		// The smallest decoded picture for each colour type and depth: 1 byte per pixel for paletted and
		// below-16-bit gray, 2 for 16-bit gray, 4 for 8-bit color or gray-alpha, 8 for 16-bit with color or alpha.
		if len(data) < 26 {
			return
		}
		depth, ct := data[24], data[25]
		bpp := int64(4)
		switch {
		case ct == 3, ct == 0 && depth < 16:
			bpp = 1
		case ct == 0:
			bpp = 2
		case depth == 16:
			bpp = 8
		}
		if px := int64(cfg.Width) * int64(cfg.Height); n < bpp*px {
			t.Fatalf("%dx%d depth %d type %d priced at %d bytes, under its %d byte picture", cfg.Width, cfg.Height, depth, ct, n, bpp*px)
		}
	})
}
