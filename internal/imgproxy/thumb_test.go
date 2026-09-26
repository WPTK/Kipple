package imgproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/imgcache"
)

// noise is a w x h image of random pixels: incompressible, so even a modest
// size clears the 150 KB threshold. alpha makes the top-left quarter transparent.
func noise(w, h int, alpha bool) *image.NRGBA {
	rng := rand.New(rand.NewPCG(1, 2))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		v := rng.Uint32()
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(v), byte(v>>8), byte(v>>16), 255
	}
	if alpha {
		for y := 0; y < h/2; y++ {
			for x := 0; x < w/2; x++ {
				img.Pix[y*img.Stride+x*4+3] = 0
			}
		}
	}
	return img
}

func jpegOf(t testing.TB, img image.Image, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, jpeg.Encode(&b, img, &jpeg.Options{Quality: q}))
	return b.Bytes()
}

func pngOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, img))
	return b.Bytes()
}

var (
	bigJPEGOnce sync.Once
	bigJPEG     []byte
)

// sampleJPEG is a 2400x1600 noise JPEG (a few MB).
func sampleJPEG(t testing.TB) []byte {
	bigJPEGOnce.Do(func() { bigJPEG = jpegOf(t, noise(2400, 1600, false), 90) })
	return bigJPEG
}

func testLimits() thumbLimits {
	return thumbLimits{width: ThumbWidth, maxPixels: defaultThumbPixels, ceiling: defaultDecodeCeiling, budget: newBudget(defaultDecodeBudget)}
}

func runTranscode(t testing.TB, data []byte, ct string) ([]byte, string, error) {
	t.Helper()
	return transcode(bytes.NewReader(data), int64(len(data)), ct, testLimits())
}

func decodeCfg(t testing.TB, data []byte) (image.Config, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	return cfg, format
}

func TestTranscodeJPEGSizeFormatAndMetadata(t *testing.T) {
	src := sampleJPEG(t)
	out, ct, err := runTranscode(t, src, "image/jpeg")
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", ct)
	cfg, format := decodeCfg(t, out)
	require.Equal(t, "jpeg", format)
	require.Equal(t, 800, cfg.Width)
	require.Equal(t, 533, cfg.Height, "aspect ratio kept (2400x1600 -> 800x533)")
	require.Less(t, len(out), len(src))
	require.False(t, bytes.Contains(out, []byte("Exif")), "metadata is stripped")
	require.False(t, bytes.Contains(out, []byte("ICC_PROFILE")))
}

func TestTranscodePNGAlphaStaysPNGOpaqueBecomesJPEG(t *testing.T) {
	withAlpha := pngOf(t, noise(1200, 800, true))
	out, ct, err := runTranscode(t, withAlpha, "image/png")
	require.NoError(t, err)
	require.Equal(t, "image/png", ct)
	img, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, 800, img.Bounds().Dx())
	require.Equal(t, 533, img.Bounds().Dy())
	_, _, _, a := img.At(10, 10).RGBA()
	require.Zero(t, a, "the transparent corner stays transparent")
	_, _, _, a = img.At(700, 500).RGBA()
	require.EqualValues(t, 0xffff, a)

	opaque := pngOf(t, noise(1200, 800, false))
	out, ct, err = runTranscode(t, opaque, "image/png")
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", ct, "no transparency: JPEG")
	cfg, _ := decodeCfg(t, out)
	require.Equal(t, 800, cfg.Width)
}

func TestTranscodeNeverUpscalesAndSkipsSmall(t *testing.T) {
	var pe *passError
	// Exactly the target width, and narrower: nothing to shrink.
	for _, w := range []int{800, 700} {
		data := jpegOf(t, noise(w, 700, false), 95)
		require.Greater(t, len(data), thumbMinSource)
		_, _, err := runTranscode(t, data, "image/jpeg")
		require.ErrorAs(t, err, &pe, "width %d", w)
	}
	// Wide but tiny in bytes: the original is already cheap.
	small := jpegOf(t, image.NewGray(image.Rect(0, 0, 1600, 1000)), 80)
	require.Less(t, len(small), thumbMinSource)
	_, _, err := runTranscode(t, small, "image/jpeg")
	require.ErrorAs(t, err, &pe)
}

