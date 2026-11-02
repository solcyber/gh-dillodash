package ui

import (
	"context"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/selfupdate"
)

// Updater is the build check behind the stale banner and the release
// actions' gate.
type Updater interface {
	Check(ctx context.Context, force bool) selfupdate.Status
}

// buildCheckMsg carries a finished build check back to the UI loop.
type buildCheckMsg struct{ st selfupdate.Status }

// checkBuild starts a build check in the background (lock held). It is
// quiet: an offline check leaves the last answer, and nothing waits on
// it.
func (m *Model) checkBuild(force bool) {
	if m.updater == nil || m.checking {
		return
	}
	m.checking = true
	ctx, u, last := m.ctx, m.updater, m.upd
	go func() {
		st := last
		defer func() {
			_ = recover() // a broken check never takes the dashboard down
			m.send(buildCheckMsg{st})
		}()
		st = u.Check(ctx, force)
	}()
}

// joinNonEmpty joins the non-empty parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
