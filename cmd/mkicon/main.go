// mkicon writes a 1024×1024 Grok Pane app icon (no external tools).
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	out := "desktop/build/appicon.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		panic(err)
	}
	img := render(1024)
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func render(n int) *image.RGBA {
	// Fully transparent canvas — only the pane mark is opaque.
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	ink := color.RGBA{28, 25, 20, 255}
	clear := color.RGBA{0, 0, 0, 0}
	m := n * 18 / 100
	thick := n * 9 / 100
	roundRect(img, m, m, n-m, n-m, n/7, ink)
	inner := m + thick
	roundRect(img, inner, inner, n-inner, n-inner, n/10, clear)
	mid := n / 2
	bar := n * 7 / 100
	for y := inner; y < n-inner; y++ {
		for x := mid - bar/2; x <= mid+bar/2; x++ {
			img.Set(x, y, ink)
		}
	}
	for x := inner; x < n-inner; x++ {
		for y := mid - bar/2; y <= mid+bar/2; y++ {
			img.Set(x, y, ink)
		}
	}
	return img
}

func roundRect(img *image.RGBA, x0, y0, x1, y1, r int, c color.Color) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if insideRound(x, y, x0, y0, x1, y1, r) {
				img.Set(x, y, c)
			}
		}
	}
}

func insideRound(x, y, x0, y0, x1, y1, r int) bool {
	if x >= x0+r && x < x1-r {
		return y >= y0 && y < y1
	}
	if y >= y0+r && y < y1-r {
		return x >= x0 && x < x1
	}
	corners := [][2]int{
		{x0 + r, y0 + r},
		{x1 - r - 1, y0 + r},
		{x0 + r, y1 - r - 1},
		{x1 - r - 1, y1 - r - 1},
	}
	for _, p := range corners {
		dx := x - p[0]
		dy := y - p[1]
		if dx*dx+dy*dy <= r*r {
			if (x >= x0 && x < x1) && (y >= y0 && y < y1) {
				return true
			}
		}
	}
	return false
}
