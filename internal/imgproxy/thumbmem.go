package imgproxy

import (
	"encoding/binary"
	"io"
)

// The decode-memory model (design §7.4, Limits). thumbCost is an upper bound of
// every byte a transcode allocates, garbage included, so no GC timing can
// push the heap past it. It is computed from the file itself (a strict walk
// of a JPEG from SOI to EOI, PNG IHDR/tRNS, the WebP chunks and the VP8L
// transforms) the way the standard decoders size their buffers, plus the
// halving and encode buffers, times a safety factor. A file the model cannot
// account for exactly is not decoded at all (the original is served). The
// tests build worst-case files and check the real allocation against it.

const (
	costSafetyNum = 5 // the pixel buffers are multiplied by 5/4
	costSafetyDen = 4
	costFixed     = 3 << 20 // decoder state, bufio, zlib/flate state, the header scan
	maxSegments   = 4096    // JPEG/PNG/WebP header walks stop here
)

// decodeCost is what decoding the w x h image in r (size bytes, format as
// image.DecodeConfig named it) allocates. It reports false when the file is
// not one the model can account for: the image is then not thumbnailed.
func decodeCost(r io.ReaderAt, size int64, format string, w, h int) (int64, bool) {
	switch format {
	case "jpeg":
		f, ok := parseJPEG(r, size)
		if !ok || f.w != w || f.h != h {
			return 0, false // not well-formed, or its frame is not the one DecodeConfig read
		}
		return jpegDecodeCost(f), true
	case "png":
		return pngDecodeCost(r, size, w, h), true
	case "webp":
		return webpDecodeCost(r, size, w, h)
	}
	return 0, false
}

// thumbCost is the whole transcode: the decode (dec bytes) of a w x h
// picture, scaled to rw x rh (the pre-orientation size), with an EXIF
// orientation applied when oriented.
func thumbCost(dec int64, w, h, rw, rh int, oriented bool) int64 {
	out := halvingCost(w, h, rw, rh)
	final := 4 * int64(rw) * int64(rh)
	out += final     // the scaled picture
	out += 2 * final // the encoder's output buffer as it grows (never more than the raw pixels, doubled by bytes.Buffer)
	if oriented {
		out += final // orientImage's copy
	}
	return (dec+out)*costSafetyNum/costSafetyDen + costFixed
}

// halvingCost is what decodeScale's halving passes allocate: an RGBA (or
// NRGBA) picture of half the size per pass, until less than 2x is left.
func halvingCost(w, h, rw, rh int) int64 {
	var n int64
	for w >= 2*rw && h >= 2*rh {
		w, h = w/2, h/2
		n += 4 * int64(w) * int64(h)
	}
	return n
}

// pngDecodeCost mirrors image/png: the output picture (1 byte per pixel for
// gray or paletted, 2 for 16-bit gray, 4 for 8-bit color or any 8-bit image
// with transparency, 8 for 16-bit color or 16-bit transparency), twice for an
// Adam7-interlaced file (every pass is decoded into its own picture before it
// is merged), plus the two row buffers per pass.
func pngDecodeCost(r io.ReaderAt, size int64, w, h int) int64 {
	var ihdr [13]byte
	if _, err := r.ReadAt(ihdr[:], 16); err != nil {
		return 16 * int64(w) * int64(h)
	}
	depth, ct, interlaced := ihdr[8], ihdr[9], ihdr[12] == 1
	trns := pngHasTRNS(r, size)
	var bpp int64
	switch {
	case ct == 3:
		bpp = 1
	case ct == 0 && depth < 16 && !trns:
		bpp = 1
	case ct == 0 && depth == 16 && !trns:
		bpp = 2
	case depth == 16:
		bpp = 8
	default:
		bpp = 4
	}
	px := int64(w) * int64(h)
	n := bpp * px
	rows := 2 * (8*int64(w) + 1)
	if interlaced {
		n *= 2
		rows *= 7
	}
	return n + rows
}

// pngHasTRNS reports a tRNS chunk before the image data (an unreadable chunk
// list counts as one: the larger estimate).
func pngHasTRNS(r io.ReaderAt, size int64) bool {
	var hdr [8]byte
	off := int64(8)
	for i := 0; i < maxSegments && off+8 <= size; i++ {
		if _, err := r.ReadAt(hdr[:], off); err != nil {
			return true
		}
		switch string(hdr[4:8]) {
		case "tRNS":
			return true
		case "IDAT", "IEND":
			return false
		}
		off += 12 + int64(binary.BigEndian.Uint32(hdr[0:4]))
	}
	return true
}
