package ui

import (
	"testing"

	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// TestAConfirmHoldsTheCursor: while a question is open about the row, a
// refresh keeps the cursor on that item, neither following a new head
// of "your turn" nor moving to the next to-do; the idle clock leaves it
// alone too.
func TestAConfirmHoldsTheCursor(t *testing.T) {
	h := newHarness(t)
	setPRs(h, reviews(101)...)
	h.load()
	h.m.mu.Lock()
	h.m.modal = widgets.NewConfirm("approve #101?")
	h.m.allPRs = append(reviews(99), h.m.allPRs...) // what a refresh lands
	h.m.refilter()
	h.m.goHome()
	h.m.mu.Unlock()
	if got := cursorAt(h); got != "pin#101" {
		t.Errorf("unplaced, confirm open on #101: cursor on %s", got)
	}

	h = newHarness(t)
	setPRs(h, reviews(101, 103, 104)...)
	h.load()
	put(h, "pr", 103, true)
	h.m.mu.Lock()
	h.m.modal = widgets.NewConfirm("approve #103?")
	for _, p := range h.m.allPRs {
		if p.Number == 103 {
			p.IsDraft = true
		}
	}
	h.m.refilter()
	h.m.mu.Unlock()
	if got := cursorAt(h); got != "home#103" {
		t.Errorf("confirm open on #103 as it left your turn: cursor on %s", got)
	}
}

// TestIdleLeavesAnActionAndAnOverlayAlone: a pager, an editor or the help
// open longer than idle_after_secs does not move the cursor under it.
func TestIdleLeavesAnActionAndAnOverlayAlone(t *testing.T) {
	for _, c := range []string{"action", "overlay"} {
		h := newHarness(t)
		setPRs(h, reviews(101)...)
		h.load()
		put(h, "issue", 7, false)
		h.m.mu.Lock()
		h.m.cfg.Display.IdleAfterSecs = 300
		h.m.lastInput = fx.Now() - 400
		if c == "action" {
			h.m.action = true
		} else {
			h.m.overlay = "help"
		}
		h.m.goHome()
		h.m.mu.Unlock()
		if got := cursorAt(h); got != "home#7" {
			t.Errorf("%s: idle moved the cursor to %s", c, got)
		}
	}
}

// TestAFilterTypoAndBackspace: the row the filter started from comes back
// as soon as it matches again.
func TestAFilterTypoAndBackspace(t *testing.T) {
	h := newHarness(t)
	r := h.gh.Open[infra]
	r.Issues = append(r.Issues, issue(infra, 8, "wip notes", "carol"))
	h.gh.Open[infra] = r
	h.load()
	put(h, "issue", 7, false) // "rotate the signing keys"
	var during string
	h.m.tp.Prompt = func(_ string, o workflow.PromptOpts) (string, bool) {
		h.m.mu.Lock()
		o.OnChange("e")
		o.OnChange("en") // a typo: #7 stops matching
		o.OnChange("e")  // a backspace: it matches again
		h.m.mu.Unlock()
		during = cursorAt(h)
		return "e", true
	}
	h.key('/')
	if during != "home#7" {
		t.Errorf("after the backspace: %s, want home#7", during)
	}
	if got := cursorAt(h); got != "home#7" {
		t.Errorf("after Enter: %s, want home#7", got)
	}
}
