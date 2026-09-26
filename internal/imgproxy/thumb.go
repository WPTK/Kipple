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
	thumbMinSource      = 150 << 10
	defaultThumbPixels  = 24_000_000
	defaultThumbWorkers = 2
	defaultThumbQueue   = 16
	// defaultDecodeCeiling is the most one transcode may be estimated to
	// allocate (thumbCost, an upper bound of every byte including garbage), and
	// defaultDecodeBudget the most all running transcodes together may: the
	// worst admitted case keeps the transient heap under 100 MiB in a 256 MB
	// container with GOMEMLIMIT=64MiB.
	defaultDecodeCeiling = 80 << 20
	defaultDecodeBudget  = 96 << 20
	jpegQuality          = 80
	exifScan             = 256 << 10
)

// Test hooks: nil in production.
var (
	testHookBeforeDecode func() // on the worker, after the in-progress marker is written, before the decode
	testHookThumbLeader  func() // on the request, right after it becomes the thumbnail leader
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

// thumbPlan is what the headers say about a transcode, before any decode.
type thumbPlan struct {
	format string
	w, h   int   // the stored picture
	orient int   // EXIF orientation, 1 when none
	rw, rh int   // the scaled size before orientation
	need   int64 // thumbCost: the bytes the transcode may allocate
}

// planThumb reads the headers of the image in r (size bytes, content type ct)
// and decides whether a thumbnail is possible and what it costs. It never
// decodes pixels. Refusals are *passError.
func planThumb(r io.ReaderAt, size int64, ct string, width, maxPixels int) (thumbPlan, error) {
	var p thumbPlan
	switch ct {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return p, pass("%s is served as it is", ct) // gif, avif
	}
	if size < thumbMinSource {
		return p, pass("the source is small")
	}
	head := make([]byte, exifScan)
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	if ct == "image/webp" && webpAnimated(head) {
		return p, pass("animated WebP")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil {
		// The header may be longer than the scan (a huge JPEG segment): read it whole.
		cfg, format, err = image.DecodeConfig(io.NewSectionReader(r, 0, size))
	}
	if err != nil {
		return p, pass("unreadable image")
	}
	switch format {
	case "jpeg", "png", "webp":
	default:
		return p, pass("%s is served as it is", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > int64(maxPixels) {
		return p, pass("over %d megapixels", maxPixels/1_000_000)
	}
	p.format, p.w, p.h, p.orient = format, cfg.Width, cfg.Height, 1
	if format == "jpeg" {
		p.orient = exifOrientation(head)
	}
	dispW, dispH := cfg.Width, cfg.Height
	if p.orient >= 5 {
		dispW, dispH = dispH, dispW
	}
	if dispW <= width {
		return p, pass("already %d px wide or less", width)
	}
	newW := width
	newH := max(1, int((int64(dispH)*int64(newW)+int64(dispW)/2)/int64(dispW)))
	p.rw, p.rh = newW, newH
	if p.orient >= 5 {
		p.rw, p.rh = newH, newW
	}
	p.need = thumbCost(r, size, format, p.w, p.h, p.rw, p.rh, p.orient != 1)
	return p, nil
}

// transcode makes a ThumbWidth-wide thumbnail of the image in r (size bytes,
// content type ct): JPEG at quality 80, or PNG when the picture has
// transparency. Metadata is dropped (an EXIF orientation is applied to the
// pixels first). It never upscales. When there is nothing to gain or the image
// is unsafe to decode (its estimated cost is over the ceiling) it returns a
// *passError and the original is served.
func transcode(r io.ReaderAt, size int64, ct string, lim thumbLimits) (out []byte, outType string, err error) {
	p, err := planThumb(r, size, ct, lim.width, lim.maxPixels)
	if err != nil {
		return nil, "", err
	}
	if p.need > lim.ceiling {
		return nil, "", pass("decoding needs about %d MiB", p.need>>20)
	}
	lim.budget.acquire(p.need)
	defer lim.budget.release(p.need)
	return render(r, size, p)
}

// render decodes, scales, orients and encodes as planned.
func render(r io.ReaderAt, size int64, p thumbPlan) (out []byte, outType string, err error) {
	if testHookBeforeDecode != nil {
		testHookBeforeDecode()
	}
	dst, err := decodeScale(io.NewSectionReader(r, 0, size), p.rw, p.rh)
	if err != nil {
		return nil, "", pass("decode failed")
	}
	final := dst
	if p.orient != 1 {
		final = orientImage(dst, p.orient)
	}
	var buf bytes.Buffer
	if final.Opaque() {
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
//
// Every intermediate and the result are *image.RGBA (premultiplied alpha,
// which keeps transparency and is the right space to average in): x/image/draw
// writes those directly, while any other destination type goes through
// dst.Set and allocates a boxed color per pixel.
func decodeScale(r io.Reader, w, h int) (*image.RGBA, error) {
	src, _, err := image.Decode(bufio.NewReaderSize(r, 64<<10))
	if err != nil {
		return nil, err
	}
	cur := scalable(src)
	for b := cur.Bounds(); b.Dx() >= 2*w && b.Dy() >= 2*h; b = cur.Bounds() {
		next := image.NewRGBA(image.Rect(0, 0, b.Dx()/2, b.Dy()/2))
		xdraw.ApproxBiLinear.Scale(next, next.Bounds(), cur, b, xdraw.Src, nil)
		cur = next
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), cur, cur.Bounds(), xdraw.Src, nil)
	return dst, nil
}

// rgba64Only hides a picture's concrete type so x/image/draw reads it through
// RGBA64At (a plain value) instead of At (a color boxed on the heap for every
// sample).
type rgba64Only struct{ image.RGBA64Image }

// scalable returns src as x/image/draw can scale it without per-pixel
// allocations: the types it has fast paths for as they are, anything else
// (CMYK, 16-bit, paletted, NYCbCrA, 4:1:1 YCbCr) through RGBA64At.
func scalable(src image.Image) image.Image {
	switch s := src.(type) {
	case *image.Gray, *image.NRGBA, *image.RGBA:
		return src
	case *image.YCbCr:
		switch s.SubsampleRatio {
		case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio440:
			return src
		}
	}
	if s, ok := src.(image.RGBA64Image); ok {
		return rgba64Only{s}
	}
	return src
}

// webpAnimated reports a VP8X header with the animation flag.
func webpAnimated(b []byte) bool {
	return len(b) >= 21 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" && string(b[12:16]) == "VP8X" && b[20]&0x02 != 0
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

// orientImage returns src with the EXIF orientation o (2 to 8) applied. It
// copies pixel bytes, so it allocates nothing but the result.
func orientImage(src *image.RGBA, o int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
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
			si, di := src.PixOffset(b.Min.X+sx, b.Min.Y+sy), dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
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
