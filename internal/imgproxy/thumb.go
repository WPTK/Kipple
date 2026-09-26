package imgproxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"sync"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder (decode only; there is no pure-Go encoder)
)

// Thumbnail defaults (design §7.4).
const (
	// ThumbWidth is the width in pixels of a list-card thumbnail.
	ThumbWidth = 800
	// thumbMinSource is the smallest source worth re-encoding: below it the
	// original is already about as cheap as a thumbnail.
	thumbMinSource       = 150 << 10
	defaultThumbPixels   = 24_000_000
	defaultThumbWorkers  = 2
	defaultThumbQueue    = 16
	defaultDecodeCeiling = 96 << 20  // the most one decode may need
	defaultDecodeBudget  = 128 << 20 // the most all decodes together may need
	jpegQuality          = 80
	exifScan             = 256 << 10
)

// passError is a reason the original is served instead of a thumbnail. It is
// remembered (negative cache) so the attempt is not repeated for a day.
type passError struct{ reason string }

func (e *passError) Error() string { return "imgproxy: no thumbnail: " + e.reason }

func pass(format string, a ...any) error { return &passError{reason: fmt.Sprintf(format, a...)} }

// thumbLimits are the per-decode limits of transcode.
type thumbLimits struct {
	width, maxPixels int
	ceiling          int64
	budget           *budget
}

// budget is a shared allowance of estimated decode memory: transcodes that
// would together exceed it queue instead of running side by side.
type budget struct {
	mu   sync.Mutex
	cond *sync.Cond
	free int64
}

func newBudget(total int64) *budget {
	b := &budget{free: total}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *budget) acquire(n int64) {
	b.mu.Lock()
	for b.free < n {
		b.cond.Wait()
	}
	b.free -= n
	b.mu.Unlock()
}

func (b *budget) release(n int64) {
	b.mu.Lock()
	b.free += n
	b.mu.Unlock()
	b.cond.Broadcast()
}

// estimateDecode is an upper bound of the bytes decoding a w x h image of the
// given format needs. Progressive JPEG keeps 32-bit coefficients for every
// component, baseline JPEG a YCbCr image, PNG up to 16-bit RGBA, WebP YCbCr plus alpha.
func estimateDecode(format string, progressive bool, w, h int) int64 {
	px := int64(w) * int64(h)
	switch format {
	case "jpeg":
		if progressive {
			return px * 8
		}
		return px * 3
	case "png":
		return px * 8
	default:
		return px * 5
	}
}

// transcode makes a ThumbWidth-wide thumbnail of the image in r (size bytes,
// content type ct): JPEG at quality 80, or PNG when the picture has
// transparency. Metadata is dropped (an EXIF orientation is applied to the
// pixels first). It never upscales. When there is nothing to gain or the image
// is unsafe to decode it returns a *passError and the original is served.
func transcode(r io.ReaderAt, size int64, ct string, lim thumbLimits) (out []byte, outType string, err error) {
	switch ct {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, "", pass("%s is served as it is", ct) // gif, avif
	}
	if size < thumbMinSource {
		return nil, "", pass("the source is small")
	}
	head := make([]byte, exifScan)
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	if ct == "image/webp" && webpAnimated(head) {
		return nil, "", pass("animated WebP")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil {
		// The header may be longer than the scan (a huge JPEG segment): read it whole.
		cfg, format, err = image.DecodeConfig(io.NewSectionReader(r, 0, size))
	}
	if err != nil {
		return nil, "", pass("unreadable image")
	}
	switch format {
	case "jpeg", "png", "webp":
	default:
		return nil, "", pass("%s is served as it is", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > int64(lim.maxPixels) {
		return nil, "", pass("over %d megapixels", lim.maxPixels/1_000_000)
	}
	orient := 1
	progressive := false
	if format == "jpeg" {
		orient = exifOrientation(head)
		progressive = jpegProgressive(head)
	}
	dispW, dispH := cfg.Width, cfg.Height
	if orient >= 5 {
		dispW, dispH = dispH, dispW
	}
	if dispW <= lim.width {
		return nil, "", pass("already %d px wide or less", lim.width)
	}
	need := estimateDecode(format, progressive, cfg.Width, cfg.Height)
	if need > lim.ceiling {
		return nil, "", pass("decoding needs about %d MiB", need>>20)
	}
	lim.budget.acquire(need)
	defer lim.budget.release(need)

	newW := lim.width
	newH := max(1, int((int64(dispH)*int64(newW)+int64(dispW)/2)/int64(dispW)))
	rw, rh := newW, newH
	if orient >= 5 {
		rw, rh = newH, newW
	}
	dst, err := decodeScale(io.NewSectionReader(r, 0, size), rw, rh)
	if err != nil {
		return nil, "", pass("decode failed")
	}
	final := dst
	if orient != 1 {
		final = orientImage(dst, orient)
	}
	opaque := true
	if op, ok := final.(interface{ Opaque() bool }); ok {
		opaque = op.Opaque()
	}
	var buf bytes.Buffer
	if opaque {
		outType = "image/jpeg"
		err = jpeg.Encode(&buf, final, &jpeg.Options{Quality: jpegQuality})
	} else {
		outType = "image/png"
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, final)
	}
	if err != nil {
		return nil, "", pass("encode failed")
	}
	if int64(buf.Len()) >= size {
		return nil, "", pass("not smaller than the original")
	}
	return buf.Bytes(), outType, nil
}

// decodeScale decodes the image and scales it to w x h. The decoded picture is
// garbage as soon as it returns, before the (small) encode.
//
// A large reduction is done by halving until less than a factor of two is
// left, then one bilinear step. Each halving by ApproxBiLinear samples the
// centre of a 2x2 block, which is exactly a box average, so the result is not
// aliased; and unlike the kernel scalers of x/image/draw (which keep a
// float64 intermediate of dstW x srcH x 4, about 100 MB for a 20 MP photo)
// its working memory is the half-size picture.
func decodeScale(r io.Reader, w, h int) (image.Image, error) {
	src, _, err := image.Decode(bufio.NewReaderSize(r, 64<<10))
	if err != nil {
		return nil, err
	}
	cur := src
	for b := cur.Bounds(); b.Dx() >= 2*w && b.Dy() >= 2*h; b = cur.Bounds() {
		next := newLike(cur, b.Dx()/2, b.Dy()/2)
		xdraw.ApproxBiLinear.Scale(next, next.Bounds(), cur, b, xdraw.Src, nil)
		cur = next
	}
	dst := newLike(cur, w, h)
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), cur, cur.Bounds(), xdraw.Src, nil)
	return dst, nil
}

