package imgproxy

import (
	"bytes"
	"encoding/binary"
	"io"
)

// The decode-memory model (design §7.4, Limits). thumbCost is an upper bound of
// every byte a transcode allocates, garbage included, so no GC timing can
// push the heap past it. It is computed from the file's own headers (JPEG
// frame and Adobe markers, PNG IHDR/tRNS, WebP chunk types) the way the
// standard decoders size their buffers, plus the halving and encode buffers,
// times a safety factor. The tests build worst-case files and check the real
// allocation against it.

const (
	costSafetyNum = 5 // the pixel buffers are multiplied by 5/4
	costSafetyDen = 4
	costFixed     = 3 << 20 // decoder state, bufio, zlib/flate state, the header scan
	maxSegments   = 4096    // JPEG/PNG/WebP header walks stop here
)

// thumbCost estimates the bytes a transcode of a w x h image of the given
// format (read from r, size bytes) allocates when it scales to rw x rh (the
// pre-orientation size) and applies an EXIF orientation when rotated.
func thumbCost(r io.ReaderAt, size int64, format string, w, h, rw, rh int, oriented bool) int64 {
	var dec int64
	switch format {
	case "jpeg":
		dec = jpegDecodeCost(r, size, w, h)
	case "png":
		dec = pngDecodeCost(r, size, w, h)
	default:
		dec = webpDecodeCost(r, size, w, h)
	}
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

// jpegFrame is what the JPEG headers say about the decoder's buffers.
type jpegFrame struct {
	progressive bool
	n           int
	h, v        [4]int
	ids         [4]byte
	jfif        bool
	adobe       bool
	transform   byte
}

// parseJPEG walks the marker segments up to the first scan. It reports false
// when there is no usable frame header.
func parseJPEG(r io.ReaderAt, size int64) (jpegFrame, bool) {
	var f jpegFrame
	var b [4]byte
	if _, err := r.ReadAt(b[:2], 0); err != nil || b[0] != 0xFF || b[1] != 0xD8 {
		return f, false
	}
	off := int64(2)
	for seg := 0; seg < maxSegments && off+4 <= size; seg++ {
		if _, err := r.ReadAt(b[:2], off); err != nil {
			break
		}
		if b[0] != 0xFF {
			break
		}
		m := b[1]
		switch {
		case m == 0xFF: // fill byte
			off++
			continue
		case m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			off += 2
			continue
		case m == 0xD9 || m == 0xDA: // end of image, start of scan: the headers are over
			return f, f.n > 0
		}
		if _, err := r.ReadAt(b[2:4], off+2); err != nil {
			break
		}
		l := int64(binary.BigEndian.Uint16(b[2:4]))
		if l < 2 {
			break
		}
		payload := func(n int64) []byte {
			n = min(n, l-2)
			p := make([]byte, n)
			k, _ := r.ReadAt(p, off+4)
			return p[:k]
		}
		switch m {
		case 0xE0:
			if p := payload(5); bytes.Equal(p, []byte("JFIF\x00")) {
				f.jfif = true
			}
		case 0xEE:
			if p := payload(12); len(p) == 12 && bytes.Equal(p[:5], []byte("Adobe")) {
				f.adobe, f.transform = true, p[11]
			}
		case 0xC0, 0xC1, 0xC2:
			p := payload(6 + 3*4)
			if len(p) < 6 {
				return f, false
			}
			n := int(p[5])
			if n != 1 && n != 3 && n != 4 || len(p) < 6+3*n {
				return f, false
			}
			f.n, f.progressive = n, m == 0xC2
			for i := 0; i < n; i++ {
				f.ids[i] = p[6+3*i]
				f.h[i], f.v[i] = max(1, int(p[7+3*i]>>4)), max(1, int(p[7+3*i]&0x0f))
			}
		}
		off += 2 + l
	}
	return f, f.n > 0
}

// jpegDecodeCost mirrors image/jpeg's allocations: the MCU-padded Gray or
// YCbCr picture (4:4:4 whenever the sampling is not a standard ratio), the
// black plane of a 4-component file, 256 bytes per 8x8 block of every
// component for a progressive file's coefficients, and the 4 bytes per pixel
// RGBA/CMYK copy of a CMYK, YCCK or RGB file. An unreadable frame header costs
// the worst of all (progressive CMYK at 4:4:4).
func jpegDecodeCost(r io.ReaderAt, size int64, w, h int) int64 {
	px := int64(w) * int64(h)
	f, ok := parseJPEG(r, size)
	if !ok {
		return px * (3 + 1 + 4*4 + 4 + 1) // +1 per pixel for MCU padding
	}
	if f.n == 1 {
		f.h[0], f.v[0] = 1, 1 // the decoder treats a single component as 1x1 sampled
	}
	maxH, maxV := 1, 1
	for i := 0; i < f.n; i++ {
		maxH, maxV = max(maxH, f.h[i]), max(maxV, f.v[i])
	}
	mxx := int64((w + 8*maxH - 1) / (8 * maxH))
	myy := int64((h + 8*maxV - 1) / (8 * maxV))
	var n int64
	if f.n == 1 {
		n = 64 * mxx * myy
	} else {
		pw, ph := 8*int64(maxH)*mxx, 8*int64(maxV)*myy
		n = pw * ph
		cw, ch := pw, ph // 4:4:4, also every non-standard ("flex") sampling
		if f.h[1] == f.h[2] && f.v[1] == f.v[2] && f.h[0] == maxH && f.v[0] == maxV {
			switch (maxH/f.h[1])<<4 | maxV/f.v[1] {
			case 0x12:
				ch = (ph + 1) / 2
			case 0x21:
				cw = (pw + 1) / 2
			case 0x22:
				cw, ch = (pw+1)/2, (ph+1)/2
			case 0x41:
				cw = (pw + 3) / 4
			case 0x42:
				cw, ch = (pw+3)/4, (ph+1)/2
			}
		}
		n += 2 * cw * ch
		if f.n == 4 {
			n += 64 * int64(f.h[3]) * mxx * int64(f.v[3]) * myy
		}
	}
	if f.progressive {
		for i := 0; i < f.n; i++ {
			n += 256 * mxx * myy * int64(f.h[i]) * int64(f.v[i])
		}
	}
	rgb := f.n == 3 && !f.jfif && ((f.adobe && f.transform == 0) || (f.ids[0] == 'R' && f.ids[1] == 'G' && f.ids[2] == 'B'))
	if f.n == 4 || rgb {
		n += 4 * px
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

// webpDecodeCost mirrors golang.org/x/image/webp: lossy (VP8) is a 4:2:0 YCbCr
// picture padded to 16x16 macroblocks; lossless (VP8L) is 4 bytes per pixel,
// or 6 when a color-indexing transform unpacks pixels bundled two or more to
// a word into a new picture; an alpha plane is 1 byte per pixel raw, or a
// whole lossless decode plus the plane (7 bytes per pixel) when compressed.
// The frame's compressed data may be held whole, so the file size is added.
// Anything unrecognized costs the worst case.
func webpDecodeCost(r io.ReaderAt, size int64, w, h int) int64 {
	px := int64(w) * int64(h)
	mb := 256 * int64((w+15)/16) * int64((h+15)/16)
	lossy := mb*3/2 + mb/32 // YCbCr plus per-macroblock filter state
	const lossless = 6
	worst := lossy + 7*px + size
	var hdr [21]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WEBP" {
		return worst
	}
	switch string(hdr[12:16]) {
	case "VP8 ":
		return lossy + size
	case "VP8L":
		return lossless*px + size
	case "VP8X":
	default:
		return worst
	}
	alpha := hdr[20]&0x10 != 0
	var alphaCost, image int64 = 0, -1
	var ch [9]byte
	off := int64(30) // RIFF header (12) + VP8X chunk (8 + 10)
	for i := 0; i < maxSegments && off+8 <= size && image < 0; i++ {
		if _, err := r.ReadAt(ch[:], off); err != nil {
			break
		}
		l := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch string(ch[0:4]) {
		case "ALPH":
			if ch[8]&0x03 == 0 {
				alphaCost = px
			} else {
				alphaCost = 7 * px
			}
		case "VP8 ":
			image = lossy
		case "VP8L":
			image = lossless * px
		}
		off += 8 + l + l&1
	}
	switch {
	case image < 0:
		return worst
	case alpha && alphaCost == 0:
		alphaCost = 7 * px // flagged but not found before the image: assume compressed
	}
	return image + alphaCost + size
}
