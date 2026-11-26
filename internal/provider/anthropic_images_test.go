package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// testImage is a w×h image encoded in format, as base64.
func testImage(t *testing.T, format string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 7 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&b, img)
	case "jpeg":
		err = jpeg.Encode(&b, img, nil)
	case "gif":
		err = gif.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// webpImage is golang.org/x/image's gopher-doc.1bpp.lossless.webp, base64
// (nothing in Go writes WebP).
const webpImage = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="

func imageSize(t *testing.T, data string) (int, int, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height, format
}

func imageBlock(mediaType, data string) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}}
}

func TestFittedImageSides(t *testing.T) {
	cases := []struct {
		name, format, mediaType string
		w, h, side              int
		wantW, wantH            int // 0: goes as it came
		wantType                string
	}{
		{"fits", "png", "image/png", 4000, 100, 8000, 0, 0, ""},
		{"tall png", "png", "image/png", 400, 8192, 8000, 390, 8000, "image/png"},
		{"wide jpeg", "jpeg", "image/jpeg", 8100, 50, 8000, 8000, 49, "image/jpeg"},
		{"gif becomes png", "gif", "image/gif", 2400, 1200, 2000, 2000, 1000, "image/png"},
	}
	for _, c := range cases {
		data := testImage(t, c.format, c.w, c.h)
		budget := claudeScaledPixels
		mt, out, ok := fittedImage(c.mediaType, data, c.side, &budget)
		if c.wantW == 0 {
			if ok {
				t.Errorf("%s: changed", c.name)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s: not fitted", c.name)
		}
		w, h, _ := imageSize(t, out)
		if w != c.wantW || h != c.wantH || mt != c.wantType {
			t.Errorf("%s: %dx%d %s, want %dx%d %s", c.name, w, h, mt, c.wantW, c.wantH, c.wantType)
		}
	}
	budget := claudeScaledPixels
	if _, _, ok := fittedImage("image/png", "not base64!", 10, &budget); ok {
		t.Error("an unreadable image was changed")
	}
}

// What a buyer's images may cost the seller before the model server is even
// called is bounded: an image over the pixel limit is not decoded, and a
// request has a budget of pixels to scale.
func TestScalingABuyersImagesIsBounded(t *testing.T) {
	budget := claudeScaledPixels
	huge := testImage(t, "png", 8100, 2000) // 16.2 M pixels in a few kilobytes
	if _, _, ok := fittedImage("image/png", huge, 8000, &budget); ok || budget != claudeScaledPixels {
		t.Fatalf("an image over the pixel limit was decoded (budget %d)", budget)
	}
	// Tall and narrow: the smooth scaler's buffer would be over its limit.
	tall := testImage(t, "png", 1000, 15000)
	if _, out, ok := fittedImage("image/png", tall, 8000, &budget); !ok {
		t.Fatal("a 15 M pixel image was not fitted")
	} else if w, h, _ := imageSize(t, out); w != 533 || h != 8000 {
		t.Fatalf("fitted to %dx%d", w, h)
	}
	if budget != claudeScaledPixels-15_000_000 {
		t.Fatalf("budget %d", budget)
	}
	// Out of budget: the next image goes as it came; one scaled before is remembered.
	budget = 1000
	if _, _, ok := fittedImage("image/png", testImage(t, "png", 8200, 31), 8000, &budget); ok {
		t.Fatal("an image was scaled past the request's budget")
	}
	if _, _, ok := fittedImage("image/png", tall, 8000, &budget); !ok || budget != 1000 {
		t.Fatalf("a remembered image: %v, budget %d", ok, budget)
	}
}

func TestWebPIsWrittenAsPNG(t *testing.T) {
	budget := claudeScaledPixels
	mt, out, ok := fittedImage("image/webp", webpImage, 16, &budget)
	if !ok || mt != "image/png" {
		t.Fatalf("%v %s", ok, mt)
	}
	if _, _, format := imageSize(t, out); format != "png" {
		t.Fatal(format)
	}
}

// More than 20 images in a request: each is held to 2000 px; at 20 the
// limit is 8000. Images given by URL stay as they are.
func TestManyImagesFitTwoThousand(t *testing.T) {
	big := testImage(t, "png", 2400, 10)
	build := func(n int) map[string]any {
		blocks := []any{map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/a.png"}}}
		for i := 1; i < n; i++ {
			blocks = append(blocks, imageBlock("image/png", big))
		}
		return map[string]any{"messages": []any{map[string]any{"role": "user", "content": blocks}}}
	}
	sides := func(req map[string]any) (out []int) {
		for _, r := range imageRefs(req)[1:] {
			w, _, _ := imageSize(t, r.block()["source"].(map[string]any)["data"].(string))
			out = append(out, w)
		}
		return out
	}
	req := build(20)
	fitClaudeImages(req, 0, true)
	if s := sides(req); s[0] != 2400 {
		t.Fatalf("20 images: %v", s)
	}
	req = build(21)
	fitClaudeImages(req, 0, true)
	for _, w := range sides(req) {
		if w != 2000 {
			t.Fatalf("21 images: %v", sides(req))
		}
	}
	if src := imageRefs(req)[0].block()["source"].(map[string]any); src["url"] != "https://example.com/a.png" {
		t.Fatalf("url image: %v", src)
	}
	// Not Claude: no side is changed.
	req = build(21)
	fitClaudeImages(req, 0, false)
	if s := sides(req); s[0] != 2400 {
		t.Fatalf("not scaled: %v", s)
	}
}

func TestRefusedImageCap(t *testing.T) {
	for refusal, want := range map[string]int{
		"too many images: maximum allowed for model gemini-4-carbon is 10, got 12": 10,
		"Too many images: 101 > 100":                                                100,
		"messages: A request may carry at most 8 images.":                           8,
		"Too many images in request. Maximum number of images is 100":               100,
		"a maximum of 20 images per request":                                        20,
		"messages.0.content.0.image.source.base64.data: image exceeds 5 MB maximum": 0,
		"prompt is too long: 212345 tokens > 200000 maximum":                        0,
	} {
		if got := refusedImageCap(refusal); got != want {
			t.Errorf("%q: %d, want %d", refusal, got, want)
		}
	}
}

// A model that refuses a request for its number of images has it sent again
// at once with the oldest images left out, and the later requests are sent
// within the cap; the latest user message keeps its images.
func TestImageCapIsLearnt(t *testing.T) {
	small := testImage(t, "png", 4, 4)
	refused := refusalBody(t, "Too many images: 3 > 2")
	ok := scripted{Status: 200, Body: strings.Join(messagesOK, "\n")}
	srv, sent := scriptedServer(t, scripted{Status: 400, Body: refused}, ok, ok)
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	img := func() string { b, _ := json.Marshal(imageBlock("image/png", small)); return string(b) }
	body := `{"model":"claude-opus-5-5","max_tokens":100,"stream":true,"messages":[` +
		`{"role":"user","content":[` + img() + `,` + img() + `,{"type":"text","text":"look"}]},` +
		`{"role":"assistant","content":"seen"},` +
		`{"role":"user","content":[` + img() + `,{"type":"text","text":"and this"}]}]}`
	for range 2 {
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(*sent) != 3 {
		t.Fatalf("%d requests", len(*sent))
	}
	count := func(req map[string]any) int { return len(imageRefs(req)) }
	if count((*sent)[0]) != 3 || count((*sent)[1]) != 2 || count((*sent)[2]) != 2 {
		t.Fatalf("images sent: %d %d %d", count((*sent)[0]), count((*sent)[1]), count((*sent)[2]))
	}
	first := asJSON(t, messageList((*sent)[1])[0])
	last := asJSON(t, messageList((*sent)[1])[2])
	if strings.Count(first, imageOmitted) != 1 || !strings.Contains(last, `"image"`) {
		t.Fatalf("sent %s ... %s", first, last)
	}
}

// When the latest message alone is over the cap, the refusal stands.
func TestImageCapKeepsTheLatestMessage(t *testing.T) {
	var s anthropicShapes
	req := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{imageBlock("image/png", "x"), imageBlock("image/png", "y")}}}}
	if s.learnImageCap("m", "Too many images: 2 > 1", req) {
		t.Fatal("learnt a cap the request cannot be fitted to")
	}
}
