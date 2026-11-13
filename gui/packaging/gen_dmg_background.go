//go:build ignore

// Draws the background of the macOS disk image: a light backdrop with an arrow
// from the app's icon to the Applications link. Run from gui/:
//
//	go run packaging/gen_dmg_background.go
//
// It writes packaging/dmg-background.png (600x400) and dmg-background@2x.png
// (1200x800); build-gui.sh joins them into one TIFF for Retina screens. The
// icons sit at (150,190) and (450,190) in the 600x400 window.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

func segment(x, y, ax, ay, bx, by, width float64) bool {
	dx, dy := bx-ax, by-ay
	t := max(0, min(1, ((x-ax)*dx+(y-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(x-(ax+t*dx), y-(ay+t*dy)) <= width/2
}

func draw(scale int, name string) {
	w, h := 600*scale, 400*scale
	s := float64(scale)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	const ss = 3 // samples per axis, for smooth edges
	for py := range h {
		bg := 245.0 // flat: a gradient over so few levels shows bands
		for px := range w {
			hit := 0
			for i := range ss {
				for j := range ss {
					x := (float64(px) + (float64(i)+.5)/ss) / s
					y := (float64(py) + (float64(j)+.5)/ss) / s
					if segment(x, y, 245, 190, 345, 190, 7) ||
						segment(x, y, 345, 190, 322, 168, 7) ||
						segment(x, y, 345, 190, 322, 212, 7) {
						hit++
					}
				}
			}
			a := float64(hit) / (ss * ss)
			v := bg*(1-a) + 150*a
			img.Set(px, py, color.RGBA{uint8(v), uint8(v), uint8(v + 2*a), 255})
		}
	}
	f, err := os.Create(name)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}

func main() {
	draw(1, "packaging/dmg-background.png")
	draw(2, "packaging/dmg-background@2x.png")
}