func TestTranscodeAnimatedAndUnsupportedPassThrough(t *testing.T) {
	var pe *passError
	var g bytes.Buffer
	pal := color.Palette{color.Black, color.White}
	require.NoError(t, gif.EncodeAll(&g, &gif.GIF{
		Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), pal), image.NewPaletted(image.Rect(0, 0, 4, 4), pal)},
		Delay: []int{5, 5},
	}))
	_, _, err := runTranscode(t, g.Bytes(), "image/gif")
	require.ErrorAs(t, err, &pe, "GIF is served as the original")
	_, _, err = runTranscode(t, avifBytes("avif"), "image/avif")
	require.ErrorAs(t, err, &pe)

	anim := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x12"), bytes.Repeat([]byte{0}, 300<<10)...) // VP8X with the animation bit
	require.True(t, webpAnimated(anim))
	_, _, err = runTranscode(t, anim, "image/webp")
	require.ErrorAs(t, err, &pe, "animated WebP is served as the original")
	require.False(t, webpAnimated(append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x10"), 0)))
}

// bombPNG is a PNG whose header claims w x h pixels, padded past 150 KB. Only
// DecodeConfig ever reads it: decoding it for real would need gigabytes.
func bombPNG(w, h uint32) []byte {
	chunk := func(kind string, data []byte) []byte {
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
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", ihdr)...)
	return append(out, chunk("IDAT", bytes.Repeat([]byte{0x78}, 200<<10))...)
}

func TestTranscodeRefusesDecompressionBomb(t *testing.T) {
	var pe *passError
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, err := runTranscode(t, bombPNG(30000, 30000), "image/png") // 900 MP
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.reason, "megapixels")
	_, _, err = runTranscode(t, bombPNG(5000, 4801), "image/png") // 24.005 MP, just over the cap
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.reason, "megapixels")
	runtime.ReadMemStats(&after)
	require.Less(t, int64(after.TotalAlloc-before.TotalAlloc), int64(16<<20), "refused from the header, before any decode")
}

func TestTranscodeMemoryCeilingRefusesHeavyDecode(t *testing.T) {
	var pe *passError
	// A 20 MP PNG needs about 160 MiB by the estimate: over the 96 MiB ceiling.
	_, _, err := runTranscode(t, bombPNG(5000, 4000), "image/png")
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.reason, "MiB")
}

// jpegWithOrientation splices an EXIF APP1 segment with the orientation tag into a JPEG.
func jpegWithOrientation(jpg []byte, o uint16) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, byte(o >> 8), byte(o), 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	app1 = append(app1, seg...)
	return append(append(append([]byte{}, jpg[:2]...), app1...), jpg[2:]...)
}

func TestTranscodeAppliesExifOrientation(t *testing.T) {
	base := sampleJPEG(t) // 2400x1600
	require.Equal(t, 1, exifOrientation(base))
	for _, tc := range []struct {
		o    uint16
		w, h int
	}{{1, 800, 533}, {3, 800, 533}, {6, 800, 1200}, {8, 800, 1200}} {
		src := jpegWithOrientation(base, tc.o)
		require.EqualValues(t, tc.o, exifOrientation(src))
		out, _, err := runTranscode(t, src, "image/jpeg")
		require.NoError(t, err, "orientation %d", tc.o)
		cfg, _ := decodeCfg(t, out)
		require.Equal(t, tc.w, cfg.Width, "orientation %d", tc.o)
		require.Equal(t, tc.h, cfg.Height, "orientation %d", tc.o)
	}
}

