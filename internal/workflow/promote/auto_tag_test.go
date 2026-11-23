package promote

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

// releases is a release workflow state listing open release PRs.
type releases []*model.PR

func (r releases) CIPRs() []*model.PR { return r }

var _ workflow.CIProvider = releases(nil)

func stageUnit(tag string, conclusion string) *Change {
	unit := merged(301, "stage", "promote/202-stage", "Promote #202", "s301", 2)
	c := &Change{Repo: web, Promo: unit, Where: OnStage, Tag: tag}
	switch conclusion {
	case "":
	case "running":
		c.Runs = []model.WorkflowRun{{Name: "deploy", Status: "in_progress"}}
	default:
		c.Runs = []model.WorkflowRun{{Name: "deploy", Status: "completed", Conclusion: conclusion}}
	}
	return c
}

// TestAutoTagUntaggedUnitIsNotTagged: with tagging automatic (the
// default), P on an untagged unit writes nothing and says how the unit
// gets its tag, by what its deploy did.
func TestAutoTagUntaggedUnitIsNotTagged(t *testing.T) {
	for conclusion, want := range map[string]string{
		"running": "it is tagged once its deploy succeeds",
		"success": "its deploy passed; the tag follows within minutes",
		"failure": "its deploy failed (deploy), so the cut stops here; fix it on dev, or label its PR hold",
	} {
		w, app := newWF(t)
		selectChange(app, stageUnit("", conclusion))
		w.doPromote(app)
		if len(app.Fake.Calls) != 0 || len(app.Menus) != 0 {
			t.Errorf("%s: nothing offered or written: %+v %v", conclusion, app.Fake.Calls, app.Menus)
		}
		if !strings.Contains(app.Last(), "#301 on stage is untagged — tagging is automatic") || !strings.Contains(app.Last(), want) {
			t.Errorf("%s: %q, want %q", conclusion, app.Last(), want)
		}
	}
}

// TestAutoTagChips: an untagged unit waits in a neutral chip (or says
// its deploy failed); the legend and the key bar say tagging is
// automatic.
func TestAutoTagChips(t *testing.T) {
	w, _ := newWF(t)
	for conclusion, want := range map[string][2]string{
		"running": {"stage · awaiting deploy", "chip.state"},
		"":        {"stage · awaiting deploy", "chip.state"},
		"success": {"stage · awaiting tag", "chip.state"},
		"failure": {"stage · deploy failed", "chip.fail"},
	} {
		got, style := w.whereChip(stageUnit("", conclusion))
		if got != want[0] || style != want[1] {
			t.Errorf("%q: chip %q %s, want %q %s", conclusion, got, style, want[0], want[1])
		}
	}
	if got, style := w.whereChip(stageUnit("go", "success")); got != "stage · go" || style != "chip.approved" {
		t.Errorf("a go unit: %q %s", got, style)
	}
	var legend string
	for _, row := range w.LegendRows() {
		legend += text.SegsText(row)
	}
	if !strings.Contains(legend, "stage · awaiting deploy") || !strings.Contains(legend, "tagged go when its deploy succeeds") || strings.Contains(legend, "P tags it") {
		t.Errorf("legend: %q", legend)
	}
	if h := w.HelpRows()[0][1]; !strings.Contains(h, "stage: flip go and hold; tagging is automatic") {
		t.Errorf("help: %q", h)
	}
	for tag, want := range map[string]string{"go": "flip to hold", "hold": "flip to go", "": "why untagged"} {
		f := workflow.Focus{HasRow: true, Row: workflow.Row{Kind: RowChange, Item: stageUnit(tag, "success")}}
		if got, ok := w.offerPromote(f); !ok || got != want {
			t.Errorf("P on a %q unit offers %q, want %q", tag, got, want)
		}
	}
	w.cfg.TagByHand = true
	if got, style := w.whereChip(stageUnit("", "running")); got != "stage · untagged" || style != "chip.await" {
		t.Errorf("by hand: the needs-action chip: %q %s", got, style)
	}
}

