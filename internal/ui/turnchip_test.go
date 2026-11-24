package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// TestAYourTurnRowStopsShortOfItsChip: the row is laid out to end before
// its key's chip, so a long title ends on an ellipsis instead of being
// cut under the chip, at every width the chip shows.
func TestAYourTurnRowStopsShortOfItsChip(t *testing.T) {
	h := newHarness(t)
	r := h.gh.Open[infra]
	r.PRs = append(r.PRs, pr(infra, 103, "widen the deploy role for the warehouse functions and the nightly loader", "bob", false))
	r.PRs = r.PRs[1:] // #101 out: the long one is the only review on me
	h.gh.Open[infra] = r
	h.load()
	for _, w := range []int{140, 100, 80, 64} {
		h.m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		var line string
		for _, l := range strings.Split(h.screen(), "\n") {
			if strings.Contains(l, "review it") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("w=%d: no chip on the pinned row:\n%s", w, h.screen())
		}
		before, _, _ := strings.Cut(line, "review it")
		before = strings.TrimRight(before, " ")
		before = strings.TrimSuffix(before, "enter")
		before = strings.TrimSuffix(before, "↵")
		before = strings.TrimRight(before, " ")
		if !strings.HasSuffix(before, text.Ellipsis) && !strings.HasSuffix(before, "nightly loader") {
			t.Errorf("w=%d: the title is cut under the chip: %q", w, line)
		}
		if !strings.Contains(before, "widen the") {
			t.Errorf("w=%d: the title is crushed: %q", w, line)
		}
	}
}