func TestOrientImageMovesPixels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	src.Set(0, 0, color.RGBA{R: 255, A: 255}) // top-left marker
	got := func(o int) [2]int {
		img := orientImage(src, o)
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				if r, _, _, _ := img.At(x, y).RGBA(); r != 0 {
					return [2]int{x, y}
				}
			}
		}
		return [2]int{-1, -1}
	}
	require.Equal(t, [2]int{1, 0}, got(2), "mirrored: top-right")
	require.Equal(t, [2]int{1, 2}, got(3), "rotated 180: bottom-right")
	require.Equal(t, [2]int{2, 0}, got(6), "rotated 90 clockwise: top-right of the 3x2 result")
	require.Equal(t, [2]int{0, 1}, got(8), "rotated 90 counter-clockwise: bottom-left")
}

func TestTranscodeHeapStaysUnderCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 20 megapixel JPEG")
	}
	yc := image.NewYCbCr(image.Rect(0, 0, 5000, 4000), image.YCbCrSubsampleRatio420)
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range yc.Y {
		yc.Y[i] = byte(i>>7) ^ byte(rng.Uint32()&15)
	}
	for i := range yc.Cb {
		yc.Cb[i], yc.Cr[i] = byte(i>>9), byte(i>>8)
	}
	src := jpegOf(t, yc, 85)
	yc = nil //nolint:ineffassign,wastedassign // free the source picture before measuring
	require.Greater(t, len(src), thumbMinSource)

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	out, _, err := runTranscode(t, src, "image/jpeg")
	took := time.Since(start)
	close(stop)
	wg.Wait()
	require.NoError(t, err)
	cfg, _ := decodeCfg(t, out)
	require.Equal(t, 800, cfg.Width)
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	grew := int64(peak.Load()) - int64(base.HeapAlloc)
	allocated := int64(end.TotalAlloc - base.TotalAlloc)
	p, err := planThumb(bytes.NewReader(src), int64(len(src)), "image/jpeg", ThumbWidth, defaultThumbPixels)
	require.NoError(t, err)
	t.Logf("20 MP JPEG (%d KB) -> %d KB in %s; heap peak +%d MiB (includes uncollected garbage), allocated in total %d MiB, estimate %d MiB",
		len(src)>>10, len(out)>>10, took, grew>>20, allocated>>20, p.need>>20)
	// Every byte the transcode allocates, garbage included, fits the ceiling, so no GC timing can push the heap past it.
	require.Less(t, allocated, int64(defaultDecodeCeiling), "one decode stays under the ceiling")
	require.LessOrEqual(t, allocated, p.need, "the estimate that admits a decode is honest")
}

func TestBudgetSerializesDecodes(t *testing.T) {
	b := newBudget(100)
	b.acquire(60)
	got := make(chan struct{})
	go func() { b.acquire(60); close(got) }()
	select {
	case <-got:
		t.Fatal("second acquire should wait for the first release")
	case <-time.After(50 * time.Millisecond):
	}
	b.release(60)
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("release did not wake the waiter")
	}
}

func TestPoolBoundsWorkersAndQueue(t *testing.T) {
	p := newPool(2, 1)
	block := make(chan struct{})
	var running, maxRun atomic.Int32
	job := func() {
		n := running.Add(1)
		for {
			m := maxRun.Load()
			if n <= m || maxRun.CompareAndSwap(m, n) {
				break
			}
		}
		<-block
		running.Add(-1)
	}
	require.NoError(t, p.submit(job))
	require.Eventually(t, func() bool { return running.Load() == 1 }, 2*time.Second, time.Millisecond)
	require.NoError(t, p.submit(job))
	require.Eventually(t, func() bool { return running.Load() == 2 }, 2*time.Second, time.Millisecond)
	require.NoError(t, p.submit(job), "one job may wait")
	require.ErrorIs(t, p.submit(job), errThumbQueueFull, "a full queue refuses at once")
	close(block)
	p.close()
	require.EqualValues(t, 2, maxRun.Load(), "never more than two at a time")
	require.ErrorIs(t, p.submit(job), errThumbQueueFull, "a closed pool refuses")
}

