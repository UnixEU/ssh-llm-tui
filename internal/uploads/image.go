package uploads

import (
	"bytes"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
)

const (
	DefaultImageMaxPx    = 1536
	DefaultImageMaxBytes = 2 << 20
)

// PreparedImage is the payload actually sent to the model.
type PreparedImage struct {
	Name   string
	MIME   string
	Width  int
	Height int
	Data   []byte
}

// SniffImage reports png, jpeg, gif, or webp from magic bytes.
func SniffImage(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		return "png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "jpeg"
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return "gif"
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "webp"
	}
	return ""
}

// PrepareImage decodes an image, downscales the longest side to maxPx, and
// re-encodes it. GIF uses the first frame. ok is false when the bytes are
// not a supported image.
func PrepareImage(name string, data []byte, maxPx int, maxBytes int) (PreparedImage, bool, error) {
	kind := SniffImage(data)
	if kind == "" {
		return PreparedImage{}, false, nil
	}
	if maxPx <= 0 {
		maxPx = DefaultImageMaxPx
	}
	if maxBytes <= 0 {
		maxBytes = DefaultImageMaxBytes
	}
	img, err := decodeImage(kind, data)
	if err != nil {
		return PreparedImage{}, true, err
	}
	img = fitImage(img, maxPx)
	encoded, err := encodeFit(img, maxBytes)
	if err != nil {
		return PreparedImage{}, true, err
	}
	b := encoded.Bounds()
	return PreparedImage{
		Name:   name,
		MIME:   encoded.mime,
		Width:  b.Dx(),
		Height: b.Dy(),
		Data:   encoded.data,
	}, true, nil
}

type encodedImage struct {
	mime string
	data []byte
	w, h int
}

func (e encodedImage) Bounds() image.Rectangle { return image.Rect(0, 0, e.w, e.h) }

func decodeImage(kind string, data []byte) (image.Image, error) {
	r := bytes.NewReader(data)
	switch kind {
	case "png":
		return png.Decode(r)
	case "jpeg":
		return jpeg.Decode(r)
	case "gif":
		return gif.Decode(r)
	case "webp":
		return webp.Decode(r)
	default:
		return nil, image.ErrFormat
	}
}

func fitImage(src image.Image, maxPx int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return src
	}
	long := w
	if h > long {
		long = h
	}
	if long <= maxPx {
		return src
	}
	nw := maxInt(1, w*maxPx/long)
	nh := maxInt(1, h*maxPx/long)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

func encodeFit(img image.Image, maxBytes int) (encodedImage, error) {
	cur := img
	for attempt := 0; attempt < 6; attempt++ {
		if mostlyOpaque(cur) {
			for _, q := range []int{85, 70, 55, 40} {
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, cur, &jpeg.Options{Quality: q}); err != nil {
					return encodedImage{}, err
				}
				if buf.Len() <= maxBytes || q == 40 {
					if buf.Len() <= maxBytes {
						return encodedImage{mime: "image/jpeg", data: buf.Bytes(), w: cur.Bounds().Dx(), h: cur.Bounds().Dy()}, nil
					}
				}
			}
		} else {
			var buf bytes.Buffer
			if err := png.Encode(&buf, cur); err != nil {
				return encodedImage{}, err
			}
			if buf.Len() <= maxBytes {
				return encodedImage{mime: "image/png", data: buf.Bytes(), w: cur.Bounds().Dx(), h: cur.Bounds().Dy()}, nil
			}
		}
		// Still too big: shrink and try again.
		nw := maxInt(1, cur.Bounds().Dx()*3/4)
		nh := maxInt(1, cur.Bounds().Dy()*3/4)
		if nw == cur.Bounds().Dx() && nh == cur.Bounds().Dy() {
			break
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.BiLinear.Scale(dst, dst.Bounds(), cur, cur.Bounds(), draw.Over, nil)
		cur = dst
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, cur, &jpeg.Options{Quality: 40})
	return encodedImage{mime: "image/jpeg", data: buf.Bytes(), w: cur.Bounds().Dx(), h: cur.Bounds().Dy()}, nil
}

func mostlyOpaque(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 8 {
		for x := b.Min.X; x < b.Max.X; x += 8 {
			_, _, _, a := img.At(x, y).RGBA()
			if a < 0xff00 {
				return false
			}
		}
	}
	return true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
