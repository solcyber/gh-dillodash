package widgets

import (
	"fmt"
	"math"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// DrawBox draws a rounded box with a cleared interior at (top, left),
// height rows by width cells, with title in the top border. ants is the
// phase (0..1) at which a band of light runs round the border, or
// negative for none. Returns the interior's top row, left column and
// width.
func DrawBox(c *canvas.Canvas, top, left, height, width int, title, titleStyle, borderStyle string, ants float64) (int, int, int) {
	box := []rune(glyphs.G("box"))
	if len(box) < 6 {
		box = []rune("++++-|")
	}
	tl, tr, bl, br, hz, vt := string(box[0]), string(box[1]), string(box[2]), string(box[3]), string(box[4]), string(box[5])
	if width < 4 {
		width = 4
	}
	perim := 2*(width-1) + 2*(height-1)
	bs := func(p int) string { // p: the cell's position clockwise from the top-left corner
		if ants < 0 || perim <= 0 {
			return borderStyle
		}
		d := math.Mod(float64(p)-ants*float64(perim), float64(perim))
		if d < 0 {
			d += float64(perim)
		}
		switch {
		case d < 2:
			return "fx.b7"
		case d < 5:
			return "fx.b6"
		case d < 9:
			return "fx.b5"
		}
		return borderStyle
	}
	c.PutSegs(top, left, []text.Seg{text.S(tl, bs(0)), text.S(hz, bs(1))}, 0, false, false)
	x := left + 2
	if title != "" {
		x = c.PutSegs(top, left+2, []text.Seg{text.S(" "+title+" ", titleStyle)}, 0, false, false)
	}
	for col := x - left; col < width-1; col++ {
		c.PutSegs(top, left+col, []text.Seg{text.S(hz, bs(col))}, 0, false, false)
	}
	c.PutSegs(top, left+width-1, []text.Seg{text.S(tr, bs(width-1))}, 0, false, false)
	for r := 1; r < height-1; r++ {
		c.PutSegs(top+r, left, []text.Seg{text.S(vt, bs(perim-r))}, 0, false, false)
		c.Fill(top+r, left+1, left+width-1, "text", false, false)
		c.PutSegs(top+r, left+width-1, []text.Seg{text.S(vt, bs(width-1+r))}, 0, false, false)
	}
	for col := 0; col < width; col++ {
		ch := hz
		if col == 0 {
			ch = bl
		} else if col == width-1 {
			ch = br
		}
		c.PutSegs(top+height-1, left+col, []text.Seg{text.S(ch, bs(2*(width-1)+(height-1)-col))}, 0, false, false)
	}
	return top + 1, left + 2, width - 4
}

// PopHeight is a box's height at t (0..1) into its pop, from one row to
// full, overshooting by a row before it settles; ok false (the pop is
// over) gives full.
func PopHeight(full int, t float64, ok bool) int {
	if !ok {
		return full
	}
	return max(2, int(math.RoundToEven(float64(full)*fx.EaseOutBack(t))))
}

// PopSecs is how long a menu's pop takes.
const PopSecs = 0.18

// Pick chooses one of Labels from a boxed menu: j/k or arrows then Enter,
// or 1-9 for the first nine; Esc or q cancels. Every entry is reachable
// however many (g/G, PgUp/PgDn, and j/k wrap). With motion the box pops
// open (the PopName timer in the app's effects) and, ambiently, a band of
// light runs round its border.
type Pick struct {
	Title  string
	Labels []string
	// PopName names the pop timer in the effect set given to Draw; Open
	// starts it. "" means no pop.
	PopName string

	sel, first int
	room       int // rows of entries shown, from the last Draw
	done       bool
	result     int
}

// NewPick makes a menu; popName names its pop timer ("" for none).
func NewPick(title string, labels []string, popName string) *Pick {
	return &Pick{Title: title, Labels: labels, PopName: popName, result: -1, room: max(1, len(labels))}
}

// Open starts the pop in f (a no-op without a PopName or f).
func (p *Pick) Open(f *fx.Fx) {
	if f != nil && p.PopName != "" {
		f.Start(p.PopName, PopSecs, nil)
	}
}

// Result is the chosen index, -1 when cancelled (or not yet answered).
func (p *Pick) Result() int { return p.result }

// Done says the menu was answered or cancelled.
func (p *Pick) Done() bool { return p.done }

// Selected is the highlighted index.
func (p *Pick) Selected() int { return p.sel }

// Animated says the menu moves on its own (motion on).
func (p *Pick) Animated() bool { return fx.Motion == "full" }

// Handle moves the selection or answers.
func (p *Pick) Handle(k Key) bool {
	if p.done {
		return true
	}
	n := len(p.Labels)
	if n == 0 {
		p.done = true
		return true
	}
	switch k.Name {
	case "esc", "ctrl+c", "ctrl+g":
		p.done = true
		return true
	case "down":
		p.sel = (p.sel + 1) % n
	case "up":
		p.sel = (p.sel - 1 + n) % n
	case "home":
		p.sel = 0
	case "end":
		p.sel = n - 1
	case "pgdn":
		p.sel = min(n-1, p.sel+p.room)
	case "pgup":
		p.sel = max(0, p.sel-p.room)
	case "enter":
		p.result, p.done = p.sel, true
		return true
	case "":
		switch r := k.Rune; {
		case r == 'q':
			p.done = true
			return true
		case r == 'j':
			p.sel = (p.sel + 1) % n
		case r == 'k':
			p.sel = (p.sel - 1 + n) % n
		case r == 'g':
			p.sel = 0
		case r == 'G':
			p.sel = n - 1
		case r >= '1' && r <= '9' && int(r-'1') < n:
			p.result, p.done = int(r-'1'), true
			return true
		}
	}
	return false
}

// Draw paints the box at row 2, column 1.
func (p *Pick) Draw(c *canvas.Canvas, f *fx.Fx) {
	h, w := c.H, c.W
	n := len(p.Labels)
	animated := p.Animated()
	widest := 0
	for _, l := range p.Labels {
		widest = max(widest, text.Width(l))
	}
	bw := min(w-2, max(widest+10, 48))
	// a title too long for the border is wrapped inside the box
	var head []string
	borderTitle := p.Title
	if text.Width(p.Title)+6 > bw {
		head = Wrap(p.Title, bw-4)
		borderTitle = ""
	}
	room := max(1, min(n, h-8-len(head)))
	p.room = room
	if p.sel < p.first {
		p.first = p.sel
	} else if p.sel >= p.first+room {
		p.first = p.sel - room + 1
	}
	shown := p.Labels[min(n, p.first):min(n, p.first+room)]
	full := room + 4 + len(head)
	if len(head) > 0 {
		full++
	}
	pop, popping := 0.0, false
	if f != nil && p.PopName != "" {
		pop, popping = f.T(p.PopName)
	}
	bh := min(h-2, PopHeight(full, pop, popping))
	ants := -1.0
	if animated && fx.Ambient {
		ants = fx.Wave(2.5, 0)
	}
	top, left, iw := DrawBox(c, 2, 1, bh, bw, borderTitle, "menu.title", "logo", ants)
	last := 2 + bh - 2 // the interior's last row
	for i, ln := range head {
		if top+i <= last {
			c.PutSegs(top+i, left, []text.Seg{text.S(ln, "menu.title")}, left+iw, false, false)
		}
	}
	top += len(head)
	if len(head) > 0 {
		top++
	}
	for i, lab := range shown {
		if top+i > last {
			break
		}
		idx := p.first + i
		isSel := idx == p.sel
		num := "  "
		if idx < 9 {
			num = fmt.Sprintf("%d ", idx+1)
		}
		gut := "gutter"
		if isSel && animated && theme.Current() != theme.Mono {
			gut = fx.Ramp("gutter.", 5, fx.Tri(fx.Wave(1.2, 0)))
		}
		mark, ls := " ", "menu.item"
		if isSel {
			mark, ls = glyphs.G("sel"), "bright"
			c.Fill(top+i, left-1, left+iw+1, "text", true, false)
		}
		c.PutSegs(top+i, left-1, []text.Seg{
			text.S(mark, gut), text.S(num, "faint"), text.S(lab, ls),
		}, left+iw+1, isSel, false)
	}
	sep := glyphs.G("sep")
	hint := fmt.Sprintf("j/k move %s Enter picks %s 1-9 jump %s Esc cancels", sep, sep, sep)
	if n > room {
		hint += fmt.Sprintf("  (%d-%d of %d)", p.first+1, p.first+len(shown), n)
	}
	if top+room+1 <= last {
		c.PutSegs(top+room+1, left, []text.Seg{text.S(hint, "menu.hint")}, left+iw, false, false)
	}
}
