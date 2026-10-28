package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
)

const sampleINI = `; pr-dash config
[defaults]
repo = acme/infra
user = alice
refresh_secs = 120
icons = unicode
theme = mono
motion = off
sound = on

[user.alice]
token_command = pass show github/token

[user.infra-bot]
token_file = ~/.config/pr-dash/infra-bot.token

[releases]
repos = app, web; acme/api
discover_ttl_secs = 3600
cache = ~/.cache/pr-dash/release-repos.json

[colors]
accent = gold
`

func TestMigrateINI(t *testing.T) {
	out, notes, err := MigrateINI([]byte(sampleINI))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "migrated", Overrides{})
	if err != nil {
		t.Fatalf("migrated TOML does not load: %v\n%s", err, out)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("migrated TOML warns: %v", c.Warnings)
	}
	if c.GitHub.Login != "alice" || c.GitHub.Host != "github.com" {
		t.Errorf("github = %+v", c.GitHub)
	}
	if !reflect.DeepEqual(c.Reviews.Repos, []model.Repo{{Owner: "acme", Name: "infra"}}) {
		t.Errorf("repos = %v", c.Reviews.Repos)
	}
	if c.Display != (DisplayConfig{RefreshSecs: 120, Icons: "unicode", Theme: "mono", Motion: "off", Mascot: "sprite"}) {
		t.Errorf("display = %+v", c.Display)
	}
	if !reflect.DeepEqual(c.Bots.Logins, []string{"github-actions", "dependabot"}) {
		t.Errorf("bots = %v", c.Bots.Logins)
	}
	if c.TerraformApprove == nil || !reflect.DeepEqual(c.TerraformApprove.Repos, c.Reviews.Repos) {
		t.Errorf("terraform_approve = %+v", c.TerraformApprove)
	}
	if c.MaintenanceWindow == nil || c.MaintenanceWindow.ManifestRepo.String() != "acme/infra" {
		t.Errorf("maintenance_window = %+v", c.MaintenanceWindow)
	}
	rp := c.ReleasePR
	if rp == nil {
		t.Fatal("release_pr off")
	}
	wantRepos := []model.Repo{{Owner: "acme", Name: "app"}, {Owner: "acme", Name: "web"}, {Owner: "acme", Name: "api"}}
	if rp.Auto || !reflect.DeepEqual(rp.Repos, wantRepos) || rp.Org != "acme" || rp.DiscoverTTLSecs != 3600 {
		t.Errorf("release_pr = %+v", rp)
	}
	if !strings.HasSuffix(rp.Cache, "/.cache/pr-dash/release-repos.json") {
		t.Errorf("cache = %q", rp.Cache)
	}

	// the TOML text itself
	for _, want := range []string{
		"# dillodash config, migrated from a pr-dash config.ini.",
		"enabled = false on any protocol",
		"[workflows.terraform_approve]\nenabled = true",
		"[workflows.maintenance_window]\nenabled = true",
		"[workflows.release_pr]\nenabled = true",
		`repos = ["acme/app", "acme/web", "acme/api"]`,
		"Add your own automation account",
		`logins = ["github-actions", "dependabot"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "token") {
		t.Error("tokens must not leak into the TOML")
	}

	// the notes
	joined := strings.Join(notes, "\n")
	for _, want := range []string{
		"[defaults] repo = acme/infra became [reviews] repos",
		"[defaults] user = alice became [github] login",
		"[defaults] sound: unknown key, dropped",
		"[user.alice], [user.infra-bot] dropped: dillodash authenticates through the gh CLI; run `gh auth login`",
		"[releases] repos list became [workflows.release_pr] repos, each prefixed with acme/",
		"[releases] cache kept as given",
		"[colors]: unknown section, dropped",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q:\n%s", want, joined)
		}
	}
	if !strings.HasPrefix(notes[0], "[defaults] repo") {
		t.Errorf("the repo note comes first: %v", notes)
	}
}

func TestMigrateINIReleases(t *testing.T) {
	base := "[defaults]\nrepo = acme/infra\n"

	out, notes, err := MigrateINI([]byte(base + "[releases]\nrepos = none\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "m", Overrides{})
	if err != nil || c.ReleasePR != nil {
		t.Fatalf("none: %v %+v", err, c.ReleasePR)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "repos = none became [workflows.release_pr] enabled = false") {
		t.Errorf("notes = %v", notes)
	}

	// an absent [releases] section meant auto
	out, notes, err = MigrateINI([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	c, err = Parse([]byte(out), "m", Overrides{})
	if err != nil || c.ReleasePR == nil || !c.ReleasePR.Auto || c.ReleasePR.Org != "acme" {
		t.Fatalf("auto: %v %+v", err, c.ReleasePR)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "repos = auto kept") {
		t.Errorf("notes = %v", notes)
	}
	if strings.Contains(out, "login =") {
		t.Error("no user, no login")
	}
}

func TestMigrateINIErrors(t *testing.T) {
	cases := []struct{ name, ini, want string }{
		{"no repo", "[defaults]\nuser = alice\n", "[defaults] repo: missing"},
		{"bad repo", "[defaults]\nrepo = infra\n", `[defaults] repo: "infra" is not "owner/name"`},
		{"bad line", "[defaults]\nrepo = acme/infra\nnonsense\n", "line 3: expected key = value"},
		{"key before section", "repo = acme/infra\n", "line 1: key before any [section]"},
		{"unterminated", "[defaults\nrepo = acme/infra\n", "line 1: unterminated section header"},
		{"empty release list", "[defaults]\nrepo = acme/infra\n[releases]\nrepos = ,\n", "[releases] repos: auto, none, or a comma list"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := MigrateINI([]byte(c.ini))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestTomlString(t *testing.T) {
	cases := map[string]string{
		`plain`:      `"plain"`,
		`a "quote"`:  `"a \"quote\""`,
		`back\slash`: `"back\\slash"`,
		"tab\there":  `"tab\there"`,
	}
	for in, want := range cases {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMigrateDropsBadDisplayChoice(t *testing.T) {
	ini := "[defaults]\nrepo = acme/infra\nicons = Unicode\ntheme = solarized\nmotion = off\n"
	out, notes, err := MigrateINI([]byte(ini))
	if err != nil {
		t.Fatalf("a bad choice must not fail the migration: %v", err)
	}
	c, err := Parse([]byte(out), "migrated", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Icons != "unicode" || c.Display.Theme != "auto" || c.Display.Motion != "off" {
		t.Errorf("display = %+v", c.Display)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, `theme = "solarized" is not one of auto, mono, dropped`) {
			found = true
		}
	}
	if !found {
		t.Errorf("notes %v", notes)
	}
}
