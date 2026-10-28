package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
)

// Snapshots drives the dashboard offline: it loads the data, then for each
// script of keys ("j", "enter", "esc", "G"...) presses them and paints the
// frame onto a canvas of w x h. The screenshot tool uses it with the demo
// fixture; nothing here touches the terminal.
func Snapshots(ctx context.Context, o Options, w, h int, scripts [][]string) ([]*canvas.Canvas, error) {
	m := New(ctx, o)
	msgs := make(chan tea.Msg, 256)
	m.sendFn = func(msg tea.Msg) { msgs <- msg }
	m.tp = &testPrompter{}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	pump := func() error {
		deadline := time.After(10 * time.Second)
		for {
			select {
			case msg := <-msgs:
				m.Update(msg)
			case <-deadline:
				return fmt.Errorf("the fixture never settled")
			case <-time.After(30 * time.Millisecond):
				m.mu.Lock()
				busy := m.refreshing || m.action
				m.mu.Unlock()
				if !busy {
					return nil
				}
			}
		}
	}
	m.Init()
	if err := pump(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.fx.Stop("splash")
	m.mu.Unlock()
	var out []*canvas.Canvas
	for _, script := range scripts {
		for _, k := range script {
			m.Update(keyMsg(k))
			if err := pump(); err != nil {
				return nil, err
			}
		}
		m.mu.Lock()
		c := canvas.New(w, h)
		m.draw(c)
		m.mu.Unlock()
		out = append(out, c)
	}
	return out, nil
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
