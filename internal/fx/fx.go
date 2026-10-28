// Package fx is the motion layer: one-shot effects by name (wipes,
// cascades, confetti, a shake), the shared ambient clock, easings, and the
// closed-form particle systems the splash, the confetti and the release
// launch draw with.
//
// Motion is "full" (the default) or "off": off is the static dashboard,
// every effect a no-op and the loop ticking only for the spinner and the
// countdowns. Effects decorate; they never hide, delay or animate the text
// a decision rests on. The command, a question and a destroy count are
// drawn whole on their first frame, and only what is around them moves.
//
// One-shot effects tick at FrameMS. Ambient ones (the breathing gutter,
// chip glints, gleams, the header's logo) tick at AmbientMS and rest after
// AmbientIdleSecs without a key, so an unattended dashboard costs no more
// than a static one.
package fx

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

const (
	FrameMS         = 33    // ~30 fps while a one-shot effect plays
	AmbientMS       = 100   // ambient effects only
	AmbientIdleSecs = 300.0 // no key for 5 minutes: ambient effects rest
	ToastSecs       = 6.0   // how long a status message stays up
	GlideSecs       = 0.14  // smooth scroll, for page-sized jumps
	SplashMinSecs   = 1.1   // the startup splash, at the least
)

// Motion is "full" or "off".
var Motion = "full"

// Ambient is set by the app every frame: whether ambient effects play.
var Ambient = false

var epoch = time.Now()

// Now is the monotonic clock in seconds; tests replace it.
var Now = func() float64 { return time.Since(epoch).Seconds() }

