// Package amos draws Amos, the dillodash armadillo. The sprite comes from
// assets/amos.png (rendered from assets/amos-rough.pdf) through
// cmd/gen-amos; it is rendered with half-block
// characters, two art pixels per terminal cell, in Amos's own colors (the
// colorblind palette rules apply to status, not to the mascot). Terminals
// without color get the line-art armadillo from the glyphs package instead;
// that choice is the caller's.
package amos

import (
	"fmt"
	"strings"
)

// Options select a rendering.
type Options struct {
	// Mirror draws Amos facing the other way.
	Mirror bool
	// Frame picks a walk frame: 0 is at rest, 1 is mid-step (the legs
	// shift and the body bobs a pixel). Any value is taken modulo 2.
	Frame int
	// Bg, when set, fills the transparent half of a cell so the sprite
	// sits on a colored background instead of the terminal default.
	Bg *RGB
}

// Width is the sprite's width in terminal cells.
func Width() int { return spriteW }

// Height is the sprite's height in terminal rows (two pixels per row).
func Height() int { return (spriteH + 1) / 2 }

// Palette returns the sprite's colors (index 0 is transparent).
func Palette() []RGB { return append([]RGB(nil), spritePalette...) }

// grid returns the art pixels after the frame transform, as palette
// indices; 0 is transparent. The returned grid is spriteH+1 rows tall so
// the one-pixel bob of frame 1 has room.
func grid(o Options) [][]uint8 {
	rows := spriteH + 1
	g := make([][]uint8, rows)
	for r := range g {
		g[r] = make([]uint8, spriteW)
	}
	step := o.Frame%2 == 1
	dy := 0
	if step {
		dy = 1
	}
	for r := 0; r < spriteH; r++ {
		for c := 0; c < spriteW; c++ {
			v := spriteCells[r][c]
			if v == 0 {
				continue
			}
			cc := c
			// mid-step: the legs (the bottom fifth of the art) swing a
			// pixel forward; everything bobs down one pixel
			if step && r >= spriteH*4/5 {
				cc = c + 1
			}
			rr := r + dy
			if cc < 0 || cc >= spriteW || rr >= rows {
				continue
			}
			g[rr][cc] = v
		}
	}
	if o.Mirror {
		for r := range g {
			for i, j := 0, spriteW-1; i < j; i, j = i+1, j-1 {
				g[r][i], g[r][j] = g[r][j], g[r][i]
			}
		}
	}
	return g
}

const (
	upper = "▀" // ▀ upper half block
	lower = "▄" // ▄ lower half block
	reset = "\x1b[0m"
)

func fg(c RGB) string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.R, c.G, c.B) }
func bg(c RGB) string { return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c.R, c.G, c.B) }

// Lines renders the sprite as Height() lines of ANSI-styled text, each
// Width() cells wide. Colors are 24-bit SGR sequences; the terminal
// renderer downgrades them where the terminal cannot show them.
func Lines(o Options) []string {
	g := grid(o)
	out := make([]string, 0, Height())
	for r := 0; r < spriteH; r += 2 {
		var b strings.Builder
		var cur string
		for c := 0; c < spriteW; c++ {
			top := spritePalette[g[r][c]]
			topSet := g[r][c] != 0
			botSet := false
			var bot RGB
			if r+1 < len(g) {
				botSet = g[r+1][c] != 0
				bot = spritePalette[g[r+1][c]]
			}
			var sgr, ch string
			switch {
			case topSet && botSet:
				sgr, ch = fg(top)+bg(bot), upper
			case topSet:
				sgr, ch = fg(top)+bgOr(o.Bg), upper
			case botSet:
				sgr, ch = fg(bot)+bgOr(o.Bg), lower
			default:
				sgr, ch = bgOr(o.Bg), " "
			}
			if sgr != cur {
				b.WriteString(reset)
				b.WriteString(sgr)
				cur = sgr
			}
			b.WriteString(ch)
		}
		b.WriteString(reset)
		out = append(out, b.String())
	}
	return out
}

// Cell is one rendered terminal cell: its glyph and the SGR prefix that
// colors it ("" for an empty, transparent cell).
type Cell struct {
	Glyph string
	SGR   string
}

// Cells renders the sprite cell by cell for painting onto a canvas:
// Height() rows of Width() cells. A transparent cell has an empty SGR
// and a space glyph (with Bg set it carries the background instead).
func Cells(o Options) [][]Cell {
	g := grid(o)
	out := make([][]Cell, 0, Height())
	for r := 0; r < spriteH; r += 2 {
		row := make([]Cell, spriteW)
		for c := 0; c < spriteW; c++ {
			top := spritePalette[g[r][c]]
			topSet := g[r][c] != 0
			botSet := false
			var bot RGB
			if r+1 < len(g) {
				botSet = g[r+1][c] != 0
				bot = spritePalette[g[r+1][c]]
			}
			switch {
			case topSet && botSet:
				row[c] = Cell{upper, fg(top) + bg(bot)}
			case topSet:
				row[c] = Cell{upper, fg(top) + bgOr(o.Bg)}
			case botSet:
				row[c] = Cell{lower, fg(bot) + bgOr(o.Bg)}
			default:
				row[c] = Cell{" ", bgOr(o.Bg)}
			}
		}
		out = append(out, row)
	}
	return out
}

func bgOr(c *RGB) string {
	if c == nil {
		return ""
	}
	return bg(*c)
}

// Plain renders the sprite without color, one character per cell: "#" for
// the outline, "o" for the shell, "." for the body, space for nothing.
// It exists for tests and for the `amos` subcommand; real monochrome terminals
// get the line-art armadillo instead.
func Plain(o Options) []string {
	g := grid(o)
	out := make([]string, 0, Height())
	for r := 0; r < spriteH; r += 2 {
		var b strings.Builder
		for c := 0; c < spriteW; c++ {
			v := g[r][c]
			if r+1 < len(g) && v == 0 {
				v = g[r+1][c]
			}
			switch {
			case v == 0:
				b.WriteByte(' ')
			case isDark(spritePalette[v]):
				b.WriteByte('#')
			case isLight(spritePalette[v]):
				b.WriteByte('.')
			default:
				b.WriteByte('o')
			}
		}
		out = append(out, b.String())
	}
	return out
}

func isDark(c RGB) bool  { return int(c.R)+int(c.G)+int(c.B) < 3*96 }
func isLight(c RGB) bool { return int(c.R)+int(c.G)+int(c.B) > 3*230 }
