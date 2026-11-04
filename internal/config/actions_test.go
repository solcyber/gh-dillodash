package config

import (
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/model"
)

const actionsBase = `
[reviews]
repos = ["acme/infra"]
`

func TestMergeMethods(t *testing.T) {
	c, err := Parse([]byte(actionsBase+`
[merge]
methods = { "acme/infra" = "squash", "acme/web-portal" = "rebase" }
`), "t", Overrides{})
	if err != nil || len(c.Warnings) != 0 {
		t.Fatalf("%v %v", err, c.Warnings)
	}
	infra, _ := model.ParseRepo("acme/infra")
	other, _ := model.ParseRepo("acme/etl-jobs")
	if c.Merge.Method(infra, "merge") != "squash" || c.Merge.Method(other, "merge") != "merge" {
		t.Errorf("methods: %v", c.Merge.Methods)
	}
	if _, err := Parse([]byte(actionsBase+"[merge]\nmethods = { \"acme/infra\" = \"fast-forward\" }\n"), "t", Overrides{}); err == nil || !strings.Contains(err.Error(), "not one of") {
		t.Errorf("a bad method is an error: %v", err)
	}
}

func TestReservedWindow(t *testing.T) {
	c, err := Parse([]byte(actionsBase+`
[workflows.maintenance_window]
enabled = true
timezone = "America/New_York"
cutoff = "18:00"
release_from = "21:59"
start = "22:00"
end = "23:00"
reserved_from = "21:50"
reserved_to = "23:15"
reserved_stacks = ["prod/*"]
`), "t", Overrides{})
	if err != nil || len(c.Warnings) != 0 {
		t.Fatalf("%v %v", err, c.Warnings)
	}
	r := c.MaintenanceWindow.Reserved
	if r == nil || r.From != (clock.HM{H: 21, M: 50}) || r.To != (clock.HM{H: 23, M: 15}) {
		t.Fatalf("reserved = %+v", r)
	}
	loc := c.MaintenanceWindow.Local()
	ny, _ := time.LoadLocation("America/New_York")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, ny) }
	for _, tc := range []struct {
		h, m  int
		stack string
		want  bool
	}{{22, 0, "prod/web", true}, {21, 49, "prod/web", false}, {23, 15, "prod/web", false}, {22, 0, "staging/web", false}} {
		if got := r.Covers(loc, at(tc.h, tc.m), tc.stack); got != tc.want {
			t.Errorf("%02d:%02d %s: %v", tc.h, tc.m, tc.stack, got)
		}
	}
	// wrapping midnight, every stack
	w := &ReservedWindow{From: clock.HM{H: 23}, To: clock.HM{H: 1}}
	if !w.Covers(loc, at(0, 30), "any") || w.Covers(loc, at(12, 0), "any") {
		t.Error("a span past midnight wraps")
	}
	if _, err := Parse([]byte(actionsBase+"[workflows.maintenance_window]\nreserved_from = \"21:50\"\n"), "t", Overrides{}); err == nil {
		t.Error("from without to is an error")
	}
}

func TestDispatchEntries(t *testing.T) {
	c, err := Parse([]byte(actionsBase+`
[[workflows.dispatch]]
name = "dry-run freeze"
repo = "acme/infra"
workflow = "window.yml"
inputs = [
  { name = "phase", type = "choice", options = ["freeze", "run"], default = "freeze" },
  { name = "dry_run", type = "boolean", default = true },
]

[[workflows.dispatch]]
name = "close a night"
repo = "acme/infra"
event = "window-close"
inputs = [{ name = "night", required = true }]
`), "t", Overrides{})
	if err != nil || len(c.Warnings) != 0 {
		t.Fatalf("%v %v", err, c.Warnings)
	}
	if len(c.Dispatches) != 2 {
		t.Fatalf("dispatches: %+v", c.Dispatches)
	}
	d := c.Dispatches[0]
	if d.Kind() != "workflow" || d.Ref != "main" || len(d.Inputs) != 2 || d.Inputs[1].Default != "true" || d.Inputs[1].Type != "boolean" {
		t.Errorf("first: %+v", d)
	}
	if e := c.Dispatches[1]; e.Kind() != "event" || e.Event != "window-close" || !e.Inputs[0].Required || e.Inputs[0].Type != "string" {
		t.Errorf("second: %+v", e)
	}
	if v, err := d.Inputs[1].Value("false", false); err != nil || v != "false" {
		t.Errorf("a workflow input is a string: %v %v", v, err)
	}
	if v, err := d.Inputs[1].Value("true", true); err != nil || v != true {
		t.Errorf("an event payload keeps the boolean: %v %v", v, err)
	}
	if _, err := d.Inputs[0].Value("all", false); err == nil {
		t.Error("a choice outside its options is refused")
	}
	for name, body := range map[string]string{
		"both":        "[[workflows.dispatch]]\nname = \"x\"\nrepo = \"acme/infra\"\nworkflow = \"a.yml\"\nevent = \"e\"\n",
		"neither":     "[[workflows.dispatch]]\nname = \"x\"\nrepo = \"acme/infra\"\n",
		"no repo":     "[[workflows.dispatch]]\nname = \"x\"\nworkflow = \"a.yml\"\n",
		"choice":      "[[workflows.dispatch]]\nname = \"x\"\nrepo = \"acme/infra\"\nworkflow = \"a.yml\"\ninputs = [{ name = \"p\", type = \"choice\" }]\n",
		"bad default": "[[workflows.dispatch]]\nname = \"x\"\nrepo = \"acme/infra\"\nworkflow = \"a.yml\"\ninputs = [{ name = \"p\", type = \"boolean\", default = \"maybe\" }]\n",
		"twice":       "[[workflows.dispatch]]\nname = \"x\"\nrepo = \"acme/infra\"\nworkflow = \"a.yml\"\n[[workflows.dispatch]]\nname = \"X\"\nrepo = \"acme/infra\"\nworkflow = \"b.yml\"\n",
	} {
		if _, err := Parse([]byte(actionsBase+body), "t", Overrides{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
