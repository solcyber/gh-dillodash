// Package glyphs holds the three icon sets the dashboard draws with and
// the ASCII art it shows at startup.
//
// Three sets, picked by `icons =` / --icons (default nerd). Every status
// has its own SHAPE as well as its own color, so no state depends on
// telling two colors apart. nerd = Nerd Fonts Octicons, GitHub's own icon
// set: BMP private-use code points (U+F4xx/F5xx, the powerline caps
// U+E0B4/E0B6) only, never the U+F0000+ nf-md range, which some wcwidth
// tables count as unprintable. The font test checks every glyph of the
// nerd and unicode sets. ascii keeps the original + x . ~ - glyphs for a
// terminal that cannot show non-ASCII (icons = "ascii" or --icons ascii;
// nothing detects it, the CI column would otherwise read as "no CI").
package glyphs

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// Set names one of the three glyph sets.
type Set string

// The glyph sets.
const (
	Nerd    Set = "nerd"
	Unicode Set = "unicode"
	ASCII   Set = "ascii"
)

// common is shared by the nerd and unicode sets.
var common = map[string]string{
	"gutter": "▌", "bullet": "•", "quote": "│", "cont": "↪",
	"sep": "·", "more": "…", "arrow": "→", "rule": "─",
	"vbar": "│", "cross": "┼", // table cell bar, and where it meets a rule
	"box": "╭╮╰╯─│", "bar_on": "▮",
	"bar_off": "▯", "thumb": "┃", "track": "│",
	"h1": "█", "h2": "▸", "h3": "▹", "fold": "▾", "section": "▌",
	"spinner": "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏",
	"crumb":   "›", "sel": "▌",
	// motion and meters: partial cells 1/8..7/8, the meter's trough
	// texture, a wipe's soft edge, a rule's gleam, sparks, the rolling
	// armadillo, the destroy prompt's hazard stripe, logotype pixels
	"eighths": "▏▎▍▌▋▊▉", "block": "█", "trough": "░", "shade": "░▒▓",
	"gleam": "━", "spark": "·•◦⋆◇◆", "ball": "◐◓◑◒", "hazard": "▚▞",
	"px": "█╗╔═╝╚║",
}

var nerd = merge(common, map[string]string{
	"logo": "", "pr": "", "draft": "", "issue": "",
	"queued": "", "window": "", "pass": "",
	"fail": "", "wait": "",
	"skip": "", "unknown": "", "comment": "",
	"person": "", "branch": "", "clock": "",
	"rev": "", "attend": "", "live": "",
	"nochange": "", "skipped": "", "applied": "",
	"dead": "", "shield": "", "warn": "", "ok": "",
	"err": "", "info": "", "search": "",
	"filter": "", "tag": "", "sync": "",
	"file.added": "", "file.removed": "",
	"file.modified": "", "file.renamed": "",
	"file.other": "", "capl": "", "capr": "",
	"budget": "", "pl": "", "edit": "",
	// application releases: reuse verified code points
	"release": "", "hotfix": "", "stage": "",
	// fortress mode: Dwarf Fortress's CP437 look where Hack has the
	// glyph; Font Awesome stand-ins for the smiley, fire, sun and tree
	"df.dwarf": "", "df.draft": "", "df.goblin": "g",
	"df.troll": "T", "df.ruin": "░▒▓", "df.statue": "Ω",
	"df.glint": "", "df.fire": "", "df.alert": "!",
	"df.sleep": "z", "df.merchant": "$", "df.wagon": "■",
	"df.wagon.hold": "□", "df.water": "≈~", "df.wild": "\"',.",
	"df.tree": "τ", "df.ground": "·", "df.street": "─",
	"df.road": "║", "df.bridge": "═", "df.rail": "╬", "df.cursor": "X",
	"df.up": "▲", "df.down": "▼",
})

