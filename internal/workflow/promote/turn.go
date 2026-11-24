package promote

import (
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/keymap"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// waitingRunsName is the waiting runs workflow's state name: it read the
// environments a change's waiting run waits on (workflow.RunWaits).
const waitingRunsName = "waiting_runs"

// waitOf is the environments of the change's waiting run that are the
// viewer's to approve now; known is false when nobody read the run (its
// repo is not one the waiting runs workflow reads).
func waitOf(app workflow.App, c *Change) (mine []string, known bool) {
	rw, ok := app.State(waitingRunsName).(workflow.RunWaits)
	if !ok || rw == nil {
		return nil, false
	}
	for _, r := range c.Runs {
		if r.Status != "waiting" {
			continue
		}
		m, k := rw.RunWait(c.Repo, r.ID)
		if !k {
			return nil, false
		}
		mine = append(mine, m...)
		known = true
	}
	return mine, known
}

// turn is a change's move (workflow.Turn). The viewer's turn: a tier
// deploy that waits on them (or one nobody read, which D reads), a
// promotion of their own that is ready to merge, and a staging unit to
// tag by hand where nothing tags. Everything else waits on its author,
// automation or the next release cut.
func (w *Workflow) turn(app workflow.App, c *Change) workflow.Turn {
	if c == nil {
		return workflow.Turn{}
	}
	me := app.Me()
	mine := me != "" && strings.EqualFold(c.Author(), me)
	tier := w.tierName(c)
	d, name := deployOf(c.Runs)
	switch d {
	case DeployWaiting:
		envs, known := waitOf(app, c)
		switch {
		case !known:
			// nobody read whose it is (waiting runs off, or the repo not
			// read): not pinned, but D reads and approves it
			return workflow.Turn{Kind: workflow.TurnOthers, What: "its " + tier + " deploy's reviewers (D reads them)", Key: keymap.S(keymap.Deploys), Verb: "read its " + tier + " deploy"}
		case len(envs) > 0:
			return workflow.You(keymap.S(keymap.Deploys), "approve "+strings.Join(envs, ", "), "deploy", workflow.PrioDeploy)
		}
		return workflow.Others("the reviewers of its " + tier + " deploy")
	case DeployRunning:
		return workflow.Running(tier+" deploying", time.Time{})
	case DeployFailed:
		return workflow.Turn{Kind: workflow.TurnFailed, What: tier + " deploy failed (" + name + ")"}
	}
	switch c.Where {
	case Promoting:
		if c.Promo != nil {
			switch strings.ToUpper(c.Promo.MergeStateStatus) {
			case "CLEAN", "HAS_HOOKS":
				if mine {
					return workflow.You(keymap.S(keymap.Promote), "merge the promotion", "promotion", workflow.PrioPromotion)
				}
			}
		}
		return workflow.Others(authorOr(c.Author(), "its author"))
	case OnStage:
		switch c.Tag {
		case "go", "hold":
			return workflow.Others("the next release cut")
		}
		if w.cfg.TagByHand {
			return workflow.You(keymap.S(keymap.Promote), "tag it go or hold", "promotion", workflow.PrioPromotion)
		}
		return workflow.Others("automation (tagging)")
	case OnFirst:
		if mine {
			return workflow.You(keymap.S(keymap.Promote), "open the promotion PR", "promotion", workflow.PrioPromotion)
		}
		return workflow.Others(authorOr(c.Author(), "its author") + " to promote it")
	}
	return workflow.Turn{Kind: workflow.TurnDone, What: "on " + w.cfg.Prod()}
}

func authorOr(a, def string) string {
	if a == "" {
		return def
	}
	return a
}
