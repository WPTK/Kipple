package favicon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif"  // register the decoder for DecodeConfig
	_ "image/jpeg" // register the decoder for DecodeConfig
	_ "image/png"  // register the decoder for DecodeConfig

	_ "golang.org/x/image/webp" // register the decoder for DecodeConfig
)

const (
	minSide = 8    // smaller is a tracking pixel or a placeholder, not an icon
	maxSide = 2048 // larger is not a favicon
)

var errNotIcon = errors.New("not a PNG, JPEG, GIF, WebP or ICO image")

// sniff identifies an icon by its bytes alone (the server's Content-Type is
// ignored: favicon.ico is routinely served as text/plain or octet-stream, and an
// SVG or HTML page can claim image/png). Only raster types pass: PNG, JPEG, GIF,
// WebP and ICO. The header must also parse, with sides between minSide and maxSide.
func sniff(b []byte) (contentType string, err error) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		contentType = "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		contentType = "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		contentType = "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		contentType = "image/webp"
	case len(b) >= 6 && binary.LittleEndian.Uint16(b[0:2]) == 0 && binary.LittleEndian.Uint16(b[2:4]) == 1:
		if err := checkICO(b); err != nil {
			return "", err
		}
		return "image/x-icon", nil
	default:
		return "", errNotIcon
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", errors.New("the image header does not parse")
	}
	if cfg.Width < minSide || cfg.Height < minSide || cfg.Width > maxSide || cfg.Height > maxSide {
		return "", errors.New("the image is too small or too large to be an icon")
	}
	return contentType, nil
}

// checkICO validates an ICO directory: 1 to 64 entries, each image's bytes
// inside the file. Sides are one byte each (0 means 256), so they need no cap.
func checkICO(b []byte) error {
	n := int(binary.LittleEndian.Uint16(b[4:6]))
	if n < 1 || n > 64 || len(b) < 6+16*n {
		return errors.New("the ICO directory is malformed")
	}
	for i := 0; i < n; i++ {
		e := b[6+16*i : 6+16*(i+1)]
		size := int64(binary.LittleEndian.Uint32(e[8:12]))
		off := int64(binary.LittleEndian.Uint32(e[12:16]))
		if size == 0 || off < int64(6+16*n) || off+size > int64(len(b)) {
			return errors.New("the ICO directory points outside the file")
		}
	}
	return nil
}
