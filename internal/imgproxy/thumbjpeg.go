package imgproxy

import (
	"bufio"
	"encoding/binary"
	"io"
)

// The strict JPEG walk behind the decode-memory model (design §7.4, Limits).
//
// image/jpeg is liberal: it skips stray bytes between segments (an "FF 00"
// included) looking for the next marker, and it keeps reading markers after
// the first scan (an Adobe APP14 there still changes the color conversion).
// A model that reads the headers any other way can be shown one frame while
// the decoder decodes another (a fake grayscale frame hidden in a comment
// after a stray "FF 00", in front of a progressive CMYK one), and it then
// admits a decode that costs ten times its estimate.
//
// So the model does not try to guess what the decoder will make of an odd
// file. It accepts only a well-formed stream, walked from SOI to EOI:
// segments that each start with FF and a known marker (legal FF fill bytes
// before a marker allowed), with lengths that fit the file; exactly one frame
// header (SOF0, SOF1 or SOF2) with 1, 3 or 4 components and sampling factors
// the decoder supports; at least one scan; entropy-coded data after each SOS
// (the only place "FF 00" stuffing and RSTn markers may appear); and the JFIF
// and Adobe markers read wherever they are, the last one of each winning, as
// in the decoder. Anything else is "unknown" and the image is not
// thumbnailed: the original is served and the refusal remembered. In such a
// stream every byte is where the decoder looks for it too, so the frame the
// model prices is the frame the decoder decodes. planThumb also checks the
// size against image.DecodeConfig.

// jpegFrame is what the JPEG stream says about the decoder's buffers.
type jpegFrame struct {
	w, h        int
	progressive bool
	n           int // components
	hs, vs      [4]int
	ids         [4]byte
	jfif        bool
	adobe       bool
	transform   byte
}

// jpegWalker reads the stream once, front to back, through a buffer.
type jpegWalker struct {
	br   *bufio.Reader
	pos  int64
	size int64
}

func (s *jpegWalker) byte() (byte, bool) {
	c, err := s.br.ReadByte()
	if err != nil {
		return 0, false
	}
	s.pos++
	return c, true
}

func (s *jpegWalker) read(p []byte) bool {
	n, err := io.ReadFull(s.br, p)
	s.pos += int64(n)
	return err == nil
}

func (s *jpegWalker) skip(n int) bool {
	k, err := s.br.Discard(n)
	s.pos += int64(k)
	return err == nil
}

// parseJPEG walks the whole stream and reports false for anything that is not
// well-formed (see above). It reads the file once; the entropy-coded data is
// scanned, not decoded.
func parseJPEG(r io.ReaderAt, size int64) (jpegFrame, bool) {
	var f jpegFrame
	s := &jpegWalker{br: bufio.NewReaderSize(io.NewSectionReader(r, 0, size), 32<<10), size: size}
	var b [2 + 6 + 3*4]byte
	if !s.read(b[:2]) || b[0] != 0xFF || b[1] != 0xD8 {
		return f, false
	}
	sawSOF, scans, inScan := false, 0, false
	for seg := 0; seg < maxSegments; seg++ {
		var m byte
		if inScan {
			// Entropy-coded data up to the next marker: "FF 00" is a stuffed FF
			// and FF D0..D7 a restart marker; any other FF ends the scan.
			var ok bool
			if m, ok = s.scanData(); !ok {
				return f, false
			}
			inScan = false
		} else {
			c, ok := s.byte()
			if !ok || c != 0xFF {
				return f, false // stray bytes between segments (the decoder would skip them; the model does not guess)
			}
			if m, ok = s.marker(); !ok {
				return f, false
			}
		}
		switch {
		case m == 0xD9: // EOI: the decoder stops here, and so does the walk (trailing bytes are never read)
			return f, sawSOF && scans > 0
		case m == 0x00, m == 0x01, m == 0xD8, m >= 0xD0 && m <= 0xD7:
			return f, false // "FF 00" or a restart marker outside scan data, TEM, a second SOI
		}
		if !s.read(b[:2]) {
			return f, false
		}
		n := int(binary.BigEndian.Uint16(b[:2])) - 2
		if n < 0 || s.pos+int64(n) > size {
			return f, false
		}
		switch {
		case m == 0xC0 || m == 0xC1 || m == 0xC2:
			if sawSOF || !parseSOF(s, n, m == 0xC2, &f) {
				return f, false // a second frame header, or one the decoder cannot use
			}
			sawSOF = true
		case m == 0xDA:
			if !sawSOF || n < 6 || n > 4+2*f.n || n%2 != 0 || !s.read(b[:1]) || int(b[0]) < 1 || n != 4+2*int(b[0]) || !s.skip(n-1) {
				return f, false
			}
			scans++
			inScan = true
		case m == 0xC4 || m == 0xDB || m == 0xDD: // DHT, DQT, DRI: the decoder checks their contents; they do not size anything
			if !s.skip(n) {
				return f, false
			}
		case m == 0xE0: // APP0: as in the decoder, any APP0 of 5 bytes or more sets (or clears) the JFIF flag
			if n >= 5 {
				if !s.read(b[:5]) {
					return f, false
				}
				f.jfif = string(b[:5]) == "JFIF\x00"
				n -= 5
			}
			if !s.skip(n) {
				return f, false
			}
		case m == 0xEE: // APP14: an Adobe segment of 12 bytes or more sets the transform
			if n >= 12 {
				if !s.read(b[:12]) {
					return f, false
				}
				if string(b[:5]) == "Adobe" {
					f.adobe, f.transform = true, b[11]
				}
				n -= 12
			}
			if !s.skip(n) {
				return f, false
			}
		case m >= 0xE1 && m <= 0xEF, m == 0xFE: // other APPn, COM
			if !s.skip(n) {
				return f, false
			}
		default:
			return f, false // SOF3/5..15, DAC, DNL, DHP, EXP, JPGn: not decoded, or not sized by the model
		}
	}
	return f, false
}

