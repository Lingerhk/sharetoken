package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Images sent to Claude (after magpie's claude_images.go and image_cap.go):
//
//   - the API refuses an image over 8000 px a side, and one over 2000 px in
//     a request with more than 20 images. Clients send such images as they
//     are (a tall screenshot, the earlier turns of a resumed transcript,
//     a tool's images), the turn fails, and so does every later one that
//     carries the same history. A base64 image over the limit is scaled to
//     fit before it is sent; one under it, or one given by URL, goes as it
//     came.
//   - a model may take only so many images in one request, and says how many
//     when it refuses one with more. The cap is learnt from the refusal, per
//     model as anthropicShapes learns what a model refused, and the request
//     goes again, and later ones go, with the oldest images each replaced by
//     a short text. The latest user message keeps its own: when they alone
//     are over the cap, nothing is left out and the refusal stands.

const (
	claudeImageSide     = 8000 // a side the API takes
	claudeManyImages    = 20   // more images than this in one request ...
	claudeManyImageSide = 2000 // ... and each side has to fit this
	// claudeImageBytes is the most base64 one image may carry.
	claudeImageBytes = 5 << 20
	// claudeImagePixels bounds the images decoded to be scaled: a larger one
	// would hold too much of the seller's memory and goes as it came.
	claudeImagePixels = 50_000_000
)

// imageOmitted stands where an image over the model's cap was left out. It
// goes to the model, so it names neither ShareToken nor the vendor.
const imageOmitted = "[An earlier image was omitted here to keep this request within the model's image limit.]"

// imageCapRefused are the words a refusal of too many images gives the cap
// in, most specific first.
var imageCapRefused = []*regexp.Regexp{
	regexp.MustCompile(`(?i)too many images: maximum allowed for model \S+ is (\d+)`),
	regexp.MustCompile(`(?i)too many images\D*?\d+\s*(?:>|\\u003e)\s*(\d+)`), // '>' as a JSON encoder may escape it
	regexp.MustCompile(`(?i)at most (\d+) images`),
	regexp.MustCompile(`(?i)(?:maximum|max|limit) of (\d+) images`),
	regexp.MustCompile(`(?i)too many images\D*?(?:maximum|max|limit|at most)(?: number of images)?(?: allowed)?(?: is|:| of)?\s*(\d+)`),
}

// refusedImageCap is the image cap a refusal names, 0 for none.
func refusedImageCap(refusal string) int {
	for _, re := range imageCapRefused {
		if m := re.FindStringSubmatch(refusal); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// imageRef is an image block of a request: blocks[i], in message msg.
type imageRef struct {
	msg    int
	blocks []any
	i      int
}

func (r imageRef) block() map[string]any {
	b, _ := r.blocks[r.i].(map[string]any)
	return b
}

// imageRefs are req's image blocks, oldest first, those inside tool results
// too.
func imageRefs(req map[string]any) []imageRef {
	var out []imageRef
	var walk func(msg int, blocks []any)
	walk = func(msg int, blocks []any) {
		for i, raw := range blocks {
			b, _ := raw.(map[string]any)
			switch b["type"] {
			case "image":
				out = append(out, imageRef{msg: msg, blocks: blocks, i: i})
			case "tool_result":
				walk(msg, list(b["content"]))
			}
		}
	}
	for i, m := range messageList(req) {
		walk(i, list(m["content"]))
	}
	return out
}

// latestUser is the index of req's last user message, -1 for none.
func latestUser(req map[string]any) int {
	msgs := messageList(req)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			return i
		}
	}
	return -1
}

// earlierImages is how many of refs are before the latest user message.
func earlierImages(req map[string]any, refs []imageRef) int {
	last, n := latestUser(req), 0
	for _, r := range refs {
		if r.msg < last {
			n++
		}
	}
	return n
}

// fitClaudeImages fits req's images to what Claude takes: no more than
// most (0 for no cap), the oldest left out first, and each within the side
// the request's number of images allows.
func fitClaudeImages(req map[string]any, most int, scale bool) {
	refs := imageRefs(req)
	if len(refs) == 0 {
		return
	}
	if over := len(refs) - most; most > 0 && over > 0 && earlierImages(req, refs) >= over {
		for _, r := range refs[:over] {
			r.blocks[r.i] = map[string]any{"type": "text", "text": imageOmitted}
		}
		refs = refs[over:]
	}
	if !scale {
		return
	}
	side := claudeImageSide
	if len(refs) > claudeManyImages {
		side = claudeManyImageSide
	}
	for _, r := range refs {
		src, _ := r.block()["source"].(map[string]any)
		if src == nil || src["type"] != "base64" {
			continue
		}
		data, _ := src["data"].(string)
		mediaType, _ := src["media_type"].(string)
		if mt, d, ok := fittedImage(mediaType, data, side); ok {
			src["media_type"], src["data"] = mt, d
		}
	}
}

// fittedImage is a base64 image scaled to fit side px a side; false when it
// fits already or cannot be read, and goes as it came.
func fittedImage(mediaType, data string, side int) (string, string, bool) {
	if data == "" {
		return "", "", false
	}
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil || (cfg.Width <= side && cfg.Height <= side) || cfg.Width*cfg.Height > claudeImagePixels {
		return "", "", false
	}
	key := sha256.Sum256([]byte(strconv.Itoa(side) + "\x00" + mediaType + "\x00" + data))
	if e, ok := fittedImages.get(key); ok {
		return e.mediaType, e.data, e.data != ""
	}
	e := scaledImage(data, side)
	fittedImages.put(key, e)
	return e.mediaType, e.data, e.data != ""
}

// fittedEntry is an image as scaledImage wrote it; no data when it could not.
type fittedEntry struct{ mediaType, data string }

// scaledImage decodes a base64 image, scales it to fit side and writes it
// again: a JPEG as JPEG, anything else as PNG, and as JPEG at a lower
// quality when that is still over the API's size for one image.
func scaledImage(data string, side int) fittedEntry {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fittedEntry{}
	}
	var src image.Image
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fittedEntry{}
	}
	switch format {
	case "png":
		src, err = png.Decode(bytes.NewReader(raw))
	case "jpeg":
		src, err = jpeg.Decode(bytes.NewReader(raw))
	case "gif":
		src, err = gif.Decode(bytes.NewReader(raw))
	case "webp":
		src, err = webp.Decode(bytes.NewReader(raw))
	default:
		return fittedEntry{}
	}
	if err != nil {
		return fittedEntry{}
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w >= h {
		w, h = side, max(h*side/w, 1)
	} else {
		w, h = max(w*side/h, 1), side
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Rect, src, b, draw.Src, nil)
	fits := func(out []byte) bool { return base64.StdEncoding.EncodedLen(len(out)) <= claudeImageBytes }
	var out bytes.Buffer
	if format == "jpeg" {
		if jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}) == nil && fits(out.Bytes()) {
			return fittedEntry{"image/jpeg", base64.StdEncoding.EncodeToString(out.Bytes())}
		}
	} else if png.Encode(&out, dst) == nil && fits(out.Bytes()) {
		return fittedEntry{"image/png", base64.StdEncoding.EncodeToString(out.Bytes())}
	}
	// Still too large: JPEG, on white where the image was clear.
	flat := image.NewRGBA(dst.Rect)
	draw.Draw(flat, flat.Rect, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Rect, dst, image.Point{}, draw.Over)
	for _, q := range []int{75, 60, 40} {
		out.Reset()
		if jpeg.Encode(&out, flat, &jpeg.Options{Quality: q}) == nil && fits(out.Bytes()) {
			return fittedEntry{"image/jpeg", base64.StdEncoding.EncodeToString(out.Bytes())}
		}
	}
	return fittedEntry{}
}

// fittedImages keeps the images scaled lately: a conversation sends its
// history, and the same images in it, with every turn.
var fittedImages = &imageCache{max: 32}

type imageCache struct {
	mu  sync.Mutex
	max int
	m   map[[32]byte]fittedEntry
}

func (c *imageCache) get(k [32]byte) (fittedEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	return e, ok
}

func (c *imageCache) put(k [32]byte, e fittedEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= c.max {
		c.m = map[[32]byte]fittedEntry{}
	}
	c.m[k] = e
}