// ---- signed URLs ----

func TestSignatureStableForOldURLs(t *testing.T) {
	// Pinned when FlagThumb was added: URLs signed before it must verify forever.
	require.Equal(t, "mU71Lnop4kdyLt6FW0-ps6", Sign(secret, 0, "https://example.com/pic.jpg"))
	require.Equal(t, "29q1lRz9ekFm-79uM7E8Yi", Sign(secret, FlagPrivateNet|FlagInsecureTLS, "http://example.com/a.png"))
}

func TestThumbFlagIsSignedAndSeparate(t *testing.T) {
	const orig = "https://example.com/pic.jpg"
	require.NotEqual(t, Sign(secret, 0, orig), Sign(secret, FlagThumb, orig))
	plain := Rewriter{Secret: secret, Flags: FlagPrivateNet, All: true}.Rewrite(orig)
	thumb := Rewriter{Secret: secret, Flags: FlagPrivateNet, All: true, Thumb: true}.Rewrite(orig)
	require.Equal(t, Path(secret, FlagPrivateNet, orig), plain)
	require.Equal(t, Path(secret, FlagPrivateNet|FlagThumb, orig), thumb)
	require.NotEqual(t, plain, thumb)
	require.Contains(t, thumb, "/"+strconv.Itoa(FlagPrivateNet|FlagThumb)+"/")

	// An old URL cannot be upgraded to a thumbnail (or the reverse) by editing the flags.
	rg := newRig(t)
	up := upstream(t, serve("image/png", pngBytes))
	path := Path(secret, 0, up.URL+"/a.png")
	tampered := bytes.Replace([]byte(path), []byte("/0/"), []byte("/4/"), 1)
	require.Equal(t, 403, rg.get(string(tampered)).StatusCode)
	require.Equal(t, 400, rg.get(Path(secret, 8, up.URL+"/a.png")).StatusCode, "unknown flag bits are refused")
}

// ---- through the handler ----

type thumbRig struct {
	*cacheRig
	up  *countingUpstream
	src []byte
}

func newThumbRig(t *testing.T, ct string, src []byte, tune ...func(*Options)) *thumbRig {
	t.Helper()
	// A generous wait by default: the race detector makes a decode several times slower.
	tune = append([]func(*Options){func(o *Options) { o.ThumbWait = 60 * time.Second }}, tune...)
	tr := &thumbRig{cacheRig: newCacheRig(t, tune...), src: src}
	t.Cleanup(tr.h.Close)
	tr.up = &countingUpstream{}
	tr.up.Server = upstream(t, func(w http.ResponseWriter, r *http.Request) {
		tr.up.n.Add(1)
		w.Header().Set("Content-Type", ct)
		w.Header().Set("ETag", `"src"`)
		_, _ = w.Write(src)
	})
	return tr
}

func (tr *thumbRig) thumb(name string, hdr ...string) *http.Response {
	return tr.get(Path(secret, FlagPrivateNet|FlagThumb, tr.up.URL+"/"+name), hdr...)
}

func (tr *thumbRig) orig(name string) *http.Response {
	return tr.fetchOrig(tr.up.URL+"/"+name, FlagPrivateNet)
}

func (tr *thumbRig) thumbEntry(name string) (imgcache.Entry, bool) {
	return tr.cache.Peek(context.Background(), imgcache.KeyThumb(FlagPrivateNet, tr.up.URL+"/"+name))
}

func (tr *thumbRig) origEntry(name string) (imgcache.Entry, bool) {
	return tr.cache.Peek(context.Background(), imgcache.KeyOrig(FlagPrivateNet, tr.up.URL+"/"+name))
}

