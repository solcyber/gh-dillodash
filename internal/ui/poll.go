package ui

import (
	"fmt"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// The poll is the light sibling of the refresh: while a workflow has
// something in flight (workflow.Poller), its Poll runs on a goroutine of
// its own every PollEvery with a few small calls, and the patch lands
// in ApplyPoll on the UI goroutine. Nothing else moves: the list keeps
// its rows, the detail its item; only what the poller owns changes.

// pollResult is what the poll goroutine sends back.
type pollResult struct {
	patches map[string]any
	err     string
	retryAt time.Time // set when err is a rate limit
	limited bool
}

type pollDoneMsg struct{ out *pollResult }

// schedulePoll arms the next poll from the workflows' cadence, or
// disarms it when nothing is in flight.
func (m *Model) schedulePoll() {
	d := m.wf.PollEvery(m)
	if d <= 0 {
		m.nextPollAt = 0
		return
	}
	m.nextPollAt = fx.Now() + d.Seconds()
}

func (m *Model) pollDue() bool { return m.nextPollAt > 0 && fx.Now() >= m.nextPollAt }

// poll starts one poll cycle. A refresh in flight re-derives everything
// a poll watches and re-arms the poll when it lands, so the cycle is
// skipped under one; a rate-limit wall pushes it past the wall.
func (m *Model) poll() {
	if m.polling || m.refreshing {
		return
	}
	if hold := m.backoff.RateLimitWait(clock.Now()); hold > 0 {
		m.nextPollAt = fx.Now() + hold.Seconds() + 1
		return
	}
	m.polling = true
	m.nextPollAt = 0 // re-armed by applyPoll
	gh, wf, ctx := m.gh, m.wf, m.ctx
	go func() {
		out := &pollResult{}
		defer func() {
			if r := recover(); r != nil {
				out.err = fmt.Sprintf("poll failed: %v", r)
			}
			m.send(pollDoneMsg{out})
		}()
		patches, err := wf.PollAll(ctx, gh)
		out.patches = patches
		if err != nil {
			out.err = err.Error()
			if e, ok := ghapi.AsError(err); ok && e.IsRateLimit() {
				out.limited, out.retryAt = true, e.RetryAt
			}
		}
	}()
}

// applyPoll lands a poll's patches and arms the next cycle. A rate
// limit raises the same wall the refresh honours and says so on the warn
// line; any other failure keeps the last known progress and retries at
// twice the cadence without touching the refresh's backoff.
func (m *Model) applyPoll(out *pollResult) {
	m.polling = false
	if len(out.patches) > 0 {
		m.wf.ApplyPolls(m, out.patches)
	}
	switch {
	case out.limited:
		m.backoff.RateLimit(out.retryAt)
		wait := m.backoff.RateLimitWait(clock.Now())
		note := "rate limited — live progress paused until " + clock.Now().Add(wait).Local().Format("15:04:05")
		if m.warn == "" {
			m.warn = note
		} else if len(m.warn) < 200 {
			m.warn = note + "; " + m.warn
		}
		m.nextPollAt = fx.Now() + wait.Seconds() + 1
	case out.err != "":
		if d := m.wf.PollEvery(m); d > 0 {
			m.nextPollAt = fx.Now() + 2*d.Seconds()
		} else {
			m.nextPollAt = 0
		}
	default:
		m.schedulePoll()
	}
}

// liveDetail reports whether the open detail's row changes between
// refreshes: its card is then rebuilt every frame.
func (m *Model) liveDetail() bool {
	if m.detail == nil {
		return false
	}
	var want model.Ident
	switch d := m.detail.(type) {
	case *model.PR:
		want = d.Ident()
	case *model.Issue:
		want = d.Ident()
	default:
		return false
	}
	for _, r := range m.rows {
		if r.Ident() == want {
			return m.wf.LiveRow(m, r)
		}
	}
	return false
}