// marker reads the byte after an FF, skipping FF fill bytes.
func (s *jpegWalker) marker() (byte, bool) {
	for {
		m, ok := s.byte()
		if !ok || m != 0xFF {
			return m, ok
		}
	}
}

// scanData skips entropy-coded data and returns the marker that ends it.
func (s *jpegWalker) scanData() (byte, bool) {
	for {
		c, ok := s.byte()
		if !ok {
			return 0, false // no EOI
		}
		if c != 0xFF {
			continue
		}
		m, ok := s.byte()
		if !ok {
			return 0, false
		}
		switch {
		case m == 0x00, m >= 0xD0 && m <= 0xD7:
			continue
		case m == 0xFF:
			return s.marker()
		}
		return m, true
	}
}

// parseSOF reads a frame header of n bytes and accepts only what image/jpeg
// decodes: 8-bit precision, a non-zero size, 1, 3 or 4 components with
// distinct ids, sampling factors 1, 2 or 4 that divide the largest (3
// components), and the two 4-component layouts it supports.
func parseSOF(s *jpegWalker, n int, progressive bool, f *jpegFrame) bool {
	var p [6 + 3*4]byte
	nc := (n - 6) / 3
	if n != 6+3*nc || nc != 1 && nc != 3 && nc != 4 || !s.read(p[:n]) {
		return false
	}
	if p[0] != 8 || int(p[5]) != nc {
		return false
	}
	f.h, f.w = int(binary.BigEndian.Uint16(p[1:3])), int(binary.BigEndian.Uint16(p[3:5]))
	if f.w == 0 || f.h == 0 {
		return false // a zero height means a DNL marker, which the decoder does not support
	}
	f.n, f.progressive = nc, progressive
	maxH, maxV := 1, 1
	for i := 0; i < nc; i++ {
		id, hv := p[6+3*i], p[7+3*i]
		if p[8+3*i] > 3 {
			return false // quantization table selector
		}
		for j := 0; j < i; j++ {
			if f.ids[j] == id {
				return false
			}
		}
		h, v := int(hv>>4), int(hv&0x0f)
		if h < 1 || h > 4 || v < 1 || v > 4 || h == 3 || v == 3 {
			return false
		}
		if nc == 4 {
			switch {
			case i == 0 && hv != 0x11 && hv != 0x22,
				(i == 1 || i == 2) && hv != 0x11,
				i == 3 && (h != f.hs[0] || v != f.vs[0]):
				return false
			}
		}
		f.ids[i], f.hs[i], f.vs[i] = id, h, v
		maxH, maxV = max(maxH, h), max(maxV, v)
	}
	if nc == 3 {
		for i := 0; i < 3; i++ {
			if maxH%f.hs[i] != 0 || maxV%f.vs[i] != 0 {
				return false
			}
		}
	}
	return true
}

// jpegDecodeCost mirrors image/jpeg's allocations for a well-formed stream:
// the MCU-padded Gray or YCbCr picture (4:4:4 whenever the sampling is not a
// standard ratio), the black plane of a 4-component file, 256 bytes per 8x8
// block of every component for a progressive file's coefficients, and the 4
// bytes per pixel RGBA/CMYK copy of a CMYK, YCCK or RGB file.
func jpegDecodeCost(f jpegFrame) int64 {
	w, h := f.w, f.h
	px := int64(w) * int64(h)
	if f.n == 1 {
		f.hs[0], f.vs[0] = 1, 1 // the decoder treats a single component as 1x1 sampled
	}
	maxH, maxV := 1, 1
	for i := 0; i < f.n; i++ {
		maxH, maxV = max(maxH, f.hs[i]), max(maxV, f.vs[i])
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
		if f.hs[1] == f.hs[2] && f.vs[1] == f.vs[2] && f.hs[0] == maxH && f.vs[0] == maxV {
			switch (maxH/f.hs[1])<<4 | maxV/f.vs[1] {
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
			n += 64 * int64(f.hs[3]) * mxx * int64(f.vs[3]) * myy
		}
	}
	if f.progressive {
		for i := 0; i < f.n; i++ {
			n += 256 * mxx * myy * int64(f.hs[i]) * int64(f.vs[i])
		}
	}
	rgb := f.n == 3 && !f.jfif && ((f.adobe && f.transform == 0) || (f.ids[0] == 'R' && f.ids[1] == 'G' && f.ids[2] == 'B'))
	if f.n == 4 || rgb {
		n += 4 * px
	}
	return n
}