func TestThumbServedCachedAndCoexistsWithOriginal(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))

	resp := tr.thumb("a.jpg")
	require.Equal(t, 200, resp.StatusCode)
	body := read(t, resp)
	require.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
	cfg, _ := decodeCfg(t, body)
	require.Equal(t, 800, cfg.Width)
	require.Less(t, len(body), len(tr.src)/4)
	require.EqualValues(t, 1, tr.up.n.Load())

	te, ok := tr.thumbEntry("a.jpg")
	require.True(t, ok)
	require.True(t, te.OK)
	require.Equal(t, int64(len(body)), te.Size)
	require.Equal(t, `"`+te.SHA256[:16]+`"`, resp.Header.Get("ETag"))
	oe, ok := tr.origEntry("a.jpg")
	require.True(t, ok, "the original was cached on the way")
	require.Equal(t, int64(len(tr.src)), oe.Size)
	require.NotEqual(t, te.Key, oe.Key, "separate cache keys")

	// A second thumbnail request and an original request cost no upstream fetch.
	require.Equal(t, body, read(t, tr.thumb("a.jpg")))
	require.Equal(t, tr.src, read(t, tr.orig("a.jpg")), "the original is still served whole")
	require.EqualValues(t, 1, tr.up.n.Load())

	st := tr.cache.Stats()
	require.EqualValues(t, 1, st.Thumbnails)
	require.EqualValues(t, 2, st.Files)
	require.Equal(t, te.Size+oe.Size, st.UsedBytes, "both count toward the cap")

	// Conditional and Range requests work on thumbnails.
	require.Equal(t, 304, tr.thumb("a.jpg", "If-None-Match", `"`+te.SHA256[:16]+`"`).StatusCode)
	require.Equal(t, 206, tr.thumb("a.jpg", "Range", "bytes=0-3").StatusCode)
}

func TestThumbOriginalFirstThenThumbReusesFetch(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	require.Equal(t, tr.src, read(t, tr.orig("b.jpg")))
	cfg, _ := decodeCfg(t, read(t, tr.thumb("b.jpg")))
	require.Equal(t, 800, cfg.Width)
	require.EqualValues(t, 1, tr.up.n.Load(), "the thumbnail is made from the cached original")
}

func TestThumbEvictionAccounting(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	_ = read(t, tr.thumb("a.jpg"))
	te, _ := tr.thumbEntry("a.jpg")
	require.Greater(t, tr.cache.Stats().UsedBytes, te.Size)
	// The thumbnail is the more recently used of the two (the clock is frozen, so move it on).
	tr.clk.Advance(time.Minute)
	_ = read(t, tr.thumb("a.jpg"))
	// A cap that fits the thumbnail but not the original: the older original goes first.
	tr.cache.SetCap(te.Size * 2)
	require.Eventually(t, func() bool { return tr.cache.Stats().UsedBytes <= tr.cache.MaxBytes() }, 2*time.Second, 5*time.Millisecond)
	st := tr.cache.Stats()
	var sum, files int64
	for _, e := range []func(string) (imgcache.Entry, bool){tr.origEntry, tr.thumbEntry} {
		if en, ok := e("a.jpg"); ok && en.OK {
			sum += en.Size
			files++
		}
	}
	require.Equal(t, sum, st.UsedBytes, "the counters match what is left")
	require.Equal(t, files, st.Files)
	_, origLeft := tr.origEntry("a.jpg")
	require.False(t, origLeft, "the original was the eviction victim")
	require.EqualValues(t, 1, st.Thumbnails)
}

func TestThumbFallbacksAreRememberedNotRetried(t *testing.T) {
	cases := []struct {
		name, ct string
		body     []byte
	}{
		{"animated gif", "image/gif", append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 300<<10)...)},
		{"bomb", "image/png", bombPNG(30000, 30000)},
		{"undecodable jpeg", "image/jpeg", append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), bytes.Repeat([]byte{7}, 300<<10)...)},
		{"small", "image/jpeg", jpegOf(t, noise(900, 20, false), 90)},
		{"avif", "image/avif", append(avifBytes("avif"), bytes.Repeat([]byte{1}, 300<<10)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newThumbRig(t, tc.ct, tc.body)
			for i := 0; i < 3; i++ {
				resp := tr.thumb("x")
				require.Equal(t, 200, resp.StatusCode)
				require.Equal(t, tc.body, read(t, resp), "the original is served")
			}
			require.EqualValues(t, 1, tr.up.n.Load(), "one fetch for all three")
			te, ok := tr.thumbEntry("x")
			require.True(t, ok)
			require.False(t, te.OK, "the refusal is remembered (negative entry), so it is not retried")
			require.EqualValues(t, 0, tr.cache.Stats().Thumbnails)
		})
	}
}

