package widgets

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// Prompt is a single-line input on the bottom row, its question wrapped
// above it. Enter accepts (the text stripped), Esc / Ctrl-C / Ctrl-G
// cancel. It edits characters, not bytes, and keeps the cursor in view by
// scrolling the line.
//
// MinLen / Forbid (the --outside-window reason): a hint row counts down
// the characters still needed and a meter fills across the bottom row.
// Enter is REFUSED until the text is long enough: the footer shakes and
// says how many more, instead of returning a short answer the caller
// would only throw away. A forbidden character is refused as it is typed,
// with a note. Hazard draws a marching gold stripe above the question
// (the destroy-count prompt).
type Prompt struct {
	Question string
	MinLen   int
	Forbid   string
	Hazard   bool
	// OnChange, when set, is called with the whole text after every edit
	// (the live list filter); never on a cursor move.
	OnChange func(text string)

	// CursorY and CursorX are where the terminal cursor belongs after the
	// last Draw.
	CursorY, CursorX int

	buf     []rune
	pos     int
	fx      *fx.Fx // this prompt's own effects: shake, shine, blink
	refused bool   // Enter was pressed while too short; until an edit
	note    string // a refused keystroke's explanation; until an edit
	wasOK   int    // -1 unknown, 0 short, 1 enough: the shine fires on 0 -> 1
	done    bool
	ok      bool
	result  string
}

// NewPrompt makes a prompt for question with the cursor at the end of
// initial. Set MinLen, Forbid, Hazard and OnChange on the result.
func NewPrompt(question, initial string) *Prompt {
	p := &Prompt{Question: question, buf: []rune(initial), fx: fx.New(), wasOK: -1}
	p.pos = len(p.buf)
	return p
}

// Text is the current text, unstripped.
func (p *Prompt) Text() string { return string(p.buf) }

// Pos is the cursor's position in characters.
func (p *Prompt) Pos() int { return p.pos }

// Result is the stripped text, and whether Enter accepted it (ok is
// false until the prompt is done, and after a cancel).
func (p *Prompt) Result() (string, bool) { return p.result, p.ok }

// Done says the prompt finished.
func (p *Prompt) Done() bool { return p.done }

// Refused says Enter was refused since the last edit (the footer is red).
func (p *Prompt) Refused() bool { return p.refused }

// Note is the explanation of a refused keystroke since the last edit.
func (p *Prompt) Note() string { return p.note }

// Animated says the prompt moves on its own: the meter and the stripe.
func (p *Prompt) Animated() bool {
	return fx.Motion == "full" && (p.MinLen > 0 || p.Hazard)
}

// Active says one of the prompt's own effects plays (tick at FrameMS).
func (p *Prompt) Active() bool { return p.fx.Active() }

func (p *Prompt) progressOK() bool {
	ok, _, _ := ReasonProgress(string(p.buf), p.MinLen, p.Forbid)
	return ok
}

// track fires the shine the moment the reason becomes long enough.
func (p *Prompt) track() {
	if p.MinLen <= 0 {
		return
	}
	ok := 0
	if p.progressOK() {
		ok = 1
	}
	if p.wasOK == 0 && ok == 1 {
		p.fx.Start("shine", 0.7, nil)
	}
	p.wasOK = ok
}

// Handle edits the line; done reports Enter or a cancel ended it.
func (p *Prompt) Handle(k Key) bool {
	if p.done {
		return true
	}
	p.track()
	before := string(p.buf)
	switch k.Name {
	case "enter":
		if p.MinLen > 0 && !p.progressOK() {
			p.refused = true
			p.fx.Start("shake", 0.25, nil)
			p.fx.Start("refused", 0.6, nil)
			return false
		}
		p.result, p.ok, p.done = strings.TrimSpace(string(p.buf)), true, true
		return true
	case "esc", "ctrl+c", "ctrl+g":
		p.done = true
		return true
	case "backspace":
		if p.pos > 0 {
			p.buf = append(p.buf[:p.pos-1], p.buf[p.pos:]...)
			p.pos--
		}
	case "delete", "ctrl+d":
		if p.pos < len(p.buf) {
			p.buf = append(p.buf[:p.pos], p.buf[p.pos+1:]...)
		}
	case "left", "ctrl+b":
		if p.pos > 0 {
			p.pos--
		}
	case "right", "ctrl+f":
		if p.pos < len(p.buf) {
			p.pos++
		}
	case "home", "ctrl+a":
		p.pos = 0
	case "end", "ctrl+e":
		p.pos = len(p.buf)
	case "ctrl+u": // kill to the start of the line
		p.buf = append([]rune(nil), p.buf[p.pos:]...)
		p.pos = 0
	case "ctrl+k": // kill to the end of the line
		p.buf = p.buf[:p.pos]
	case "ctrl+w": // kill the previous word
		j := p.pos
		for j > 0 && p.buf[j-1] == ' ' {
			j--
		}
		for j > 0 && p.buf[j-1] != ' ' {
			j--
		}
		p.buf = append(p.buf[:j], p.buf[p.pos:]...)
		p.pos = j
	case "":
		if k.Rune == 0 || !unicode.IsPrint(k.Rune) {
			return false
		}
		if strings.ContainsRune(p.Forbid, k.Rune) {
			p.note = fmt.Sprintf("no %c — the validator refuses it", k.Rune)
			p.fx.Start("shake", 0.25, nil)
			return false
		}
		p.buf = append(p.buf[:p.pos], append([]rune{k.Rune}, p.buf[p.pos:]...)...)
		p.pos++
	default:
		return false
	}
	if now := string(p.buf); now != before {
		p.refused = false
		p.note = ""
		p.track()
		if p.OnChange != nil {
			p.OnChange(now)
		}
	}
	return false
}

