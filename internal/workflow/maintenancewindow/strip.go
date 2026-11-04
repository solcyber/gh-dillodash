package maintenancewindow

// The night strip is a one-row timeline on a night's manifest row, in
// its title's place (the title repeats the night above it): the
// window from its start to its end (or to the last slot, when one runs
// later) as a rule, one mark per manifest item at its slot in run order,
// colored by how it stands, and a gold "now" mark while the night is on.
// It reads as the night's shape at a glance: what runs when, what is
// done, what failed, where the clock is.

import (
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// stripMax is the widest the strip's rule grows.
const stripMax = 72

// slotMark is one item on the strip.
type slotMark struct {
	at    time.Time
	style string
	glyph string
}

// itemMark is how an item stands: done blue, failed gold, running a
// bright gold, scheduled a blue dot, left out (bumped, rolled) a dim gold
// dot.
func itemMark(it Item) (string, string) {
	st := strings.ToLower(it.Status)
	on := glyphs.G("bar_on")
	switch {
	case it.Final != nil && *it.Final && (strings.Contains(st, "fail") || strings.Contains(st, "refused") || strings.Contains(st, "revert")):
		return glyphs.G("block"), "ci.fail"
	case it.Final != nil && *it.Final:
		return on, "ci.pass"
	case strings.Contains(st, "running") || strings.Contains(st, "applying") || strings.Contains(st, "releasing") || strings.Contains(st, "deploying"):
		return on, "rev.attend"
	case it.Flag == "scheduled" || it.Flag == "attended-only":
		return glyphs.G("bullet"), "ci.run"
	}
	return glyphs.G("bullet"), "sum.fail"
}

// stripSegs is the strip of a night width cells wide at most, or nil when
// there is nothing to place (no slots yet, the night not on).
func (w *Workflow) stripSegs(info *Night, night string, now time.Time, width int) []text.Seg {
	if info == nil || !info.Live() {
		return nil
	}
	from, err1 := WallToUTC(night, w.cfg.Start, w.cfg)
	to, err2 := WallToUTC(night, w.cfg.End, w.cfg)
	if err1 != nil || err2 != nil {
		return nil
	}
	if !to.After(from) {
		to = to.Add(24 * time.Hour) // a window that crosses midnight
	}
	var marks []slotMark
	for _, it := range info.Items {
		if it.StartUTC == "" {
			continue
		}
		t, err := clock.ParseUTC(it.StartUTC)
		if err != nil {
			continue
		}
		g, st := itemMark(it)
		marks = append(marks, slotMark{at: t, style: st, glyph: g})
		if t.After(to) {
			to = t.Add(time.Minute)
		}
		if t.Before(from) {
			from = t
		}
	}
	nowOn := !now.Before(from) && now.Before(to)
	if len(marks) == 0 && !nowOn {
		return nil
	}
	sort.SliceStable(marks, func(a, b int) bool { return marks[a].at.Before(marks[b].at) })
	local := w.cfg.Local()
	left := local.TAt(from)
	right := local.TAt(to)
	if i := strings.IndexByte(left, ' '); i > 0 {
		left = left[:i] // the zone once, on the right
	}
	n := min(stripMax, width-text.Width(left)-text.Width(right)-2)
	if n < 8 {
		return nil
	}
	span := to.Sub(from)
	col := func(t time.Time) int {
		c := int(float64(n-1) * float64(t.Sub(from)) / float64(span))
		return max(0, min(n-1, c))
	}
	cells := make([]text.Seg, n)
	rule := glyphs.G("rule")
	for i := range cells {
		cells[i] = text.S(rule, "rule")
	}
	// items in run order: a later item that lands on a taken cell moves
	// right, so every one shows
	taken := map[int]bool{}
	for _, m := range marks {
		c := col(m.at)
		for taken[c] && c < n-1 {
			c++
		}
		taken[c] = true
		cells[c] = text.S(m.glyph, m.style)
	}
	if nowOn {
		c := col(now)
		cells[c] = text.S(glyphs.G("thumb"), "sum.attn")
	}
	segs := []text.Seg{text.S(left+" ", "faint")}
	segs = append(segs, joinStrip(cells)...)
	segs = append(segs, text.S(" "+right, "faint"))
	return segs
}

func joinStrip(cells []text.Seg) []text.Seg {
	var out []text.Seg
	for _, c := range cells {
		if k := len(out); k > 0 && out[k-1].Style == c.Style {
			out[k-1].Text += c.Text
			continue
		}
		out = append(out, c)
	}
	return out
}

// stripLegend is the help legend's strip line.
func (w *Workflow) stripLegend() []text.Seg {
	on, off := glyphs.G("bar_on"), glyphs.G("bullet")
	return []text.Seg{text.S(text.Pad("", 10), "section"),
		text.S(off, "ci.run"), text.S(" scheduled  ", "muted"),
		text.S(on, "rev.attend"), text.S(" running  ", "muted"),
		text.S(on, "ci.pass"), text.S(" done  ", "muted"),
		text.S(glyphs.G("block"), "ci.fail"), text.S(" failed  ", "muted"),
		text.S(glyphs.G("thumb"), "sum.attn"), text.S(" now: the night's timeline", "muted")}
}
