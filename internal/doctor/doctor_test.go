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
	gh.Open = map[model.Repo]ghapi.ListResult{infra: {PRs: []*model.PR{{Number: 1}}}}
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
	for _, want := range []string{"token: from keyring", "login: alice", "repo acme/infra:", "pull requests: 1 open", "actions: readable", "workflows:"} {
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
