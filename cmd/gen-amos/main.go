// Command gen-amos turns assets/amos.png (upscaled pixel art of Amos the
// armadillo) into a Go sprite: it finds the art's native pixel grid, samples
// one color per grid cell, and writes internal/amos/sprite_gen.go plus a
// debug PNG of the sampled grid.
//
// assets/amos.png is rendered from the master, assets/amos-rough.pdf, at
// 150 dpi and cropped to the figure (the page also carries a logo):
//
//	pdftoppm -r 150 -png -singlefile assets/amos-rough.pdf /tmp/amos
//	magick /tmp/amos.png -gravity North -crop 100%x62%+0+0 +repage assets/amos.png
//	go run ./cmd/gen-amos -in assets/amos.png -out internal/amos/sprite_gen.go -debug /tmp/amos-grid.png
//
// Check the debug PNG: the pitch detector wants about eight render pixels
// per art pixel; a finer render makes it see two cells per pixel.
// -drop-shadow drops a pale gray drop shadow (an older source had one;
// the current art uses gray for the cheek and the nose, so it is off).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"image"
	"image/color"
	"image/png"
	"os"
	"sort"
)

type rgb struct{ R, G, B uint8 }

func main() {
	in := flag.String("in", "assets/amos.png", "source PNG")
	out := flag.String("out", "internal/amos/sprite_gen.go", "generated Go file")
	debug := flag.String("debug", "", "optional PNG of the sampled grid, scaled up")
	scale := flag.Int("debug-scale", 12, "pixels per cell in the debug PNG")
	merge := flag.Int("merge", 56, "colors closer than this (RGB distance) merge into one palette entry")
	dropShadow := flag.Bool("drop-shadow", false, "drop pale gray pixels (a drop shadow under the figure)")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		die(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		die(err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// 1. Background: everything reachable from the border through white or
	// transparent pixels. Amos's white body is sealed by the black outline,
	// so the flood never enters it.
	bg := make([]bool, w*h)
	isBG := func(x, y int) bool {
		r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		if a < 0x8000 {
			return true
		}
		return r>>8 > 245 && g>>8 > 245 && bl>>8 > 245
	}
	stack := []int{}
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= w || y >= h || bg[y*w+x] || !isBG(x, y) {
			return
		}
		bg[y*w+x] = true
		stack = append(stack, y*w+x)
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%w, i/w
		push(x+1, y)
		push(x-1, y)
		push(x, y+1)
		push(x, y-1)
	}

	// 2. Bounding box of the figure.
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !bg[y*w+x] {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		die(fmt.Errorf("no figure found"))
	}

	// 3. Pixel pitch: the most common short run length of one color along
	// rows and columns inside the box (edges between art pixels are blurry
	// after upscaling, so quantize colors coarsely first).
	q := func(x, y int) rgb {
		r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		return rgb{uint8(r>>8) / 32, uint8(g>>8) / 32, uint8(bl>>8) / 32}
	}
	runs := map[int]int{}
	for y := minY; y <= maxY; y++ {
		run := 1
		for x := minX + 1; x <= maxX; x++ {
			if q(x, y) == q(x-1, y) {
				run++
			} else {
				if run >= 4 && run <= 200 {
					runs[run]++
				}
				run = 1
			}
		}
	}
	for x := minX; x <= maxX; x++ {
		run := 1
		for y := minY + 1; y <= maxY; y++ {
			if q(x, y) == q(x, y-1) {
				run++
			} else {
				if run >= 4 && run <= 200 {
					runs[run]++
				}
				run = 1
			}
		}
	}
	// pick the pitch: the smallest run length r whose count (plus counts of
	// its near multiples) dominates; runs of k*r are solid stretches.
	type kv struct{ r, n int }
	var kvs []kv
	for r, n := range runs {
		kvs = append(kvs, kv{r, n})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].n > kvs[j].n })
	pitch := 0
	bestScore := 0
	for _, c := range kvs[:min(12, len(kvs))] {
		score := 0
		for r, n := range runs {
			if r%c.r <= 2 || c.r-r%c.r <= 2 {
				score += n
			}
		}
		if score > bestScore || (score == bestScore && c.r < pitch) {
			bestScore, pitch = score, c.r
		}
	}
	if pitch < 4 {
		die(fmt.Errorf("could not detect a pixel pitch (got %d)", pitch))
	}
	// refine: the figure spans an integer number of cells
	cols := int(float64(maxX-minX+1)/float64(pitch) + 0.5)
	rows := int(float64(maxY-minY+1)/float64(pitch) + 0.5)
	px := float64(maxX-minX+1) / float64(cols)
	py := float64(maxY-minY+1) / float64(rows)
	fmt.Fprintf(os.Stderr, "figure %dx%d px, pitch ~%d → %dx%d cells (px %.2f, py %.2f)\n",
		maxX-minX+1, maxY-minY+1, pitch, cols, rows, px, py)

	// 4. Sample the centre of every cell; drop the pale gray shadow.
	grid := make([][]rgb, rows)
	set := make([][]bool, rows)
	for r := 0; r < rows; r++ {
		grid[r] = make([]rgb, cols)
		set[r] = make([]bool, cols)
		for c := 0; c < cols; c++ {
			x := minX + int(float64(c)*px+px/2)
			y := minY + int(float64(r)*py+py/2)
			if bg[y*w+x] {
				continue
			}
			cr, cg, cb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			col := rgb{uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8)}
			if *dropShadow && isShadow(col) {
				continue
			}
			grid[r][c] = col
			set[r][c] = true
		}
	}
	// trim empty rows/cols left by the shadow
	grid, set = trim(grid, set)
	rows, cols = len(grid), len(grid[0])

	// 5. Palette: the upscaled art has anti-aliased edges, so merge every
	// sampled color into the nearest frequent one (greedy, in order of
	// frequency, within a small distance) and keep that palette.
	count := map[rgb]int{}
	for r := range grid {
		for c := range grid[r] {
			if set[r][c] {
				count[grid[r][c]]++
			}
		}
	}
	var seen []rgb
	for k := range count {
		seen = append(seen, k)
	}
	sort.Slice(seen, func(i, j int) bool {
		if count[seen[i]] != count[seen[j]] {
			return count[seen[i]] > count[seen[j]]
		}
		return seen[i].R < seen[j].R || (seen[i].R == seen[j].R && (seen[i].G < seen[j].G || (seen[i].G == seen[j].G && seen[i].B < seen[j].B)))
	})
	var pal []rgb
	snap := map[rgb]rgb{}
	for _, c := range seen {
		best, bestD := -1, 1<<30
		for i, p := range pal {
			if d := dist(c, p); d < bestD {
				best, bestD = i, d
			}
		}
		if best >= 0 && bestD < *merge**merge {
			snap[c] = pal[best]
			continue
		}
		pal = append(pal, c)
		snap[c] = c
	}
	for r := range grid {
		for c := range grid[r] {
			if set[r][c] {
				grid[r][c] = snap[grid[r][c]]
			}
		}
	}
	count = map[rgb]int{}
	for r := range grid {
		for c := range grid[r] {
			if set[r][c] {
				count[grid[r][c]]++
			}
		}
	}
	sort.Slice(pal, func(i, j int) bool { return count[pal[i]] > count[pal[j]] })
	index := map[rgb]int{}
	for i, c := range pal {
		index[c] = i + 1 // 0 = transparent
	}

	var src bytes.Buffer
	fmt.Fprintf(&src, "// Code generated by cmd/gen-amos from assets/amos.png (rendered from assets/amos-rough.pdf); DO NOT EDIT.\n\npackage amos\n\n")
	fmt.Fprintf(&src, "// spriteW and spriteH are the sprite's size in art pixels.\nconst (\n\tspriteW = %d\n\tspriteH = %d\n)\n\n", cols, rows)
	fmt.Fprintf(&src, "// spritePalette is indexed by the cell values below; 0 is transparent.\nvar spritePalette = []RGB{\n\t{}, // 0: transparent\n")
	for i, c := range pal {
		fmt.Fprintf(&src, "\t{0x%02x, 0x%02x, 0x%02x}, // %d\n", c.R, c.G, c.B, i+1)
	}
	fmt.Fprintf(&src, "}\n\n// spriteCells is the art, one byte per pixel, row-major.\nvar spriteCells = [spriteH][spriteW]uint8{\n")
	for r := range grid {
		fmt.Fprintf(&src, "\t{")
		for c := range grid[r] {
			v := 0
			if set[r][c] {
				v = index[grid[r][c]]
			}
			if c > 0 {
				src.WriteString(", ")
			}
			fmt.Fprintf(&src, "%d", v)
		}
		fmt.Fprintf(&src, "},\n")
	}
	fmt.Fprintf(&src, "}\n")
	code, err := format.Source(src.Bytes())
	if err != nil {
		die(err)
	}
	if err := os.WriteFile(*out, code, 0o644); err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s: %d colors\n", *out, len(pal))

	if *debug != "" {
		s := *scale
		dbg := image.NewRGBA(image.Rect(0, 0, cols*s, rows*s))
		for r := range grid {
			for c := range grid[r] {
				var col color.RGBA
				if set[r][c] {
					col = color.RGBA{grid[r][c].R, grid[r][c].G, grid[r][c].B, 255}
				} else {
					col = color.RGBA{40, 44, 52, 255} // dark terminal-ish background
				}
				for dy := 0; dy < s; dy++ {
					for dx := 0; dx < s; dx++ {
						dbg.Set(c*s+dx, r*s+dy, col)
					}
				}
			}
		}
		df, err := os.Create(*debug)
		if err != nil {
			die(err)
		}
		png.Encode(df, dbg)
		df.Close()
	}
}

func dist(a, b rgb) int {
	dr, dg, db := int(a.R)-int(b.R), int(a.G)-int(b.G), int(a.B)-int(b.B)
	return dr*dr + dg*dg + db*db
}

// isShadow is the pale gray drop shadow under the figure: nearly neutral,
// light but not white.
func isShadow(c rgb) bool {
	maxc := max(c.R, max(c.G, c.B))
	minc := min(c.R, min(c.G, c.B))
	return maxc-minc < 12 && minc > 150 && maxc < 240
}

func trim(grid [][]rgb, set [][]bool) ([][]rgb, [][]bool) {
	rows, cols := len(grid), len(grid[0])
	top, bottom, left, right := rows, -1, cols, -1
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if set[r][c] {
				top, bottom = min(top, r), max(bottom, r)
				left, right = min(left, c), max(right, c)
			}
		}
	}
	g2 := make([][]rgb, 0, bottom-top+1)
	s2 := make([][]bool, 0, bottom-top+1)
	for r := top; r <= bottom; r++ {
		g2 = append(g2, grid[r][left:right+1])
		s2 = append(s2, set[r][left:right+1])
	}
	return g2, s2
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "gen-amos:", err)
	os.Exit(1)
}