var unicodeSet = merge(common, map[string]string{
	"logo": "◈", "pr": "◆", "draft": "◇", "issue": "●",
	"queued": "◷", "window": "◷", "pass": "√", "fail": "×", "wait": "◦",
	"skip": "−", "unknown": "?", "comment": "≡", "person": "@",
	"branch": "»", "clock": "◷", "rev": "↺", "attend": "◉",
	"live": "●", "nochange": "○", "skipped": "−",
	"applied": "▣", "dead": "⊘", "shield": "◈", "warn": "!",
	"ok": "√", "err": "×", "info": "·", "search": "/",
	"filter": "▽", "tag": "@", "sync": "↻", "file.added": "+",
	"file.removed": "−", "file.modified": "±",
	"file.renamed": "→", "file.other": "·", "capl": "▐",
	"capr": "▌", "budget": "◔", "pl": "▌", "edit": "»",
	"release": "◆", "hotfix": "!", "stage": "»",
	"df.dwarf": "@", "df.draft": "ø", "df.goblin": "g", "df.troll": "T",
	"df.ruin": "░▒▓", "df.statue": "Ω", "df.glint": "¤", "df.fire": "‼",
	"df.alert": "!", "df.sleep": "z", "df.merchant": "$",
	"df.wagon": "■", "df.wagon.hold": "□", "df.water": "≈~",
	"df.wild": "\"',.", "df.tree": "τψ", "df.ground": "·",
	"df.street": "─", "df.road": "║", "df.bridge": "═", "df.rail": "╬",
	"df.cursor": "X", "df.up": "▲", "df.down": "▼",
})

// ascii leaves decorative icons out ("") rather than invent letters.
var ascii = map[string]string{
	"logo": "#", "pr": "P", "draft": "d", "issue": "i", "queued": "q",
	"window": "",
	"pass":   "+", "fail": "x", "wait": ".", "skip": "-", "unknown": "?",
	"comment": "c", "person": "@", "branch": "", "clock": "", "rev": "r",
	"attend": "!", "live": "*", "nochange": "o", "skipped": "-",
	"applied": "=", "dead": "/", "shield": "", "warn": "!", "ok": "+",
	"err": "x", "info": "-", "search": "/", "filter": "/", "tag": "@",
	"sync": "", "file.added": "A", "file.removed": "D",
	"file.modified": "M", "file.renamed": "R", "file.other": "?",
	"capl": "[", "capr": "]", "budget": "",
	"gutter": ">", "bullet": "*", "quote": "|", "cont": ">", "sep": "-",
	"more": "..", "arrow": "->", "rule": "-", "vbar": "|", "cross": "+",
	"box":    "++++-|",
	"bar_on": "#", "bar_off": "-", "thumb": "#", "track": "|", "h1": "#",
	"h2": "#", "h3": ">", "fold": "v", "section": "#", "crumb": ">",
	// steady: an animated |/-\ would pass through "-", which is skip
	"spinner": "~",
	"sel":     ">", "pl": "", "edit": "",
	"release": "R", "hotfix": "!", "stage": "s",
	// no partial cells in ascii: meters fill whole cells of "#"
	"eighths": "", "block": "#", "trough": "-", "shade": ":|#",
	"gleam": "=", "spark": ".+*o", "ball": "o0Oo", "hazard": "/\\",
	"px":       "#",
	"df.dwarf": "@", "df.draft": "o", "df.goblin": "g", "df.troll": "T",
	"df.ruin": ".:#", "df.statue": "^", "df.glint": "+", "df.fire": "*",
	"df.alert": "!", "df.sleep": "z", "df.merchant": "$",
	"df.wagon": "W", "df.wagon.hold": "w", "df.water": "~~",
	"df.wild": "\"',.", "df.tree": "%", "df.ground": ".",
	"df.street": "-", "df.road": "|", "df.bridge": "=", "df.rail": "+",
	"df.cursor": "X", "df.up": "^", "df.down": "v",
}

var sets = map[Set]map[string]string{Nerd: nerd, Unicode: unicodeSet, ASCII: ascii}

