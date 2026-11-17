package terraformapprove

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// noWindowPR is a digest from a repo that applies on approval: its
// Window line says "n/a", or there is no Window line at all.
func noWindowPR(line string) *model.PR {
	body := digest("prod/00-baseline", idA, 0, 100, "")
	if line != "" {
		body += line + "\n"
	}
	return makePR([]model.Comment{comment(body)}, "sha1")
}

// TestApproveNoWindow: a repo without a maintenance window applies on
// approval. A digest with "Window: **n/a (apply on approval)**", or with
// no Window line, is a plain /approve even when this config also runs
// the window workflow for another repo: no queue question, no flag.
func TestApproveNoWindow(t *testing.T) {
	cfg := testCfg() // the window workflow is on
	for _, line := range []string{"Window: **n/a (apply on approval)**", "Window: **n/a**", ""} {
		pr := noWindowPR(line)
		app, handled := approve(t, cfg, pr, script{confirms: []bool{true}})
		check(t, "handled: "+line, handled)
		check(t, "no window question: "+line, len(app.Menus) == 0)
		check(t, "plain /approve: "+line, eq(posted(app), one(3622, "/approve "+idA)))
		for _, q := range app.Questions {
			check(t, "the confirm says nothing about queueing: "+line, !strings.Contains(q, "queues"))
		}
	}

	v, ok := PlanVerdicts(noWindowPR("Window: **n/a (apply on approval)**"), "")[idA]
	check(t, "the n/a verdict is read", ok && v.None && v.Verdict == "n/a (apply on approval)")
	check(t, "n/a is never disruptive", !IsDisruptive(v))
	_, ok = PlanVerdicts(noWindowPR(""), "")[idA]
	check(t, "no Window line: no verdict", !ok)
	// a parenthesised window line still wins over the n/a reading
	v = PlanVerdicts(windowed("disruptive · revertible", 0, false), "")[idA]
	check(t, "a live window verdict is unchanged", !v.None && IsDisruptive(v))
}

// TestApproveSaysWhenItApplies: a plan that doesn't queue says it
// applies at once, and what; a disruptive one with no window of
// dillodash's own to place it says the dispatcher may queue it, never
// "at once".
func TestApproveSaysWhenItApplies(t *testing.T) {
	cfg := testCfg()
	app, _ := approve(t, cfg, noWindowPR("Window: **n/a (apply on approval)**"), script{confirms: []bool{true}})
	check(t, "n/a: the confirm says it applies at once", len(app.Questions) == 1 && strings.Contains(app.Questions[0], "Applies at once: "))
	cfg.MaintenanceWindow = nil
	app, _ = approve(t, cfg, windowed("disruptive · revertible", 0, false), script{confirms: []bool{true}})
	for _, q := range app.Questions {
		check(t, "disruptive, no window config: never 'at once'", !strings.Contains(q, "Applies at once"))
	}
	check(t, "disruptive, no window config: the dispatcher may queue it", len(app.Questions) > 0 && strings.Contains(app.Questions[len(app.Questions)-1], "the dispatcher may queue it"))
}
