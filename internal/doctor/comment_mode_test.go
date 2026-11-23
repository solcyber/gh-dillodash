package doctor

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// TestCommentModeChecksTheAppRepos: with release_now_dispatch = "comment"
// the report looks in each release repo for a workflow on issue_comment
// that mentions the command, instead of the manifest repo's write. It
// reports and never fails the check.
func TestCommentModeChecksTheAppRepos(t *testing.T) {
	web := model.Repo{Owner: "acme", Name: "web-portal"}
	etl := model.Repo{Owner: "acme", Name: "etl-jobs"}
	db := model.Repo{Owner: "acme", Name: "db-migrations"}
	cfg := cfgWith(infra)
	mw := config.DefaultMaintenanceWindow()
	mw.ManifestRepo = infra
	cfg.MaintenanceWindow = mw
	rp := config.DefaultReleasePR()
	rp.Auto, rp.Repos, rp.Org = false, []model.Repo{web, etl, db}, "acme"
	rp.ReleaseNowDispatch, rp.ReleaseNowCommentOnly = true, true
	cfg.ReleasePR = rp

	gh := fake.New("alice")
	gh.Open = map[model.Repo]ghapi.ListResult{infra: {Permission: "ADMIN"}}
	gh.FileData = map[string][]byte{
		"acme/web-portal:.github/workflows/ci.yml":          []byte("on:\n  push:\n"),
		"acme/web-portal:.github/workflows/release-now.yml": []byte("on:\n  issue_comment:\n    types: [created]\njobs:\n  tell:\n    if: startsWith(github.event.comment.body, '/release-now')\n"),
		"acme/etl-jobs:.github/workflows/triage.yml":        []byte("on:\n  issue_comment:\n"),
	}
	out, err := reportWith(t, gh, cfg)
	if err != nil {
		t.Fatalf("comment mode never fails the check: %v\n%s", err, out)
	}
	for _, want := range []string{
		"/release-now is posted only — the release repos' own workflow tells acme/infra",
		"comment workflow in acme/web-portal: release-now.yml",
		"comment workflow in acme/etl-jobs: NONE (no workflow on issue_comment mentions release-now)",
		"comment workflow in acme/db-migrations: NONE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dispatch to acme/infra") {
		t.Errorf("comment mode does not probe the manifest repo's write:\n%s", out)
	}

	gh.Fail = map[string]error{"dir": &ghapi.Error{Status: 403, Message: "Resource not accessible"}}
	out, err = reportWith(t, gh, cfg)
	if err != nil || !strings.Contains(out, "comment workflow in acme/web-portal: not readable") {
		t.Errorf("an unreadable repo is reported, not failed: %v\n%s", err, out)
	}

	// dispatch mode still probes the manifest repo
	rp.ReleaseNowCommentOnly = false
	gh.Fail = nil
	out, _ = reportWith(t, gh, cfg)
	if !strings.Contains(out, "dispatch to acme/infra: yes (ADMIN)") || strings.Contains(out, "comment workflow") {
		t.Errorf("dispatch mode:\n%s", out)
	}
}