func TestThumbQueueFullTimeoutAndLateThumbnail(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t), func(o *Options) {
		o.ThumbWorkers, o.ThumbQueue, o.ThumbWait = 1, 1, 300*time.Millisecond
	})
	// Hold the whole decode allowance so the worker blocks inside its first job.
	tr.h.lim.budget.acquire(defaultDecodeBudget)

	slow := time.Now()
	require.Equal(t, tr.src, read(t, tr.thumb("1.jpg")), "worker busy past the wait: the original")
	require.GreaterOrEqual(t, time.Since(slow), 250*time.Millisecond)
	require.Equal(t, tr.src, read(t, tr.thumb("2.jpg")), "queued behind it: the same")
	// The worker holds job 1 and job 2 fills the queue: job 3 is refused at once.
	fast := time.Now()
	resp := tr.thumb("3.jpg")
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, tr.src, read(t, resp), "queue full: the original, immediately")
	require.Less(t, time.Since(fast), 250*time.Millisecond)
	_, ok := tr.thumbEntry("3.jpg")
	require.False(t, ok, "a full queue is not remembered as a failure")

	// Let the workers finish: the late thumbnails are there for the next request.
	tr.h.lim.budget.release(defaultDecodeBudget)
	require.Eventually(t, func() bool { e, ok := tr.thumbEntry("1.jpg"); return ok && e.OK }, 90*time.Second, 10*time.Millisecond)
	cfg, _ := decodeCfg(t, read(t, tr.thumb("1.jpg")))
	require.Equal(t, 800, cfg.Width)
}

func TestThumbConcurrentRequestsShareOneFetchAndOneTranscode(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t), func(o *Options) { o.ThumbWait = 30 * time.Second })
	const n = 20
	bodies := make([][]byte, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", tr.srv.URL+Path(secret, FlagPrivateNet|FlagThumb, tr.up.URL+"/c.jpg"), nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			bodies[i], _ = io.ReadAll(resp.Body)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, tr.up.n.Load(), "one upstream fetch")
	for i := range bodies {
		require.Equal(t, bodies[0], bodies[i])
	}
	cfg, _ := decodeCfg(t, bodies[0])
	require.Equal(t, 800, cfg.Width, "everyone got the thumbnail")
	require.EqualValues(t, 1, tr.cache.Stats().Thumbnails)
}

func TestThumbStaleThumbnailKeepsUnchangedSource(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	first := read(t, tr.thumb("s.jpg"))
	te, _ := tr.thumbEntry("s.jpg")
	tr.clk.Advance(40 * 24 * time.Hour) // both entries are stale now
	require.Equal(t, first, read(t, tr.thumb("s.jpg")))
	te2, ok := tr.thumbEntry("s.jpg")
	require.True(t, ok)
	require.Equal(t, te.SHA256, te2.SHA256, "the source is the same: the thumbnail is kept, not remade")
	require.True(t, te2.Fresh(tr.cache.Now()))
}

func TestThumbWithoutCacheServesOriginal(t *testing.T) {
	up := upstream(t, serve("image/jpeg", sampleJPEG(t)))
	rg := newRig(t) // no cache
	resp := rg.get(Path(secret, FlagPrivateNet|FlagThumb, up.URL+"/a.jpg"))
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, sampleJPEG(t), read(t, resp))
}

func TestThumbAfterCloseServesOriginal(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	tr.h.Close()
	require.Equal(t, tr.src, read(t, tr.thumb("z.jpg")))
}
