package ui

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
)

// attentionHarness is a loaded dashboard on a fake clock, with idle
// after five minutes and a 300 s refresh.
func attentionHarness(t *testing.T) (*harness, *float64) {
	t.Helper()
	h := newHarness(t)
	was := fx.Now
	t.Cleanup(func() { fx.Now = was })
	now := 1000.0
	fx.Now = func() float64 { return now }
	h.m.cfg.Display.IdleAfterSecs = 300
	h.m.cfg.Display.RefreshSecs = 300
	h.m.lastInput = now
	h.load()
	return h, &now
}

func TestAttentionSlowsTheRefreshAndCatchesUp(t *testing.T) {
	h, now := attentionHarness(t)
	if a := h.m.attention(); a != attended {
		t.Fatalf("just loaded: %v", a)
	}
	if got := h.m.refreshEvery(); got != 300 {
		t.Errorf("attended: every %v, want 300", got)
	}
	*now += 301
	if a := h.m.attention(); a != idle {
		t.Fatalf("no key for 301 s: %v", a)
	}
	if got := h.m.refreshEvery(); got != 600 {
		t.Errorf("idle: every %v, want 600", got)
	}
	h.m.Update(tea.BlurMsg{})
	if a := h.m.attention(); a != away {
		t.Fatalf("blurred: %v", a)
	}
	if got := h.m.refreshEvery(); got != 900 {
		t.Errorf("away: every %v, want 900 (three times, capped at 15 min)", got)
	}
	if got := h.m.pollEvery(30 * time.Second); got != awayPoll {
		t.Errorf("away poll: %v", got)
	}
	// back: focus catches up at once, a refresh in flight
	before := h.m.lastRefresh
	h.m.Update(tea.FocusMsg{})
	if !h.m.refreshing || h.m.lastRefresh == before {
		t.Errorf("focus after a long absence refreshes now: refreshing=%v", h.m.refreshing)
	}
	h.pump()
	if a := h.m.attention(); a != attended {
		t.Errorf("focused again: %v", a)
	}
}

func TestAKeyAfterIdleCatchesUpButNotTwice(t *testing.T) {
	h, now := attentionHarness(t)
	*now += 400 // idle, and the last refresh is 400 s old
	h.key('j')
	if h.m.lastRefresh != *now {
		t.Fatalf("the first key after idle refreshes: last refresh at %v, now %v", h.m.lastRefresh, *now)
	}
	*now += 5
	h.key('j')
	if h.m.lastRefresh != *now-5 {
		t.Error("an attended key does not refresh")
	}
}

func TestACatchUpRightAfterARefreshIsSkipped(t *testing.T) {
	h, now := attentionHarness(t)
	h.m.Update(tea.BlurMsg{})
	*now += 10
	h.m.lastRefresh = *now - 5
	h.m.Update(tea.FocusMsg{})
	if h.m.refreshing {
		t.Error("the screen is 5 s old: no catch-up refresh")
	}
}

func TestIdleAfterZeroNeverSlows(t *testing.T) {
	h, now := attentionHarness(t)
	h.m.cfg.Display.IdleAfterSecs = 0
	*now += 10000
	if a := h.m.attention(); a != attended {
		t.Errorf("idle_after_secs = 0: %v", a)
	}
}

// budget fakes GitHub's answer headers into a Usage.
func budgetUsage(t *testing.T, resource string, limit, remaining int, reset time.Time) *ghapi.Usage {
	t.Helper()
	u := ghapi.NewUsage()
	h := http.Header{}
	h.Set("X-RateLimit-Resource", resource)
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	u.ObserveBudget(h)
	return u
}

func TestGovernorKeepsPaceWithTheHour(t *testing.T) {
	h, _ := attentionHarness(t)
	now := clock.Now()
	cases := []struct {
		name      string
		remaining int
		reset     time.Duration
		factor    float64 // 0: just > 1
		pollsOff  bool
		floor     bool
	}{
		{"on pace", 2600, 30 * time.Minute, 1, false, false},
		{"ahead of the hour", 1000, 30 * time.Minute, 2.5, false, false},
		{"low", 500, 30 * time.Minute, 5, true, false},
		{"nearly out", 100, 30 * time.Minute, 8, true, true},
		{"low, but the hour is nearly over", 600, 3 * time.Minute, 1, true, false},
	}
	for _, c := range cases {
		h.m.usage = budgetUsage(t, "graphql", 5000, c.remaining, now.Add(c.reset))
		g := h.m.governor()
		if diff := g.factor - c.factor; diff > 0.05 || diff < -0.05 {
			t.Errorf("%s: factor %.2f, want %.2f", c.name, g.factor, c.factor)
		}
		if g.pollsOff != c.pollsOff || (g.refreshFloor > 0) != c.floor || g.resource != "gql" {
			t.Errorf("%s: %+v", c.name, g)
		}
	}
	h.m.cfg.GitHub.BudgetGovernor = false
	if g := h.m.governor(); g.factor != 1 || g.pollsOff {
		t.Errorf("budget_governor = false: %+v", g)
	}
}

func TestGovernorStretchesTheCadenceAndSaysSo(t *testing.T) {
	h, _ := attentionHarness(t)
	h.m.usage = budgetUsage(t, "core", 5000, 500, clock.Now().Add(30*time.Minute))
	if got := h.m.refreshEvery(); got < 1490 || got > 1510 {
		t.Errorf("refresh at 10%% left half an hour out: %v, want 1500", got)
	}
	if got := h.m.pollEvery(30 * time.Second); got != 0 {
		t.Errorf("polls pause below 15%%: %v", got)
	}
	if got := h.m.soonEvery(30 * time.Second); got < 300*time.Second {
		t.Errorf("a pull-in waits for the refresh's own interval at least: %v", got)
	}
	if note := h.m.governNote(); note == "" || !strings.Contains(note, "rest budget running low") || !strings.Contains(note, "live polls paused") {
		t.Errorf("note: %q", note)
	}
}