func merge(base, over map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

var (
	mu     sync.RWMutex
	active = Nerd
	g      = nerd
	missed = map[string]bool{}
)

// Use switches the active set (an unknown name selects nerd) and points
// text.Ellipsis at its "more" glyph.
func Use(set Set) {
	mu.Lock()
	if _, ok := sets[set]; !ok {
		set = Nerd
	}
	active, g = set, sets[set]
	text.Ellipsis = g["more"]
	mu.Unlock()
}

// Active is the set in use.
func Active() Set {
	mu.RLock()
	defer mu.RUnlock()
	return active
}

// G is the active set's glyph for name. An unknown name is a bug in the
// caller, not the terminal: it draws as "?" and is recorded (see Missed).
func G(name string) string {
	mu.RLock()
	s, ok := g[name]
	mu.RUnlock()
	if !ok {
		mu.Lock()
		missed[name] = true
		mu.Unlock()
		return "?"
	}
	return s
}

// Missed lists every unknown name G was asked for, sorted (test
// support: the glyph tests assert nothing is missed).
func Missed() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(missed))
	for n := range missed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Ico is a glyph plus a no-break space (so wrapping never strands an icon
// at the end of a line, away from its text), or "" when the set has none:
// the ascii set leaves decorative icons out rather than invent letters.
func Ico(name string) string {
	mu.RLock()
	s, ok := g[name]
	set := active
	mu.RUnlock()
	if !ok {
		mu.Lock()
		missed[name] = true
		mu.Unlock()
		return ""
	}
	if s == "" {
		return ""
	}
	if set == ASCII {
		return s + " "
	}
	return s + " "
}

var start = time.Now()

// SpinnerFrame is the spinner glyph for this instant, ten frames a second.
func SpinnerFrame() string {
	frames := []rune(G("spinner"))
	if len(frames) == 0 {
		return ""
	}
	n := int(time.Since(start).Milliseconds()/100) % len(frames)
	return string(frames[n])
}

// Names lists every glyph name, sorted. All three sets define the same
// names.
func Names() []string {
	out := make([]string, 0, len(nerd))
	for n := range nerd {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All is a copy of one set (nil for an unknown set).
func All(set Set) map[string]string {
	src, ok := sets[set]
	if !ok {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// reportNames is the order the --glyph-test table lists the status icons.
var reportNames = []string{
	"logo", "pr", "draft", "issue", "queued", "pass", "fail", "wait",
	"skip", "unknown", "comment", "person", "branch", "clock", "rev",
	"attend", "live", "nochange", "applied", "dead", "shield", "warn",
	"search", "filter", "sync", "file.added", "file.removed",
	"file.modified", "file.renamed", "budget", "capl", "capr",
	"release", "hotfix", "stage",
}

// Report writes the --glyph-test table: every glyph in every set, so you
// can see whether the terminal's font has them (empty boxes / '?' in the
// nerd column = not a Nerd Font). Each palette func (the theme's color
// dump, for a tty) is written after the table.
func Report(w io.Writer, palette ...func(io.Writer)) {
	none := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	fmt.Fprint(w, "dillodash glyph test\n\n")
	fmt.Fprint(w, "The nerd column needs a Nerd Font. If it shows empty boxes or '?',\n"+
		"set your TERMINAL's font to a Nerd Font such as \"Hack Nerd Font Mono\";\n"+
		"over SSH, install it on the machine the terminal runs on -- or run\n"+
		"with --icons unicode.\n\n")
	fmt.Fprintf(w, "%-15s %-6s %-8s %s\n", "glyph", "nerd", "unicode", "ascii")
	for _, n := range reportNames {
		fmt.Fprintf(w, "%-15s %-6s %-8s %s\n", n, nerd[n], unicodeSet[n], none(ascii[n]))
	}
	spin := []rune(common["spinner"])
	fmt.Fprintf(w, "%-15s %-6s %-8s %s\n", "spinner", string(spin[:4]), "", "~")
	for _, n := range []string{"pl", "edit"} {
		fmt.Fprintf(w, "%-15s %-6s %-8s %s\n", n, nerd[n], unicodeSet[n], none(ascii[n]))
	}
	fmt.Fprint(w, "\nmotion and meters (every set but ascii):\n")
	for _, n := range []string{"eighths", "block", "trough", "shade", "gleam", "spark", "ball", "hazard", "px"} {
		fmt.Fprintf(w, "%-15s %-14s %s\n", n, common[n], none(ascii[n]))
	}
	fmt.Fprint(w, "\n")
	for _, p := range palette {
		p(w)
	}
}
