package amos

import "github.com/solcyber/gh-dillodash/internal/text"

// miniArt is the small Amos, drawn by hand at a third of the sprite's
// size (a vote over the sprite's 3x3 blocks loses the face and the bands
// to the outline). D outline, Y shell, O the shell's bands, W face and
// feet, G nose, . nothing.
var miniArt = []string{
	".....DDDDDD.....",
	"...DDODYODYDD...",
	"..DYDOYDOYDYYDD.",
	".DYDOYDOYDWGYYYD",
	".DYDOYDOYDWWWYYD",
	"DYYDOYDOYYDWDWWD",
	"DYYDOYDOYYDWWWWG",
	"YYDDDDDDDDDDDDDD",
	"DDDWWD...DWWWD..",
	"...DDD...DDDD...",
}

// miniColors are the letters' colors; each takes the nearest sprite
// palette entry, so regenerating the sprite keeps the small Amos right.
var miniColors = map[byte]RGB{
	'D': {0x24, 0x17, 0x18}, 'Y': {0xf7, 0xe0, 0x34}, 'O': {0xc1, 0xba, 0x2d},
	'W': {0xff, 0xff, 0xff}, 'G': {0xb8, 0xb2, 0xb1},
}

// mini is miniArt as palette indices.
var mini = func() art {
	a := art{w: len(miniArt[0]), h: len(miniArt), cells: make([][]uint8, len(miniArt))}
	for r, ln := range miniArt {
		a.cells[r] = make([]uint8, a.w)
		for c := 0; c < len(ln); c++ {
			if col, ok := miniColors[ln[c]]; ok {
				a.cells[r][c] = nearest(col)
			}
		}
	}
	return a
}()

// nearest is the palette index (never 0) of the color closest to c.
func nearest(c RGB) uint8 {
	best, bestD := uint8(1), -1
	for i := 1; i < len(spritePalette); i++ {
		p := spritePalette[i]
		dr, dg, db := int(p.R)-int(c.R), int(p.G)-int(c.G), int(p.B)-int(c.B)
		if d := dr*dr + dg*dg + db*db; bestD < 0 || d < bestD {
			best, bestD = uint8(i), d
		}
	}
	return best
}

// Segs renders the sprite as styled segments, one line per terminal row:
// a colored cell is a raw segment in its exact colors (see text.Raw), a
// transparent one a plain space.
func Segs(o Options) [][]text.Seg {
	var out [][]text.Seg
	for _, row := range Cells(o) {
		var segs []text.Seg
		for _, c := range row {
			if c.SGR == "" {
				segs = append(segs, text.S(c.Glyph, "text"))
				continue
			}
			segs = append(segs, text.Raw(c.Glyph, c.SGR))
		}
		out = append(out, segs)
	}
	return out
}