// newLike is an empty w x h picture that can hold src: opaque when src is.
func newLike(src image.Image, w, h int) xdraw.Image {
	if op, ok := src.(interface{ Opaque() bool }); ok && op.Opaque() {
		return image.NewRGBA(image.Rect(0, 0, w, h))
	}
	return image.NewNRGBA(image.Rect(0, 0, w, h))
}

// webpAnimated reports a VP8X header with the animation flag.
func webpAnimated(b []byte) bool {
	return len(b) >= 21 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" && string(b[12:16]) == "VP8X" && b[20]&0x02 != 0
}

// jpegProgressive scans the marker segments for a progressive frame header.
// An unfound frame header counts as progressive (the larger estimate).
func jpegProgressive(b []byte) bool {
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return true
		}
		m := b[i+1]
		switch {
		case m == 0xFF:
			i++
			continue
		case m == 0xC2 || m == 0xC6 || m == 0xCA || m == 0xCE:
			return true
		case m == 0xC0 || m == 0xC1 || m == 0xC3 || m == 0xC5 || m == 0xC7 || m == 0xC9 || m == 0xCB || m == 0xCD || m == 0xCF:
			return false
		case m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			i += 2
			continue
		}
		i += 2 + int(binary.BigEndian.Uint16(b[i+2:]))
	}
	return true
}

// exifOrientation is the EXIF orientation (1 to 8) of a JPEG, 1 when absent or unreadable.
func exifOrientation(b []byte) int {
	i := 2
	for i+4 <= len(b) && b[i] == 0xFF {
		m := b[i+1]
		if m == 0xDA || m == 0xD9 {
			break
		}
		l := int(binary.BigEndian.Uint16(b[i+2:]))
		if m == 0xE1 && i+10 <= len(b) && string(b[i+4:i+10]) == "Exif\x00\x00" {
			end := min(i+2+l, len(b))
			return tiffOrientation(b[i+10 : end])
		}
		i += 2 + l
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	cnt := int(bo.Uint16(t[off:]))
	for k := 0; k < cnt; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orientImage returns src with the EXIF orientation o (2 to 8) applied.
func orientImage(src image.Image, o int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			default:
				sx, sy = x, y
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

var errThumbQueueFull = errors.New("imgproxy: thumbnail queue full")

// pool is the single transcoder: a fixed set of workers behind a bounded
// queue. A full queue refuses at once, and the caller serves the original.
type pool struct {
	queue chan func()
	once  sync.Once
	wg    sync.WaitGroup
	n     int
	mu    sync.Mutex
	shut  bool
}

func newPool(workers, queue int) *pool {
	return &pool{queue: make(chan func(), queue), n: workers}
}

func (p *pool) submit(f func()) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shut {
		return errThumbQueueFull
	}
	p.once.Do(func() {
		for i := 0; i < p.n; i++ {
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				for f := range p.queue {
					f()
				}
			}()
		}
	})
	select {
	case p.queue <- f:
		return nil
	default:
		return errThumbQueueFull
	}
}

// close stops accepting work, lets the queued jobs finish and waits for the workers.
func (p *pool) close() {
	p.mu.Lock()
	if !p.shut {
		p.shut = true
		close(p.queue)
	}
	p.mu.Unlock()
	p.wg.Wait()
}
