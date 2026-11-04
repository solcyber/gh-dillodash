package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The X key: run a workflow the config lists under [[workflows.dispatch]]
// (workflow_dispatch on a file, or a repository_dispatch event), with its
// inputs asked one by one and the exact request shown before it is sent.
// Nothing that is not listed can be dispatched from here.

// hasDispatches gates X in the bar.
func (m *Model) hasDispatches() bool { return len(m.cfg.Dispatches) > 0 }

// dispatchFollowFor is how long X follows the run it started; the poll
// runs every dispatchPollEvery.
var (
	dispatchFindFor   = 90 * time.Second
	dispatchFollowFor = 45 * time.Minute
	dispatchPollEvery = 10 * time.Second
)

// dispatchLabel is an entry's line in the menu.
func dispatchLabel(d config.DispatchConfig) string {
	if d.Kind() == "event" {
		return fmt.Sprintf("%s — event %s on %s", d.Name, d.Event, d.Repo)
	}
	return fmt.Sprintf("%s — %s@%s in %s", d.Name, d.Workflow, d.Ref, d.Repo)
}

// askInput reads one input: a menu for a choice or a boolean (the
// default first), a line otherwise; ok false on Esc.
func askInput(app *lockedApp, d config.DispatchConfig, in config.DispatchInput) (string, bool) {
	what := in.Name
	if in.Description != "" {
		what += " (" + in.Description + ")"
	}
	switch in.Type {
	case "choice", "boolean":
		opts := in.Options
		if in.Type == "boolean" {
			opts = []string{"true", "false"}
		}
		// the default first, so enter takes it
		ordered := make([]string, 0, len(opts))
		if in.Default != "" {
			ordered = append(ordered, in.Default)
		}
		for _, o := range opts {
			if o != in.Default {
				ordered = append(ordered, o)
			}
		}
		i := app.Pick(fmt.Sprintf("%s: %s", d.Name, what), ordered)
		if i < 0 || i >= len(ordered) {
			return "", false
		}
		return ordered[i], true
	}
	req := ""
	if in.Required {
		req = ", required"
	}
	return app.Prompt(fmt.Sprintf("%s: %s (%s%s; Esc cancels):", d.Name, what, in.Type, req), workflow.PromptOpts{Initial: in.Default})
}

// doDispatch is the X key.
func (m *Model) doDispatch(app *lockedApp) {
	list := m.cfg.Dispatches
	if len(list) == 0 {
		app.Notice("X runs the workflows the config lists under [[workflows.dispatch]]; none is listed")
		return
	}
	labels := make([]string, len(list))
	for i, d := range list {
		labels[i] = dispatchLabel(d)
	}
	i := app.Pick("run which workflow?", labels)
	if i < 0 || i >= len(list) {
		app.Notice("cancelled")
		return
	}
	d := list[i]
	typed := d.Kind() == "event" // an event payload keeps booleans and numbers typed
	values := map[string]any{}
	for _, in := range d.Inputs {
		s, ok := askInput(app, d, in)
		if !ok {
			app.Notice("cancelled")
			return
		}
		if strings.TrimSpace(s) == "" && !in.Required {
			continue // GitHub takes the workflow's own default
		}
		v, err := in.Value(s, typed)
		if err != nil {
			app.Fail("not dispatched: " + err.Error())
			return
		}
		values[in.Name] = v
	}
	var req string
	if d.Kind() == "event" {
		b, _ := json.Marshal(map[string]any{"event_type": d.Event, "client_payload": values})
		req = fmt.Sprintf("POST repos/%s/dispatches %s", d.Repo, b)
	} else {
		b, _ := json.Marshal(map[string]any{"ref": d.Ref, "inputs": values})
		req = fmt.Sprintf("POST repos/%s/actions/workflows/%s/dispatches %s", d.Repo, d.Workflow, b)
	}
	q := req
	if d.Confirm != "" {
		q += "\n" + d.Confirm
	}
	if !app.Confirm(fmt.Sprintf("%s\nsend that as %s?", q, m.me)) {
		app.Notice("cancelled")
		return
	}
	if !workflow.PostGuard(app, d.Name) {
		return
	}
	since := app.Now().Add(-5 * time.Second)
	var err error
	if d.Kind() == "event" {
		err = m.gh.Dispatch(m.ctx, d.Repo, d.Event, values)
	} else {
		err = m.gh.DispatchWorkflow(m.ctx, d.Repo, d.Workflow, d.Ref, values)
	}
	if err != nil {
		perm := "Actions: Read and write"
		if d.Kind() == "event" {
			perm = "Contents: Read and write"
		}
		hint := ""
		if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "not accessible") {
			hint = " — your token needs " + perm + " on " + d.Repo.Name
		}
		app.Fail(fmt.Sprintf("%s NOT dispatched: %s%s", d.Name, err, hint))
		return
	}
	if d.Kind() == "event" {
		app.OK(fmt.Sprintf("fired %s on %s (%s)", d.Event, d.Repo, d.Name))
		app.RefreshSoon(30 * time.Second)
		return
	}
	app.OK(fmt.Sprintf("dispatched %s on %s@%s — looking for its run...", d.Workflow, d.Repo, d.Ref))
	go m.followDispatch(app, d, since)
}

// followDispatch finds the run a dispatch started (the newest
// workflow_dispatch run of the file by the viewer since the dispatch)
// and says on the status line how it goes, until it completes.
func (m *Model) followDispatch(app *lockedApp, d config.DispatchConfig, since time.Time) {
	defer func() { _ = recover() }() // a broken follow never takes the dashboard down
	var run *model.WorkflowRun
	deadline := time.Now().Add(dispatchFindFor)
	last := ""
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(dispatchPollEvery / 2):
		}
		if run == nil {
			runs, err := m.gh.WorkflowFileRuns(m.ctx, d.Repo, d.Workflow, "workflow_dispatch", since)
			if err == nil {
				for i := range runs {
					r := runs[i]
					if strings.EqualFold(r.Actor.Login, m.me) && (run == nil || r.CreatedAt.After(run.CreatedAt)) {
						run = &r
					}
				}
			}
			if run == nil {
				if time.Now().After(deadline) {
					app.Notice(fmt.Sprintf("%s: no run showed within %s — check %s's Actions", d.Name, dispatchFindFor, d.Repo))
					m.redraw()
					return
				}
				continue
			}
			deadline = time.Now().Add(dispatchFollowFor)
		} else {
			runs, err := m.gh.WorkflowFileRuns(m.ctx, d.Repo, d.Workflow, "workflow_dispatch", since)
			if err == nil {
				for i := range runs {
					if runs[i].ID == run.ID {
						r := runs[i]
						run = &r
					}
				}
			}
		}
		state := runState(*run)
		if state != last {
			last = state
			msg := fmt.Sprintf("%s: run %d %s", d.Name, run.ID, state)
			switch {
			case run.Status == "completed" && run.Conclusion == "success":
				app.OK(msg)
			case run.Status == "completed":
				app.Fail(msg)
			default:
				app.Status(msg)
			}
			m.redraw()
		}
		if run.Status == "completed" || time.Now().After(deadline) {
			return
		}
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(dispatchPollEvery):
		}
	}
}
