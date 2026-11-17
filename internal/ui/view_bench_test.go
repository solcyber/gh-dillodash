package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/demo"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// BenchmarkView is one frame of the demo dashboard in true color: the
// list, the detail and the help, at a laptop and a wide terminal. A held
// j repaints on every key, so a frame must stay well inside 33ms.
func BenchmarkView(b *testing.B) {
	fx.Motion = "full"
	glyphs.Use(glyphs.Unicode)
	theme.Setup(colorprofile.TrueColor, false)
	cfg := demo.Config()
	wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []struct {
		name string
		w, h int
	}{{"120x40", 120, 40}, {"240x70", 240, 70}} {
		for _, view := range []struct {
			name string
			keys []string
		}{{"list", nil}, {"detail", []string{"enter"}}, {"help", []string{"?"}}} {
			b.Run(size.name+"/"+view.name, func(b *testing.B) {
				m := New(context.Background(), Options{Config: cfg, GH: demo.Client(), Workflows: wf, Me: demo.Me, Host: "github.com", Version: "bench"})
				msgs := make(chan tea.Msg, 256)
				m.sendFn = func(msg tea.Msg) { msgs <- msg }
				m.tp = &testPrompter{OpenURL: func(string) bool { return true }, Copy: func(string) string { return "" }}
				m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
				m.Init()
				if err := settle(m, msgs, 10e9); err != nil {
					b.Fatal(err)
				}
				m.mu.Lock()
				m.fx.Stop("splash")
				m.fx.Stop("cascade")
				m.mu.Unlock()
				for _, k := range view.keys {
					m.Update(keyMsg(k))
					if err := settle(m, msgs, 10e9); err != nil {
						b.Fatal(err)
					}
				}
				m.View()
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					m.View()
				}
			})
		}
	}
}