// Clamp01 clamps to [0, 1].
func Clamp01(t float64) float64 {
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// EaseOut: fast start, soft landing (cubic).
func EaseOut(t float64) float64 {
	u := 1 - Clamp01(t)
	return 1 - u*u*u
}

// EaseInOut is a cubic ease in both directions.
func EaseInOut(t float64) float64 {
	t = Clamp01(t)
	if t < 0.5 {
		return 4 * t * t * t
	}
	u := -2*t + 2
	return 1 - u*u*u/2
}

// EaseOutBack overshoots ~10% before it settles: a pop.
func EaseOutBack(t float64) float64 {
	t = Clamp01(t)
	u := t - 1
	return 1 + 2.70158*u*u*u + 1.70158*u*u
}

// Wave is 0..1 repeating every period seconds: the clock ambient effects
// share, so everything on screen moves in step.
func Wave(period, offset float64) float64 {
	v := math.Mod(Now()/period+offset, 1)
	if v < 0 {
		v += 1
	}
	return v
}

// Tri maps p in 0..1 to 0 -> 1 -> 0.
func Tri(p float64) float64 {
	p = math.Mod(p, 1)
	if p < 0 {
		p += 1
	}
	return 1 - math.Abs(2*p-1)
}

// ShakeDX is a refusal's horizontal shake, one cell each way.
func ShakeDX(t float64) int {
	return []int{0, 1, -1, 1, -1, 0}[min(5, int(Clamp01(t)*6))]
}

type entry struct {
	t0   float64
	secs float64
	data map[string]any
}

// Fx holds the live one-shot effects by name.
type Fx struct {
	live map[string]entry
}

// New makes an empty effect set.
func New() *Fx { return &Fx{live: map[string]entry{}} }

// Start stamps an effect (a no-op with motion off).
func (f *Fx) Start(name string, secs float64, data map[string]any) {
	if Motion != "full" {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	f.live[name] = entry{Now(), secs, data}
}

// Stop ends an effect.
func (f *Fx) Stop(name string) { delete(f.live, name) }

// Elapsed is the seconds since the effect started, and whether it is live.
func (f *Fx) Elapsed(name string) (float64, bool) {
	e, ok := f.live[name]
	if !ok {
		return 0, false
	}
	return Now() - e.t0, true
}

// T is the effect's progress 0..1 while it plays; ok is false once over.
func (f *Fx) T(name string) (float64, bool) {
	e, ok := f.live[name]
	if !ok {
		return 0, false
	}
	p := (Now() - e.t0) / e.secs
	if p >= 1 {
		delete(f.live, name)
		return 0, false
	}
	return math.Max(0, p), true
}

// Data returns the effect's data (empty when not live).
func (f *Fx) Data(name string) map[string]any {
	if e, ok := f.live[name]; ok {
		return e.data
	}
	return map[string]any{}
}

// Active says whether anything plays (the loop then ticks at FrameMS).
func (f *Fx) Active() bool {
	now := Now()
	for n, e := range f.live {
		if now-e.t0 >= e.secs {
			delete(f.live, n)
		}
	}
	return len(f.live) > 0
}

// Int reads an integer out of an effect's data.
func (f *Fx) Int(name, key string, def int) int {
	switch v := f.Data(name)[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

// Bool reads a boolean out of an effect's data.
func (f *Fx) Bool(name, key string) bool {
	v, _ := f.Data(name)[key].(bool)
	return v
}

// Seed reads an effect's seed.
func (f *Fx) Seed(name string) uint64 {
	switch v := f.Data(name)["seed"].(type) {
	case uint64:
		return v
	case int:
		return uint64(v)
	case int64:
		return uint64(v)
	}
	return 0
}

func runes(name string) []rune { return []rune(glyphs.G(name)) }

// MeterSegs is a progress bar exactly width cells wide at frac (0..1):
// whole blocks, then one partial cell at 1/8-cell precision where the
// glyph set has the eighth blocks (ascii fills whole cells), then the track.
func MeterSegs(frac float64, width int, fill, track string) []text.Seg {
	if width <= 0 {
		return nil
	}
	frac = Clamp01(frac)
	eighths := runes("eighths")
	block := glyphs.G("block")
	trough := glyphs.G("trough")
	var full, part int
	if len(eighths) > 0 {
		n := int(math.Round(frac * float64(width) * 8))
		full, part = n/8, n%8
	} else {
		full = int(math.Round(frac * float64(width)))
	}
	var segs []text.Seg
	if full > 0 {
		segs = append(segs, text.S(repeat(block, full), fill))
	}
	if part > 0 && full < width {
		segs = append(segs, text.S(string(eighths[part-1]), fill))
		full++
	}
	if width-full > 0 {
		segs = append(segs, text.S(repeat(trough, width-full), track))
	}
	return segs
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// ScannerSegs is a scanner bar width cells wide: a bright head sweeping
// to and fro at phase t (0..1) with a glow fading off both sides of it.
func ScannerSegs(width int, t float64) []text.Seg {
	head := Tri(t) * float64(width-1)
	gleam, rule := glyphs.G("gleam"), glyphs.G("rule")
	var segs []text.Seg
	for i := 0; i < width; i++ {
		d := math.Abs(float64(i) - head)
		var st string
		switch {
		case d < 1:
			st = "fx.b7"
		case d < 2.5:
			st = "fx.b6"
		case d < 4.5:
			st = "fx.b5"
		case d < 7:
			st = "fx.b4"
		case d < 10:
			st = "fx.b2"
		default:
			st = "rule"
		}
		ch := gleam
		if st == "rule" {
			ch = rule
		}
		if n := len(segs); n > 0 && segs[n-1].Style == st {
			segs[n-1].Text += ch
		} else {
			segs = append(segs, text.S(ch, st))
		}
	}
	return segs
}

// Ramp is the style prefix0..prefix(n-1) at position t (0..1).
func Ramp(prefix string, n int, t float64) string {
	i := int(Clamp01(t) * float64(n))
	if i > n-1 {
		i = n - 1
	}
	return prefix + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Cell is one particle on screen.
type Cell struct {
	Y, X  int
	Glyph string
	Style string
}

// Particles is confetti t seconds after launch. Closed form, no
// per-frame state: each particle's launch comes from seed, and gravity
// pulls it back down (cells are about twice as tall as wide, so sideways
// speeds run higher).
func Particles(seed uint64, count int, t float64, y0, x0 int, spread float64) []Cell {
	rnd := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	sparks := runes("spark")
	styles := []string{"fx.b4", "fx.b5", "fx.b6", "fx.b7", "fx.g2", "fx.g3", "fx.k5"}
	var out []Cell
	for i := 0; i < count; i++ {
		x := float64(x0) + uniform(rnd, -spread, spread)
		vx, vy := uniform(rnd, -24, 24), uniform(rnd, -26, -9)
		delay := uniform(rnd, 0, 0.25)
		glyph := " "
		if len(sparks) > 0 {
			glyph = string(sparks[rnd.IntN(len(sparks))])
		}
		style := styles[rnd.IntN(len(styles))]
		tt := t - delay
		if tt < 0 {
			continue
		}
		out = append(out, Cell{
			Y:     int(math.Round(float64(y0) + vy*tt + 17*tt*tt)),
			X:     int(math.Round(x + vx*tt)),
			Glyph: glyph, Style: style,
		})
	}
	return out
}

func uniform(r *rand.Rand, a, b float64) float64 { return a + r.Float64()*(b-a) }

// The release launch: a rocket, fireworks, a shockwave, RELEASED! in
// block letters, marching lights round the screen and an armadillo
// stampede on the key bar. Loud on purpose, but it never strobes: no cell
// changes color more than three times a second (WCAG 2.3.1), and it keeps
// to the colorblind-safe palette. Any key ends it, and still does its job.
const LaunchSecs = 6.0

// LaunchCycle is the color cycle of the marching lights and the letters.
var LaunchCycle = []string{"fx.b4", "fx.b5", "fx.b6", "fx.b7", "fx.g3", "fx.g2", "fx.g1", "fx.b3"}

// LaunchFont is a 5-row block font for the launch word.
var LaunchFont = map[rune][]string{
	'R': {"#### ", "#   #", "#### ", "#  # ", "#   #"},
	'E': {"#####", "#    ", "#### ", "#    ", "#####"},
	'L': {"#    ", "#    ", "#    ", "#    ", "#####"},
	'A': {" ### ", "#   #", "#####", "#   #", "#   #"},
	'S': {" ####", "#    ", " ### ", "    #", "#### "},
	'D': {"#### ", "#   #", "#   #", "#   #", "#### "},
	'!': {"#", "#", "#", " ", "#"},
}

// LaunchWord is what drops in letter by letter.
const LaunchWord = "RELEASED!"

// LaunchRocket and LaunchFlame are the rocket's art and its flame frames.
var (
	LaunchRocket = []string{"  /\\  ", " /  \\ ", " |()| ", " |  | ", "/|__|\\"}
	LaunchFlame  = []string{"  ''  ", " ^^^^ ", "  ^^  "}
)

// LaunchWordWidth is the block-letter word's width in cells.
func LaunchWordWidth() int {
	w := 0
	for _, c := range LaunchWord {
		w += len(LaunchFont[c][0]) + 1
	}
	return w - 1
}

// Burst is one firework: when it starts, where, and its color.
type Burst struct {
	Start float64
	Y, X  int
	Style string
}

// LaunchBursts are the fireworks after the rocket bursts. The first is
// the rocket itself, at the top of its climb.
func LaunchBursts(seed uint64, w, top, bottom int) []Burst {
	rnd := rand.New(rand.NewPCG(seed, seed+1))
	out := []Burst{{1.5, top, w / 2, "fx.g3"}}
	for i := 0; i < 7; i++ {
		ymax := max(top, (top+bottom)/2+2)
		xmax := max(4, w-5)
		out = append(out, Burst{
			Start: 1.8 + 0.42*float64(i),
			Y:     top + rnd.IntN(ymax-top+1),
			X:     4 + rnd.IntN(xmax-4+1),
			Style: LaunchCycle[rnd.IntN(len(LaunchCycle))],
		})
	}
	return out
}

// BurstCells is one firework t seconds after it bursts: a ring of sparks
// flying out, sagging under gravity, cooling to gray.
func BurstCells(t float64, y0, x0 int, style string, n int) []Cell {
	if t < 0 || t >= 1.4 {
		return nil
	}
	sparks := runes("spark")
	st := style
	if t >= 1.1 {
		st = "fx.k2"
	} else if t >= 0.8 {
		st = "fx.k4"
	}
	out := make([]Cell, 0, n)
	for k := 0; k < n; k++ {
		a := 2 * math.Pi * float64(k) / float64(n)
		r := 16.0 * EaseOut(t/1.4)
		glyph := "*"
		if len(sparks) > 0 {
			glyph = string(sparks[k%len(sparks)])
		}
		out = append(out, Cell{
			Y:     int(math.Round(float64(y0) + math.Sin(a)*r*0.5 + 5*t*t)),
			X:     int(math.Round(float64(x0) + math.Cos(a)*r)),
			Glyph: glyph, Style: st,
		})
	}
	return out
}