// TestAutoTagFlipOnly: on a tagged unit P offers the flip alone and says
// tagging is automatic; the flip deletes the old tag, then tags.
func TestAutoTagFlipOnly(t *testing.T) {
	w, app := newWF(t)
	selectChange(app, stageUnit("hold", "success"))
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{"go"}, []bool{true}
	w.doPromote(app)
	if len(app.Menus) != 1 || len(app.Menus[0]) != 2 || !strings.HasPrefix(app.Menus[0][1], "go/301") {
		t.Fatalf("one flip offered: %q", app.Menus)
	}
	if !strings.Contains(app.Menus[0][0], "tagging is automatic") || !strings.Contains(app.Menus[0][0], "P only flips it") {
		t.Errorf("the title says why: %q", app.Menus[0][0])
	}
	if c := app.Fake.Calls; len(c) != 2 || c[0].Op != "delete_ref" || c[0].Ref != "tags/hold/301" || c[1].Ref != "go/301" {
		t.Fatalf("flip calls %+v; log %v", c, app.Log)
	}
}

// TestAutoTagExistingTagIsNotAnError: a 422 on the tag (the automation
// tagged the unit meanwhile) re-reads the unit's tags and says what it
// carries now, instead of failing.
func TestAutoTagExistingTagIsNotAnError(t *testing.T) {
	w, app := newWF(t)
	selectChange(app, stageUnit("go", "success"))
	app.Fake.Tags = map[model.Repo][]string{web: {"hold/301", "go/300"}}
	app.Fake.Fail = map[string]error{"create_tag": &ghapi.Error{Status: 422, Message: "Reference already exists"}}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{"hold"}, []bool{true}
	w.doPromote(app)
	if !logHas(app, "notice", "hold/301 already exists — #301 is tagged hold/301 now; nothing changed") {
		t.Errorf("says the tag it has: %v", app.Log)
	}
	for _, l := range app.Log {
		if strings.HasPrefix(l, "fail") {
			t.Errorf("not an error: %q", l)
		}
	}
	if app.Fake.Reads["tags"] != 2 {
		t.Errorf("re-read both prefixes: %d reads", app.Fake.Reads["tags"])
	}
	// any other failure still fails
	w, app = newWF(t)
	selectChange(app, stageUnit("go", "success"))
	app.Fake.Fail = map[string]error{"create_tag": &ghapi.Error{Status: 403, Message: "Forbidden"}}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{"hold"}, []bool{true}
	w.doPromote(app)
	if !logHas(app, "fail", "tagging hold/301 failed") {
		t.Errorf("a 403 fails: %v", app.Log)
	}
}

// TestAutoTagReleaseNote: a go unit's last hop names the auto-cut, or
// the next cut when the open release PR is approved (an approved release
// is not re-cut).
func TestAutoTagReleaseNote(t *testing.T) {
	w, app := newWF(t)
	c := stageUnit("go", "success")
	if got := w.releaseNote(app, c); got != "the auto-cut follows: it rides the next release into main" {
		t.Errorf("no release PR: %q", got)
	}
	rel := &model.PR{Repo: web, Number: 50, BaseRefName: "main", State: "OPEN"}
	app.States[releaseName] = releases{rel}
	if got := w.releaseNote(app, c); got != "release PR #50 into main is open; the auto-cut follows (A approves it there)" {
		t.Errorf("open: %q", got)
	}
	rel.ReviewDecision = "APPROVED"
	if got := w.releaseNote(app, c); got != "release PR #50 into main is approved, so it waits for the next cut" {
		t.Errorf("approved: %q", got)
	}
	w.cfg.TagByHand = true
	if got := w.releaseNote(app, c); got != "release PR #50 into main is open (A approves it there)" {
		t.Errorf("by hand: %q", got)
	}
}

func logHas(app *wftest.App, kind, sub string) bool {
	for _, l := range app.Log {
		if strings.HasPrefix(l, kind+": ") && strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
