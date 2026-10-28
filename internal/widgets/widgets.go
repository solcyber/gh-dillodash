// Package widgets holds the modal components of the dashboard: the
// one-line prompt, the y/N confirm and the boxed pick menu, plus the
// drawing helpers they share (a word-wrapped question block, the reason
// meter, the hazard stripe, a rounded box).
//
// They know nothing of the terminal framework: the app normalises each
// key press into a Key and feeds it to Handle, and asks Draw to paint the
// modal over the frame it has already drawn. Styles stay names; the theme
// resolves them at render time.
package widgets

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// Key is a normalized key press: a printable rune, or a named key.
type Key struct {
	Rune rune // the printable rune, 0 for a named key
	// Name is "enter", "esc", "backspace", "delete", "up", "down",
	// "left", "right", "home", "end", "pgup", "pgdn", "tab", "ctrl+u",
	// "ctrl+w", "ctrl+a", "ctrl+e", "ctrl+k", "ctrl+c" (and the other
	// ctrl+<letter> chords), "" for a plain rune.
	Name string
}

// R is a plain-rune key.
func R(r rune) Key { return Key{Rune: r} }

// K is a named key.
func K(name string) Key { return Key{Name: name} }

// Modal is a component the app feeds keys and asks to draw.
type Modal interface {
	// Handle consumes a key; done reports the modal finished (its result
	// is then available on the component).
	Handle(k Key) (done bool)
	// Draw paints the modal over the current frame (the app draws the
	// base frame first). f is the app's effect set, read for the timers
	// a caller names (the menu's pop); it may be nil.
	Draw(c *canvas.Canvas, f *fx.Fx)
	// Animated says whether the modal moves on its own and the loop
	// should tick while it is up (the reason meter, the hazard stripe,
	// a breathing /approve line, the menu's pop and ants).
	Animated() bool
}

// ApproveCmdRe matches an /approve command line: the 64-hex plan id and
// its optional --confirm-destroys / --outside-window flags.
var ApproveCmdRe = regexp.MustCompile(
	`^/approve [ \t\r\n\f\v]*([0-9a-f]{64})` +
		`((?:[ \t]+--(?:confirm-destroys=[0-9]+|outside-window="[^"\r\n]*"))*)` +
		`[ \t\r\n\f\v]*$`)

// Wrap word-wraps s to width cells the way textwrap.wrap does: words move
// whole to the next line, a word wider than a line is broken, whitespace
// at a line's ends is dropped. An empty string wraps to no lines.
func Wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	cur := ""
	used := 0
	flush := func() {
		lines = append(lines, strings.TrimRight(cur, " "))
		cur, used = "", 0
	}
	for _, chunk := range chunks(s) {
		space := chunk[0] == ' '
		for chunk != "" {
			w := text.Width(chunk)
			if space && used == 0 {
				break // leading whitespace is dropped
			}
			if used+w <= width {
				cur += chunk
				used += w
				break
			}
			if space {
				flush() // the line ends; the whitespace goes
				break
			}
			if w > width {
				// a word wider than a line: fill what room is left
				head := text.Clip(chunk, width-used)
				if head != "" {
					cur += head
					used += text.Width(head)
					chunk = chunk[len(head):]
				}
				flush()
				continue
			}
			flush()
		}
	}
	if used > 0 {
		flush()
	}
	return lines
}

// chunks splits s into alternating runs of whitespace (as spaces) and
// non-whitespace, like textwrap's chunking.
func chunks(s string) []string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r', '\f', '\v':
			return ' '
		}
		return r
	}, s)
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if i > 0 && (s[i] == ' ') != (s[i-1] == ' ') {
			out = append(out, s[start:i])
			start = i
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// DrawBlock draws text word-wrapped to the canvas width so its last line
// lands on lastRow, on cleared rows. A paragraph holding an /approve
// command is drawn in cmdStyle. Returns the first row used.
func DrawBlock(c *canvas.Canvas, s string, lastRow int, style, cmdStyle string) int {
	w := c.W
	width := w - 3
	if width < 10 {
		width = 10
	}
	type line struct{ t, st string }
	var lines []line
	for _, para := range strings.Split(s, "\n") {
		st := style
		if ApproveCmdRe.MatchString(strings.TrimSpace(para)) {
			st = cmdStyle
		}
		wrapped := Wrap(para, width)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for _, ln := range wrapped {
			lines = append(lines, line{ln, st})
		}
	}
	keep := lastRow - 1 // never over the header rows
	if keep < 1 {
		keep = 1
	}
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	top := lastRow - len(lines) + 1
	for i, ln := range lines {
		c.Fill(top+i, 0, w, "text", false, false)
		c.PutSegs(top+i, 1, []text.Seg{text.S(ln.t, ln.st)}, 0, false, false)
	}
	return top
}

// ReasonProgress is (ok, remaining, hint) for a free-text answer with a
// minimum length, measured the way the approve validator measures the
// --outside-window reason: the length of the stripped text in characters.
func ReasonProgress(s string, minLen int, forbid string) (ok bool, remaining int, hint string) {
	remaining = minLen - utf8.RuneCountInString(strings.TrimSpace(s))
	if remaining < 0 {
		remaining = 0
	}
	for _, r := range forbid {
		if strings.ContainsRune(s, r) {
			return false, remaining, fmt.Sprintf("remove the %c — the validator refuses it", r)
		}
	}
	if remaining > 0 {
		plural := "s"
		if remaining == 1 {
			plural = ""
		}
		return false, remaining, fmt.Sprintf("%d more character%s", remaining, plural)
	}
	return true, 0, "enough — enter continues"
}

// HazardSegs is a gold hazard stripe width cells wide; it marches when
// animated.
func HazardSegs(width int, marching bool) []text.Seg {
	gl := []rune(glyphs.G("hazard"))
	if len(gl) == 0 {
		gl = []rune{'/', '\\'}
	}
	off := 0
	if marching {
		off = int(fx.Now() * 8)
	}
	var segs []text.Seg
	for i := 0; i < width; i++ {
		style := "hazard.dim"
		if ((i+off)/3)%2 == 0 {
			style = "hazard"
		}
		ch := string(gl[(i+off)%len(gl)])
		if n := len(segs); n > 0 && segs[n-1].Style == style {
			segs[n-1].Text += ch
		} else {
			segs = append(segs, text.S(ch, style))
		}
	}
	return segs
}

// shakeDX is the horizontal shake of a live "shake" effect, else 0.
func shakeDX(f *fx.Fx) int {
	if t, ok := f.T("shake"); ok {
		return fx.ShakeDX(t)
	}
	return 0
}
