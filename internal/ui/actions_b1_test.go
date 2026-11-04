package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// TestMergeMethodPerRepo: [merge] methods picks M's method per repo.
func TestMergeMethodPerRepo(t *testing.T) {
	h := newHarness(t)
	h.m.cfg.Merge.Methods = map[model.Repo]string{infra: "squash"}
	id := model.Ident{Repo: infra, Number: 101}
	h.gh.Mutate(func() {
		h.gh.PRs[id].ReviewDecision = "APPROVED"
		h.gh.PRs[id].MergeStateStatus = "CLEAN"
	})
	h.load()
	h.named("enter")
	var asked string
	h.m.tp.Confirm = func(q string) bool { asked = q; return true }
	h.key('M')
	if got := h.gh.Merges; len(got) != 1 || got[0].Method != "squash" {
		t.Fatalf("one squash merge: %+v (status %q)", got, h.m.status)
	}
	if !strings.Contains(asked, "with a squash") {
		t.Errorf("the confirm names the method: %q", asked)
	}
}

// TestRunsKey: x on an open PR lists the runs on its head and acts on
// the chosen one: re-run the failed jobs, re-run all, cancel, reject a
// waiting deployment (its own confirm), each after a confirm.
func TestRunsKey(t *testing.T) {
	h := newHarness(t)
	head := strings.Repeat("a", 40)
	failed := model.WorkflowRun{ID: 11, Name: "ci", Status: "completed", Conclusion: "failure", HeadSHA: head, CreatedAt: t0}
	running := model.WorkflowRun{ID: 12, Name: "deploy", Status: "in_progress", HeadSHA: head, CreatedAt: t0.Add(time.Minute)}
	waiting := model.WorkflowRun{ID: 13, Name: "release", Status: "waiting", HeadSHA: head, CreatedAt: t0.Add(2 * time.Minute)}
	var prod model.PendingDeployment
	prod.Environment.ID, prod.Environment.Name, prod.CurrentUserCanApprove = 4, "prod", true
	h.gh.Runs = map[string][]model.WorkflowRun{head: {failed, running, waiting}}
	h.gh.Pending = map[int64][]model.PendingDeployment{13: {prod}}
	h.load()
	h.named("enter")

	pickRun := func(id int64, action string) {
		h.m.tp.Pick = func(title string, labels []string) int {
			for i, l := range labels {
				if strings.Contains(l, "run "+itoa(id)+" ") || l == action {
					return i
				}
			}
			t.Fatalf("%q not in %q", action, labels)
			return -1
		}
	}
	var asked []string
	h.m.tp.Confirm = func(q string) bool { asked = append(asked, q); return true }

	pickRun(11, runRerunFailed)
	h.key('x')
	pickRun(11, runRerunAll)
	h.key('x')
	pickRun(12, runCancel)
	h.key('x')
	got := h.gh.RunActions
	if len(got) != 3 || got[0].Op != "rerun_failed" || got[0].RunID != 11 || got[1].Op != "rerun" || got[2].Op != "cancel_run" || got[2].RunID != 12 {
		t.Fatalf("run actions: %+v (status %q)", got, h.m.status)
	}
	if len(asked) != 3 || !strings.HasPrefix(asked[2], "cancel the run: run 12 (deploy) in acme/infra as alice?") {
		t.Errorf("each asks first: %q", asked)
	}

	// reject: its own prompt and confirm; never an approval
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "wrong cut", true }
	pickRun(13, runReject)
	h.key('x')
	if r := h.gh.CallsFor("reject_deployments"); len(r) != 1 || r[0].RunID != 13 || r[0].EnvIDs[0] != 4 || r[0].Body != "wrong cut" {
		t.Errorf("rejected prod of run 13: %+v (status %q)", r, h.m.status)
	}
	if len(h.gh.DeployApprovals) != 0 {
		t.Error("x never approves")
	}
	if !strings.Contains(asked[len(asked)-1], "reject prod of run 13") {
		t.Errorf("the reject confirm: %q", asked[len(asked)-1])
	}

	// a cancelled confirm sends nothing
	h.m.tp.Confirm = func(string) bool { return false }
	pickRun(12, runCancel)
	h.key('x')
	if len(h.gh.RunActions) != 3 {
		t.Errorf("a no sends nothing: %+v", h.gh.RunActions)
	}
}

// TestRunsKeyLogs: x's log entry fetches the failed job's log and hands
// it to $PAGER.
func TestRunsKeyLogs(t *testing.T) {
	h := newHarness(t)
	head := strings.Repeat("a", 40)
	h.gh.Runs = map[string][]model.WorkflowRun{head: {{ID: 21, Name: "ci", Status: "completed", Conclusion: "failure", HeadSHA: head, CreatedAt: t0}}}
	h.gh.Jobs = map[int64][]model.WorkflowJob{21: {{ID: 1, Name: "lint", Conclusion: "success", Status: "completed"}, {ID: 2, Name: "test", Conclusion: "failure", Status: "completed"}}}
	h.gh.JobLog = map[int64]string{2: "FAIL: TestSomething\n"}
	h.load()
	h.named("enter")
	h.m.tp.Pick = func(_ string, labels []string) int {
		for i, l := range labels {
			if l == runLogs {
				return i
			}
		}
		return 0
	}
	var paged string
	h.m.tp.RunExternal = func(argv []string) error {
		b, err := os.ReadFile(argv[len(argv)-1])
		paged = string(b)
		return err
	}
	h.key('x')
	if paged != "FAIL: TestSomething\n" {
		t.Errorf("the failed job's log went to the pager: %q (status %q)", paged, h.m.status)
	}
}

