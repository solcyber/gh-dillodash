package doctor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

var infra = model.Repo{Owner: "acme", Name: "infra"}

func report(t *testing.T, user, source string, cfg *config.Config) (string, error) {
	t.Helper()
	gh := fake.New(user)
	gh.Source = source
	gh.Open = map[model.Repo]ghapi.ListResult{infra: {PRs: []*model.PR{{Number: 1}}, Permission: "ADMIN"}}
	return reportWith(t, gh, cfg)
}

func reportWith(t *testing.T, gh *fake.Client, cfg *config.Config) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Report(context.Background(), &out, cfg, gh, config.Source{}, false)
	return out.String(), err
}

func cfgWith(repos ...model.Repo) *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = repos
	return cfg
}

func TestReportHuman(t *testing.T) {
	out, err := report(t, "alice", "keyring", cfgWith(infra))
	if err != nil {
		t.Fatalf("err %v\n%s", err, out)
	}
	for _, want := range []string{"token: from keyring", "login: alice", "repo acme/infra:", "pull requests: 1 open", "write: yes (ADMIN)", "actions: readable", "workflows:", "probed: the repository permission"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAILED") || strings.Contains(out, "overrides") {
		t.Errorf("a healthy report:\n%s", out)
	}
}

func TestReportRefusesBotLogin(t *testing.T) {
	out, err := report(t, "dependabot[bot]", "keyring", cfgWith(infra))
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(out, "login: FAILED: dependabot[bot] is a bot login") || !strings.Contains(out, "gh auth switch") {
		t.Errorf("report:\n%s", out)
	}
	if !strings.Contains(out, "repo acme/infra:") {
		t.Error("the probes still run after the bot line")
	}
}

func TestReportConfiguredBotLogin(t *testing.T) {
	cfg := cfgWith(infra)
	cfg.Bots.Logins = append(cfg.Bots.Logins, "infra-bot")
	out, err := report(t, "infra-bot", "GH_TOKEN", cfg)
	if err == nil {
		t.Fatalf("a [bots] login fails the check:\n%s", out)
	}
	if !strings.Contains(out, "unset GH_TOKEN") {
		t.Errorf("the hint names the env var that brought the token:\n%s", out)
	}
}

func TestReportNotesEnvToken(t *testing.T) {
	out, err := report(t, "alice", "GITHUB_TOKEN", cfgWith(infra))
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(out, "note: GITHUB_TOKEN in the environment overrides the gh login") {
		t.Errorf("report:\n%s", out)
	}
}

func TestReportMissingRepoFails(t *testing.T) {
	other := model.Repo{Owner: "acme", Name: "gone"}
	out, err := report(t, "alice", "keyring", cfgWith(infra, other))
	if err == nil || !strings.Contains(out, "list: FAILED") {
		t.Errorf("err %v\n%s", err, out)
	}
}

func TestBotHint(t *testing.T) {
	if h := BotHint("GH_TOKEN"); !strings.Contains(h, "unset GH_TOKEN") {
		t.Errorf("env: %q", h)
	}
	if h := BotHint("keyring"); !strings.Contains(h, "gh auth switch") {
		t.Errorf("keyring: %q", h)
	}
}

// TestReportWritePermission: the viewer's role is printed per repo; a
// token that is read-only everywhere fails the check, read-only in one
// repo of several only says so there, and a token that reports no role
// (fine-grained) is neither.
func TestReportWritePermission(t *testing.T) {
	portal := model.Repo{Owner: "acme", Name: "web-portal"}
	open := func(perms ...string) map[model.Repo]ghapi.ListResult {
		out := map[model.Repo]ghapi.ListResult{infra: {Permission: perms[0]}}
		if len(perms) > 1 {
			out[portal] = ghapi.ListResult{Permission: perms[1]}
		}
		return out
	}
	cases := []struct {
		name  string
		open  map[model.Repo]ghapi.ListResult
		repos []model.Repo
		fails bool
		want  []string
	}{
		{"read-only everywhere", open("READ"), []model.Repo{infra}, true,
			[]string{"write: NO (READ) — approve, comment, merge and deployment approval will 403", "write: FAILED: every review repo is read-only"}},
		{"triage cannot approve either", open("TRIAGE", "TRIAGE"), []model.Repo{infra, portal}, true, []string{"write: NO (TRIAGE)", "write: FAILED"}},
		{"read-only in one of two", open("WRITE", "READ"), []model.Repo{infra, portal}, false, []string{"write: yes (WRITE)", "write: NO (READ)"}},
		{"not reported", open(""), []model.Repo{infra}, false, []string{"write: not reported — fine-grained tokens report grants"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := fake.New("alice")
			gh.Open = c.open
			out, err := reportWith(t, gh, cfgWith(c.repos...))
			if (err != nil) != c.fails {
				t.Fatalf("err %v, want failure %v:\n%s", err, c.fails, out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
			if !c.fails && strings.Contains(out, "write: FAILED") {
				t.Errorf("no summary failure wanted:\n%s", out)
			}
		})
	}
}

func TestReportWaitingRuns(t *testing.T) {
	cfg := cfgWith(infra)
	out, _ := report(t, "alice", "keyring", cfg)
	if !strings.Contains(out, "  waiting_runs         off\n") {
		t.Errorf("waiting_runs off:\n%s", out)
	}
	wr := config.DefaultWaitingRuns()
	wr.Repos = []model.Repo{infra, {Owner: "acme", Name: "etl-jobs"}}
	cfg.WaitingRuns = wr
	out, _ = report(t, "alice", "keyring", cfg)
	if !strings.Contains(out, "  waiting_runs         on  repos: acme/infra, acme/etl-jobs\n") {
		t.Errorf("waiting_runs on:\n%s", out)
	}
}

// TestReportReleaseReach: release_pr says where /release-now goes and
// whether a release is followed live, and probes the manifest repo for
// both without failing the report.
func TestReportReleaseReach(t *testing.T) {
	cfg := cfgWith(infra)
	cfg.MaintenanceWindow = config.DefaultMaintenanceWindow()
	cfg.MaintenanceWindow.ManifestRepo = infra
	cfg.ReleasePR = config.DefaultReleasePR()
	gh := fake.New("alice")
	gh.Open = map[model.Repo]ghapi.ListResult{infra: {Permission: "WRITE"}}
	out, err := reportWith(t, gh, cfg)
	if err != nil {
		t.Fatalf("err %v\n%s", err, out)
	}
	for _, want := range []string{"/release-now is dispatched to acme/infra", "live progress every 30s from acme/infra's apply.yml", "dispatch to acme/infra: yes (WRITE)", "apply.yml runs in acme/infra: readable"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	gh.Open[infra] = ghapi.ListResult{Permission: "READ"}
	gh.Fail["file_runs"] = &ghapi.Error{Status: 403, Message: "Resource not accessible"}
	cfg.Reviews.Repos = nil // only the manifest probe reads infra now
	out, _ = reportWith(t, gh, cfg)
	for _, want := range []string{"dispatch to acme/infra: NO (READ)", "apply.yml runs in acme/infra: not readable"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	cfg.MaintenanceWindow = nil
	if out, _ = reportWith(t, gh, cfg); !strings.Contains(out, "no dispatch or live progress without maintenance_window") || strings.Contains(out, "dispatch to") {
		t.Errorf("without the window:\n%s", out)
	}
}
