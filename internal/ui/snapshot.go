package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
)

// SnapshotOptions tune a snapshot's clock and effects: the screenshot
// gallery draws the splash, the launch and Amos's walk mid-motion.
type SnapshotOptions struct {
	Motion     bool      // motion "full" with the frame clock pinned at Clock
	Clock      float64   // the pinned frame clock (seconds) during the script
	Advance    float64   // seconds added to the clock before the frame is drawn
	KeepSplash bool      // leave the splash playing (it is stopped otherwise)
	Fx         []FxStart // effects started before the frame, already Elapsed seconds in
}

// FxStart is one effect to start for a snapshot.
type FxStart struct {
	Name    string
	Secs    float64
	Elapsed float64
}

// Snapshots drives the dashboard offline: it loads the data, then for each
// script of keys ("j", "enter", "esc", "G"...) presses them and paints the
// frame onto a canvas of w x h. The screenshot tool uses it with the demo
// fixture; nothing here touches the terminal.
func Snapshots(ctx context.Context, o Options, w, h int, scripts [][]string) ([]*canvas.Canvas, error) {
	return SnapshotsWith(ctx, o, SnapshotOptions{}, w, h, scripts)
}

// SnapshotsWith is Snapshots with the clock and effects set.
func SnapshotsWith(ctx context.Context, o Options, so SnapshotOptions, w, h int, scripts [][]string) ([]*canvas.Canvas, error) {
	if so.Motion {
		wasMotion, wasNow := fx.Motion, fx.Now
		defer func() { fx.Motion, fx.Now = wasMotion, wasNow }()
		fx.Motion = "full"
		fx.Now = func() float64 { return so.Clock }
	}
	m := New(ctx, o)
	msgs := make(chan tea.Msg, 256)
	m.sendFn = func(msg tea.Msg) { msgs <- msg }
	m.tp = &testPrompter{
		OpenURL: func(string) bool { return true },
		Copy:    func(string) string { return "test clipboard" },
	}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	pump := func() error {
		if err := settle(m, msgs, 10*time.Second); err != nil {
			return fmt.Errorf("the fixture never settled: %w", err)
		}
		return nil
	}
	m.Init()
	if err := pump(); err != nil {
		return nil, err
	}
	if !so.KeepSplash {
		m.mu.Lock()
		m.fx.Stop("splash")
		m.mu.Unlock()
	}
	// a workflow with something in flight gets its first poll now, as it
	// would thirty seconds in
	m.mu.Lock()
	m.poll()
	m.mu.Unlock()
	if err := pump(); err != nil {
		return nil, err
	}
	var out []*canvas.Canvas
	for _, script := range scripts {
		if so.Motion {
			fx.Now = func() float64 { return so.Clock } // each script's keys at the clock, not the last frame's advance
		}
		for _, k := range script {
			m.Update(keyMsg(k))
			if err := pump(); err != nil {
				return nil, err
			}
		}
		m.mu.Lock()
		if so.Motion {
			for _, e := range so.Fx {
				was := fx.Now
				at := so.Clock + so.Advance - e.Elapsed
				fx.Now = func() float64 { return at }
				m.fx.Start(e.Name, e.Secs, nil)
				fx.Now = was
			}
			if so.Advance != 0 {
				at := so.Clock + so.Advance
				fx.Now = func() float64 { return at }
			}
		}
		c := canvas.New(w, h)
		m.draw(c)
		m.mu.Unlock()
		out = append(out, c)
	}
	return out, nil
}

// errNeverSettled is settle's error when the deadline passes.
var errNeverSettled = errors.New("goroutines never settled")

// settle applies the goroutines' messages until the model is idle: no
// refresh and no action in flight, or a modal is up waiting for an
// answer. It is deterministic rather than "quiet for a while": the
// refreshing and action flags are set under the lock before their
// goroutines start and cleared only by the messages those goroutines
// send last, so an empty queue while a flag is set means a message is
// still coming, and the loop blocks for it (up to deadline).
func settle(m *Model, msgs <-chan tea.Msg, deadline time.Duration) error {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		drain(m, msgs)
		m.mu.Lock()
		busy := m.refreshing || m.action || m.polling
		modal := m.modal != nil
		m.mu.Unlock()
		if modal || !busy {
			return nil
		}
		select {
		case msg := <-msgs:
			m.Update(msg)
		case <-timer.C:
			return errNeverSettled
		}
	}
}

// drain applies every message already queued, without waiting.
func drain(m *Model, msgs <-chan tea.Msg) {
	for {
		select {
		case msg := <-msgs:
			m.Update(msg)
		default:
			return
		}
	}
}

// keyMsg turns a script key into a key press.
func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}
