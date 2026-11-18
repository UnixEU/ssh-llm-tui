package uploads

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func TestSniffImageMagic(t *testing.T) {
	png := mustPNG(t, 4, 4)
	if SniffImage(png) != "png" {
		t.Fatalf("png sniff %q", SniffImage(png))
	}
	if SniffImage([]byte{0xff, 0xd8, 0xff, 0x00}) != "jpeg" {
		t.Fatal("jpeg magic")
	}
	if SniffImage([]byte("GIF89a")) != "gif" {
		t.Fatal("gif magic")
	}
	webp := append([]byte("RIFF"), make([]byte, 4)...)
	webp = append(webp, []byte("WEBP")...)
	if SniffImage(webp) != "webp" {
		t.Fatal("webp magic")
	}
	if SniffImage([]byte("hello.png")) != "" {
		t.Fatal("extension is not enough")
	}
}

func TestPrepareImageDownscales(t *testing.T) {
	src := mustPNG(t, 64, 32)
	got, ok, err := PrepareImage("wide.png", src, 16, 64*1024)
	if !ok || err != nil {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if got.Width > 16 || got.Height > 16 {
		t.Fatalf("size %dx%d", got.Width, got.Height)
	}
	if got.Width < 1 || got.Height < 1 || len(got.Data) == 0 {
		t.Fatalf("%+v", got)
	}
	if got.MIME != "image/jpeg" && got.MIME != "image/png" {
		t.Fatalf("mime %s", got.MIME)
	}
	if SniffImage(got.Data) == "" {
		t.Fatal("encoded payload is not an image")
	}
}

func TestPrepareImageGIFFirstFrame(t *testing.T) {
	var buf bytes.Buffer
	g := &gif.GIF{
		Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 20, 10), color.Palette{color.Black, color.White})},
		Delay: []int{0},
	}
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	got, ok, err := PrepareImage("a.gif", buf.Bytes(), 8, 64*1024)
	if !ok || err != nil {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if got.Width > 8 || got.Height > 8 {
		t.Fatalf("size %dx%d", got.Width, got.Height)
	}
}

func TestPutImageListsAndSendsOnce(t *testing.T) {
	s := New(t.TempDir(), 5<<20, 10)
	s.SetImageLimits(32, 256<<10)
	if err := s.Put("user", "shot.png", mustPNG(t, 40, 20)); err != nil {
		t.Fatal(err)
	}
	files := s.List("user")
	if len(files) != 1 || !files[0].IsImage || files[0].Width > 32 || files[0].Note != "" {
		t.Fatalf("%+v", files)
	}
	pending := s.PendingImages("user")
	if len(pending) != 1 || pending[0].Name != "shot.png" {
		t.Fatalf("%+v", pending)
	}
	desc := s.Describe("user")
	if !bytes.Contains([]byte(desc), []byte("shot.png")) || !bytes.Contains([]byte(desc), []byte("image")) {
		t.Fatalf("describe %q", desc)
	}
	s.MarkSent("user", []string{"shot.png"})
	if again := s.PendingImages("user"); len(again) != 0 {
		t.Fatalf("still pending %+v", again)
	}
	if err := s.SetIncluded("user", "shot.png", true); err != nil {
		t.Fatal(err)
	}
	if again := s.PendingImages("user"); len(again) != 1 {
		t.Fatal("re-include should queue the image again")
	}
}

func mustPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
