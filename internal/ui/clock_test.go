package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// withClock turns the header clock on (the harness has it off) at a fixed
// instant: 2026-11-03 04:12:33.45 UTC, 23:12:33.45 EST the evening before.
func withClock(t *testing.T, h *harness, ns int) {
	t.Helper()
	was := clock.Now
	t.Cleanup(func() { clock.Now = was })
	at := time.Date(2026, 11, 3, 4, 12, 33, ns, time.UTC)
	clock.Now = func() time.Time { return at }
	h.m.mu.Lock()
	h.m.clockLoc = config.Default().Display.ClockLocal()
	h.m.mu.Unlock()
}

func TestClockConfigDefaultsOn(t *testing.T) {
	d := config.Default().Display
	if !d.Clock || d.ClockTimezone != "America/New_York" || d.ClockTZLabel != "" {
		t.Fatalf("display = %+v", d)
	}
	cfg := config.Default()
	if m := New(t.Context(), Options{Config: cfg}); m.clockLoc == nil {
		t.Error("the clock is on by default")
	}
	cfg.Display.Clock = false
	if m := New(t.Context(), Options{Config: cfg}); m.clockLoc != nil {
		t.Error("clock = false hides it")
	}
}

func TestClockOnTheHeaderRow(t *testing.T) {
	h := newHarness(t)
	h.load()
	withClock(t, h, 450_000_000)
	was := fx.Motion
	t.Cleanup(func() { fx.Motion = was })

	row0 := func(w int) string {
		h.m.mu.Lock()
		h.m.w, h.m.h = w, 30
		h.m.fx.Stop("splash")
		h.m.mu.Unlock()
		return strings.SplitN(h.screen(), "\n", 2)[0]
	}
	fx.Motion = "full"
	if got := row0(160); !strings.Contains(got, "2026-11-03 04:12:33.45 UTC  23:12:33.45 EST") || !strings.Contains(got, "as alice") {
		t.Errorf("160 cols, motion on:\n%q", got)
	}
	// the login outranks the clock: at 70 the clock goes, "as alice" stays
	if got := row0(70); strings.Contains(got, "UTC") || !strings.Contains(got, "as alice") {
		t.Errorf("70 cols:\n%q", got)
	}
	fx.Motion = "off"
	if got := row0(160); !strings.Contains(got, "2026-11-03 04:12:33 UTC  23:12:33 EST") || strings.Contains(got, "33.45") {
		t.Errorf("160 cols, still:\n%q", got)
	}
	// narrow: the clock drops whole; the left side (the name, the repos)
	// is never clipped for it
	fx.Motion = "full"
	got := row0(60)
	if strings.Contains(got, "UTC") || strings.Contains(got, "EST") {
		t.Errorf("60 cols keeps a clock fragment:\n%q", got)
	}
	if !strings.Contains(got, "dillodash") || !strings.Contains(got, "acme/infra") {
		t.Errorf("60 cols clipped the left side:\n%q", got)
	}
}

func TestClockHoldsTheFrameRate(t *testing.T) {
	h := newHarness(t)
	h.load()
	withClock(t, h, 450_000_000)
	was, wasNow := fx.Motion, fx.Now
	t.Cleanup(func() { fx.Motion, fx.Now = was, wasNow })
	now := 1000.0
	fx.Now = func() float64 { return now }
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	h.m.fx.Stop("splash")
	h.m.ambient, h.m.animating, h.m.status = false, false, ""
	h.m.nextRefreshAt = now + 3600
	h.m.loaded = false

	fx.Motion = "full"
	if ms := h.m.tickMS(); ms != fx.FrameMS {
		t.Errorf("idle with the clock: %d, want %d", ms, fx.FrameMS)
	}
	h.m.modal = widgets.NewPick("pick", []string{"a", "b"}, "") // ticks at AmbientMS on its own
	if ms := h.m.tickMS(); ms != fx.FrameMS {
		t.Errorf("under a modal: %d, want %d", ms, fx.FrameMS)
	}
	h.m.modal = nil

	// still: no hundredths, so wake on the next whole wall second (.45 →
	// 550 ms), not every frame and not a minute away
	fx.Motion = "off"
	if ms := h.m.tickMS(); ms < 545 || ms > 565 {
		t.Errorf("still clock: %d, want ~555", ms)
	}
	h.m.clockLoc = nil
	if ms := h.m.tickMS(); ms != idleMaxMS {
		t.Errorf("no clock: %d, want %d", ms, idleMaxMS)
	}
}

// a wall clock a hair before the second must not skip it and sleep on
func TestStillClockWakesAtTheBoundary(t *testing.T) {
	h := newHarness(t)
	h.load()
	withClock(t, h, 999_999_000)
	wasNow := fx.Now
	t.Cleanup(func() { fx.Now = wasNow })
	now := 1000.0
	fx.Now = func() float64 { return now }
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	h.m.fx.Stop("splash")
	h.m.ambient, h.m.animating, h.m.status = false, false, ""
	h.m.nextRefreshAt = now + 3600
	h.m.loaded = false
	if ms := h.m.tickMS(); ms > 1010 {
		t.Errorf("boundary: %d", ms)
	}
}
