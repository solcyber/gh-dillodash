package workflow_test

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

// TestStaleGateOffersTheRestartFirst: with a newer release installed in
// the background, a release action offers the restart before anything
// else. Yes restarts (once the action settles) and stops; no goes on
// with this build; neither asks the stale question too.
func TestStaleGateOffersTheRestartFirst(t *testing.T) {
	app := wftest.New(config.Default(), "alice")
	app.Installed = "v9.9.9"
	app.Stale = map[string]string{"approve": "dillodash v1.0.0 is installed and this session still runs v0.9.0"}
	app.Confirms = []bool{true}
	if workflow.StaleGate(app, "approve") {
		t.Error("yes to the restart must stop the action")
	}
	if app.Restarts != 1 || len(app.Questions) != 1 || !strings.Contains(app.Questions[0], "restart into v9.9.9 first?") {
		t.Errorf("restarts %d, questions %q", app.Restarts, app.Questions)
	}
	if !strings.Contains(lastLog(app), "restarting into v9.9.9") {
		t.Errorf("notice: %q", app.Log)
	}

	app = wftest.New(config.Default(), "alice")
	app.Installed = "v9.9.9"
	app.Confirms = []bool{false}
	if !workflow.StaleGate(app, "merge") || app.Restarts != 0 || len(app.Questions) != 1 {
		t.Errorf("no goes on with this build: restarts %d, questions %q", app.Restarts, app.Questions)
	}

	// nothing installed: the stale question as before
	app = wftest.New(config.Default(), "alice")
	app.Stale = map[string]string{"deploy": "you run dillodash v1.0.0 and v1.1.0 is out"}
	if workflow.StaleGate(app, "deploy") || !strings.Contains(app.Questions[0], "go on with this build anyway?") {
		t.Errorf("stale, nothing installed: %q", app.Questions)
	}
	app = wftest.New(config.Default(), "alice")
	if !workflow.StaleGate(app, "window") || len(app.Questions) != 0 {
		t.Errorf("current build asks nothing: %q", app.Questions)
	}
}
