package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
)

// TestFirstLoadShowsTheListFirst: the first refresh sends the list on
// its own before the whole result, and the whole result clears the
// list-only state; later refreshes send no list-only message.
func TestFirstLoadShowsTheListFirst(t *testing.T) {
	h := newHarness(t)
	var kinds []string
	h.m.sendFn = func(msg tea.Msg) {
		switch msg.(type) {
		case refreshListMsg:
			kinds = append(kinds, "list")
		case refreshDoneMsg:
			kinds = append(kinds, "done")
		}
		h.msgs <- msg
	}
	h.load()
	if strings.Join(kinds, ",") != "list,done" {
		t.Fatalf("first load sent %v", kinds)
	}
	if h.m.listOnly {
		t.Error("the whole refresh landed but the list-only state stayed")
	}
	if s := h.screen(); !strings.Contains(s, "widen the deploy role") {
		t.Errorf("list missing:\n%s", s)
	}
	kinds = nil
	h.key('r')
	if strings.Join(kinds, ",") != "done" {
		t.Errorf("a later refresh sent %v", kinds)
	}
}

// TestListOnlyHoldsTheKeysThatAct: while only the first load's list is
// on screen, a key that acts says the workflows are still loading and
// posts nothing; a key that looks still works.
func TestListOnlyHoldsTheKeysThatAct(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	if h.m.view != ViewDetail {
		t.Fatalf("view %s", h.m.view)
	}
	h.m.listOnly = true
	h.key('A')
	if !strings.Contains(h.m.status, "still reading the workflows") {
		t.Errorf("status %q", h.m.status)
	}
	if len(h.gh.Approvals) != 0 || len(h.gh.Calls) != 0 {
		t.Errorf("posted while list-only: %+v", h.gh.Calls)
	}
	h.named("esc")
	if h.m.view != ViewList {
		t.Errorf("Esc (a key that looks) did not go back: %s", h.m.view)
	}
}

// TestFirstLoadCIRateLimitStillLands: the first load's CI pass meets the
// rate limit after the list painted; the list and the workflows still
// land (the keys that act come back), the next refresh waits for the
// wall, and the warning says CI is what paused.
func TestFirstLoadCIRateLimitStillLands(t *testing.T) {
	h := newHarness(t)
	until := time.Now().Add(90 * time.Second)
	h.gh.Fail["workflow_runs"] = &ghapi.Error{Status: 403, Message: "API rate limit exceeded", RetryAt: until}
	h.load()
	if h.m.listOnly || h.m.errText != "" {
		t.Fatalf("listOnly=%v err=%q: the refresh failed whole", h.m.listOnly, h.m.errText)
	}
	if got := h.m.backoff.Until(); !got.Equal(until) {
		t.Errorf("backoff until = %v, want %v", got, until)
	}
	if !strings.Contains(h.m.warn, "CI paused") {
		t.Errorf("warn %q", h.m.warn)
	}
}

// TestListOnlyHeldAfterFailureSaysWhy: a first load that failed after
// its list painted keeps the keys that act held (a workflow may own
// them), and the notice names the failure and r, not "a moment".
func TestListOnlyHeldAfterFailureSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.listOnly = true
	h.m.applyRefresh(&refreshResult{err: "refresh failed: boom"})
	h.named("enter")
	h.key('A')
	if !strings.Contains(h.m.status, "didn't load (refresh failed: boom)") || !strings.Contains(h.m.status, "r tries again") {
		t.Errorf("status %q", h.m.status)
	}
	if len(h.gh.Approvals) != 0 {
		t.Error("posted while the workflows were missing")
	}
	h.key('r')
	if h.m.listOnly || h.m.listOnlyErr != "" {
		t.Errorf("a refresh that landed left the hold: %v %q", h.m.listOnly, h.m.listOnlyErr)
	}
}
