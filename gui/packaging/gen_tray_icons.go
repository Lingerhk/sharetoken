//go:build ignore

// Draws the tray icons from the same shape as icon.png, drawn heavier so that it
// still reads at menu-bar size. Run from gui/: go run packaging/gen_tray_icons.go
//
//	packaging/tray_template.png  macOS: black with alpha, the system colors it
//	packaging/tray.ico           Windows: uncompressed entries, which every LoadImage reads
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

type rgba struct{ r, g, b, a float64 }

func circle(x, y, cx, cy, r float64) bool { return math.Hypot(x-cx, y-cy) <= r }

func segment(x, y, ax, ay, bx, by, width float64) bool {
	dx, dy := bx-ax, by-ay
	t := max(0, min(1, ((x-ax)*dx+(y-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(x-(ax+t*dx), y-(ay+t*dy)) <= width/2
}

func roundedRect(x, y, radius float64) bool {
	qx, qy := math.Abs(x-.5)-(.5-radius), math.Abs(y-.5)-(.5-radius)
	return math.Hypot(max(qx, 0), max(qy, 0))+min(max(qx, qy), 0) <= radius
}

// render samples shape on a 8x8 grid in each pixel; shape takes coordinates in 0–1.
func render(size int, shape func(x, y float64) (rgba, bool)) *image.NRGBA {
	const grid = 8
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := range size {
		for px := range size {
			var sum rgba
			var alpha float64
			for sy := range grid {
				for sx := range grid {
					x := (float64(px) + (float64(sx)+.5)/grid) / float64(size)
					y := (float64(py) + (float64(sy)+.5)/grid) / float64(size)
					if c, ok := shape(x, y); ok {
						sum.r, sum.g, sum.b = sum.r+c.r*c.a, sum.g+c.g*c.a, sum.b+c.b*c.a
						alpha += c.a
					}
				}
			}
			if alpha == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(sum.r / alpha * 255), uint8(sum.g / alpha * 255), uint8(sum.b / alpha * 255), uint8(alpha / (grid * grid) * 255)})
		}
	}
	return img
}

// template is the glyph alone, filling the menu bar's square.
func template(x, y float64) (rgba, bool) {
	const r, w = .145, .085
	tx, ty, lx, ly, rx, ry := .5, .24, .17, .76, .83, .76
	if circle(x, y, tx, ty, r) || circle(x, y, lx, ly, r) || circle(x, y, rx, ry, r) ||
		segment(x, y, tx, ty, lx, ly, w) || segment(x, y, tx, ty, rx, ry, w) {
		return rgba{0, 0, 0, 1}, true
	}
	return rgba{}, false
}

// colored is the app icon, with the same colors and a heavier glyph.
func colored(x, y float64) (rgba, bool) {
	const r, w = .15, .085
	if !roundedRect(x, y, .22) {
		return rgba{}, false
	}
	bg := rgba{.047, .055, .067, 1}
	lime := rgba{.773, .953, .302, 1}
	orange := rgba{1, .706, .29, 1}
	line := rgba{.569, .643, .745, 1}
	tx, ty, lx, ly, rx, ry := .5, .27, .21, .72, .79, .72
	switch {
	case circle(x, y, tx, ty, r):
		return lime, true
	case circle(x, y, lx, ly, r), circle(x, y, rx, ry, r):
		return orange, true
	case segment(x, y, tx, ty, lx, ly, w), segment(x, y, tx, ty, rx, ry, w):
		return line, true
	}
	return bg, true
}

func writeICO(path string, sizes []int) {
	var entries, blobs bytes.Buffer
	offset := 6 + 16*len(sizes)
	for _, size := range sizes {
		img := render(size, colored)
		maskRow := ((size + 31) / 32) * 4
		var dib bytes.Buffer
		binary.Write(&dib, binary.LittleEndian, struct {
			Size                          uint32
			Width, Height                 int32
			Planes, BitCount              uint16
			Compression, ImageSize        uint32
			XPels, YPels, Used, Important int32
		}{Size: 40, Width: int32(size), Height: int32(2 * size), Planes: 1, BitCount: 32})
		for y := size - 1; y >= 0; y-- { // bottom-up
			for x := range size {
				c := img.NRGBAAt(x, y)
				dib.Write([]byte{c.B, c.G, c.R, c.A})
			}
		}
		mask := make([]byte, maskRow)
		for y := size - 1; y >= 0; y-- {
			clear(mask)
			for x := range size {
				if img.NRGBAAt(x, y).A == 0 {
					mask[x/8] |= 0x80 >> (x % 8)
				}
			}
			dib.Write(mask)
		}
		binary.Write(&entries, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BitCount       uint16
			Bytes, Offset          uint32
		}{uint8(size), uint8(size), 0, 0, 1, 32, uint32(dib.Len()), uint32(offset)})
		offset += dib.Len()
		blobs.Write(dib.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	out.Write(entries.Bytes())
	out.Write(blobs.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	var buf bytes.Buffer
	if err := png.Encode(&buf, render(44, template)); err != nil { // 22 points at 2x
		log.Fatal(err)
	}
	if err := os.WriteFile("packaging/tray_template.png", buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	writeICO("packaging/tray.ico", []int{16, 20, 24, 32, 40, 48, 64})
}