// TestDispatchKey: X offers only the listed entries, asks each input
// (the default first), shows the exact request, and sends it; a
// repository_dispatch keeps booleans typed. Unlisted is impossible.
func TestDispatchKey(t *testing.T) {
	h := newHarness(t)
	h.load()
	if strings.Contains(h.screen(), " X ") {
		t.Error("X is out of the bar while nothing is listed")
	}
	h.m.tp.Pick = func(string, []string) int { t.Fatal("a menu with nothing listed"); return -1 }
	h.key('X')
	if !strings.Contains(h.m.status, "none is listed") || len(h.gh.Dispatches) != 0 {
		t.Fatalf("nothing listed: %q", h.m.status)
	}

	h.m.cfg.Dispatches = []config.DispatchConfig{
		{Name: "dry-run freeze", Repo: infra, Workflow: "window.yml", Ref: "main", Confirm: "a dry run posts nothing",
			Inputs: []config.DispatchInput{{Name: "phase", Type: "choice", Options: []string{"freeze", "run"}, Default: "run"}, {Name: "dry_run", Type: "boolean", Default: "true"}}},
		{Name: "close a night", Repo: infra, Event: "window-close", Inputs: []config.DispatchInput{{Name: "night", Type: "string", Required: true}, {Name: "force", Type: "boolean", Default: "false"}}},
	}
	dispatchFindFor = 50 * time.Millisecond
	dispatchPollEvery = 10 * time.Millisecond
	var menus [][]string
	picks := []int{0, 0, 0} // the first entry; the default of each menu input
	h.m.tp.Pick = func(_ string, labels []string) int {
		menus = append(menus, labels)
		v := picks[0]
		picks = picks[1:]
		return v
	}
	var asked []string
	h.m.tp.Confirm = func(q string) bool { asked = append(asked, q); return true }
	h.key('X')
	if len(menus) != 3 || len(menus[0]) != 2 || menus[1][0] != "run" || menus[2][0] != "true" {
		t.Fatalf("menus: %q", menus)
	}
	want := `POST repos/acme/infra/actions/workflows/window.yml/dispatches {"inputs":{"dry_run":"true","phase":"run"},"ref":"main"}`
	if len(asked) != 1 || !strings.HasPrefix(asked[0], want) || !strings.Contains(asked[0], "a dry run posts nothing") {
		t.Errorf("the exact request: %q", asked)
	}
	d := h.gh.Dispatches
	if len(d) != 1 || d[0].Op != "dispatch_workflow" || d[0].File != "window.yml" || d[0].Ref != "main" || d[0].Payload["phase"] != "run" {
		t.Fatalf("dispatched: %+v (status %q)", d, h.m.status)
	}

	time.Sleep(150 * time.Millisecond) // the follower gives up on a run that never shows
	h.pump()

	// the event: a typed payload
	picks = []int{1, 1} // the second entry; force = the second choice (true)
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "2026-10-28", true }
	h.key('X')
	d = h.gh.Dispatches
	if len(d) != 2 || d[1].Op != "dispatch" || d[1].Event != "window-close" || d[1].Payload["night"] != "2026-10-28" || d[1].Payload["force"] != true {
		t.Fatalf("event: %+v (status %q)", d, h.m.status)
	}

	// a required input left empty sends nothing
	picks = []int{1}
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "", true }
	h.key('X')
	if len(h.gh.Dispatches) != 2 || !strings.Contains(h.m.errText, "required") {
		t.Errorf("required: %+v %q", h.gh.Dispatches, h.m.errText)
	}
	time.Sleep(100 * time.Millisecond) // let the follower give up
}

// TestConfigStaleAndPull: a pulled config whose source has a newer file
// says so on the status line; P installs it (validated, stamped) and
// queues a restart into it.
func TestConfigStaleAndPull(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	src, _ := config.ParseSpec("acme/dashboard-config")
	src.SHA, src.At = "0000000000000000000000000000000000000000", t0
	body := []byte("[reviews]\nrepos = [\"acme/infra\"]\n")
	if err := os.WriteFile(path, config.Stamp(src, body), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := []byte("[reviews]\nrepos = [\"acme/infra\", \"acme/web-portal\"]\n")
	h.gh.FileData = map[string][]byte{"acme/dashboard-config:config.toml": newer}
	h.m.cfg.Path = path
	h.load()
	h.m.mu.Lock()
	h.m.checkConfig()
	h.m.mu.Unlock()
	for i := 0; i < 200; i++ {
		h.pump()
		h.m.mu.Lock()
		busy := h.m.cfgChecking
		h.m.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !h.m.configStale() || !strings.Contains(h.m.configBanner(), "acme/dashboard-config has a newer config.toml") {
		t.Fatalf("stale: %v %q", h.m.configStale(), h.m.configBanner())
	}
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('P')
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "acme/web-portal") || !strings.HasPrefix(string(got), "# dillodash: pulled from acme/dashboard-config:config.toml @ ") {
		t.Errorf("installed and stamped:\n%s", got)
	}
	if h.m.configStale() || !h.m.restartQueued {
		t.Errorf("current now, restart queued: %v %v (status %q)", h.m.configStale(), h.m.restartQueued, h.m.status)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