// Draw paints the question block, the input row and, with MinLen, the
// hint row and the meter, on the bottom rows of the canvas. f (the app's
// effects) is not used: the prompt plays its own.
func (p *Prompt) Draw(c *canvas.Canvas, f *fx.Fx) {
	h, w := c.H, c.W
	p.track()
	animated := p.Animated()
	inputRow, last := h-1, h-2
	if p.MinLen > 0 {
		inputRow, last = h-2, h-4
	}
	top := DrawBlock(c, p.Question, last, "prompt", "approve.cmd")
	if p.Hazard && top >= 3 {
		c.Fill(top-1, 0, w, "text", false, false)
		c.PutSegs(top-1, 1, HazardSegs(w-2, animated), 0, false, false)
	}
	if p.MinLen > 0 {
		p.drawReasonFooter(c)
	}
	dx := shakeDX(p.fx)
	if dx < 0 {
		dx = 0
	}
	room := w - 4
	if room < 1 {
		room = 1
	}
	start := 0
	for start < p.pos && text.Width(string(p.buf[start:p.pos])) >= room {
		start++ // scroll to keep the cursor shown
	}
	c.Fill(inputRow, 0, w, "text", false, false)
	c.PutSegs(inputRow, dx, []text.Seg{
		text.S(glyphs.G("crumb")+" ", "logo"),
		text.S(string(p.buf[start:]), "input"),
	}, 0, false, false)
	cx := dx + 2 + text.Width(string(p.buf[start:p.pos]))
	p.CursorY, p.CursorX = inputRow, min(w-2, cx)
	if cx <= w-2 {
		// show the cursor cell reversed: the canvas has no hardware cursor
		under := " "
		if p.pos < len(p.buf) {
			under = string(p.buf[p.pos])
		}
		c.PutSegs(inputRow, cx, []text.Seg{{Text: under, Style: "input", Extra: text.Reverse}}, 0, false, false)
	}
}

// drawReasonFooter draws the minimum-length prompt's footer: a hint row
// counting down and a meter across the bottom row. Gold while short, blue
// once it is enough; a refused Enter shakes it.
func (p *Prompt) drawReasonFooter(c *canvas.Canvas) {
	h, w := c.H, c.W
	s := string(p.buf)
	ok, _, hint := ReasonProgress(s, p.MinLen, p.Forbid)
	n := len([]rune(strings.TrimSpace(s)))
	dx := shakeDX(p.fx)
	if dx < 0 {
		dx = 0
	}
	var segs []text.Seg
	switch {
	case p.note != "":
		segs = []text.Seg{text.S(" "+glyphs.Ico("warn")+p.note, "hint.bad")}
	case ok:
		segs = []text.Seg{text.S(" "+glyphs.Ico("ok")+hint, "hint.ok")}
	case p.refused:
		segs = []text.Seg{text.S(" "+glyphs.Ico("fail")+hint+" before this can go on", "hint.bad")}
	default:
		segs = []text.Seg{text.S(" "+glyphs.Ico("edit")+hint, "hint.short")}
	}
	keys := "esc cancels "
	if p.Forbid != "" {
		keys = fmt.Sprintf("no %s %s ", p.Forbid, glyphs.G("sep")) + keys
	}
	c.Fill(h-3, 0, w, "text", false, false)
	c.PutSegs(h-3, dx, segs, w-text.Width(keys)-1, false, false)
	c.PutSegs(h-3, max(0, w-text.Width(keys)-1), []text.Seg{text.S(keys, "hint.keys")}, 0, false, false)
	// the meter: as wide as the screen, less its label
	label := fmt.Sprintf(" %d/%d ", n, p.MinLen)
	bw := max(4, w-2-text.Width(label))
	track := "meter.track"
	if p.refused {
		if blink, live := p.fx.T("refused"); !live || int(blink*8)%2 == 0 {
			track = "meter.track.bad"
		}
	}
	fill := "meter.short"
	if ok {
		fill = "meter.ok"
	}
	bar := fx.MeterSegs(float64(n)/float64(p.MinLen), bw, fill, track)
	c.Fill(h-1, 0, w, "text", false, false)
	x := c.PutSegs(h-1, 1+dx, bar, 0, false, false)
	if shine, live := p.fx.T("shine"); live {
		// a sweep of light along the bar the moment it fills
		at := int(shine*float64(bw+8)) - 4
		for i := at - 1; i <= at+1; i++ {
			if i >= 0 && i < bw {
				c.PutSegs(h-1, 1+i, []text.Seg{text.S(glyphs.G("block"), "meter.shine")}, 0, false, false)
			}
		}
		if sparks := []rune(glyphs.G("spark")); len(sparks) > 0 {
			spark := string(sparks[int(shine*12)%len(sparks)])
			label = label[:len(label)-1] + spark
		}
	}
	ls := "meter.label"
	switch {
	case ok:
		ls = "meter.label.ok"
	case n > 0:
		ls = "meter.label.short"
	}
	c.PutSegs(h-1, x, []text.Seg{text.S(label, ls)}, 0, false, false)
}
