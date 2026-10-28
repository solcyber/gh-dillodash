package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/model"
)

const minimal = "[reviews]\nrepos = [\"acme/infra\"]\n"

func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func parse(t *testing.T, src string) (*Config, error) {
	t.Helper()
	return parseEnv([]byte(src), "cfg.toml", Overrides{}, fakeEnv(map[string]string{"HOME": "/home/alice"}))
}

func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	c, err := parse(t, src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return c
}

func repo(s string) model.Repo {
	r, _ := model.ParseRepo(s)
	return r
}

func TestDefaults(t *testing.T) {
	c := mustParse(t, minimal)
	want := Default()
	want.Path = "cfg.toml"
	want.Reviews.Repos = []model.Repo{repo("acme/infra")}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("defaults differ:\n got %+v\nwant %+v", c, want)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings: %v", c.Warnings)
	}
	if c.TerraformApprove != nil || c.MaintenanceWindow != nil || c.ReleasePR != nil {
		t.Fatal("workflows should be off by default")
	}
	if c.Reviews.Multi() {
		t.Error("Multi with one repo")
	}
}

func TestDefaultHasNoRepos(t *testing.T) {
	d := Default()
	if len(d.Reviews.Repos) != 0 || d.Display.RefreshSecs != 300 || d.GitHub.Host != "github.com" {
		t.Fatalf("Default() = %+v", d)
	}
	if !reflect.DeepEqual(d.Bots.Logins, []string{"github-actions", "dependabot"}) {
		t.Fatalf("bots = %v", d.Bots.Logins)
	}
}

func TestEverythingSet(t *testing.T) {
	src := `
[github]
host = "github.example.com"
login = "alice"

[reviews]
repos = ["acme/infra", "acme/app", "ACME/infra"]
ci_recheck_secs = 60

[display]
refresh_secs = 30
icons = "unicode"
theme = "mono"
motion = "off"
mascot = "none"

[bots]
logins = ["Infra-Bot", "github-actions", "infra-bot"]

[workflows.terraform_approve]
enabled = true
repos = ["acme/infra"]
digest_bot = "Infra-Bot"
plan_workflow = "tf-plan.yml"
gate_context = "tf/gate"
gate_done_prefixes = ["done:"]
approve_command = "/ok"
confirm_destroys_flag = "--yes-destroy"
outside_window_flag = "--late"
outside_window_min_reason = 5
ceremony_window = { timezone = "Europe/London", from = "22:00", to = "22:30" }

[workflows.maintenance_window]
enabled = true
manifest_repo = "acme/ops"
timezone = "Europe/London"
tz_label = "UK"
cutoff = "20:00"
release_from = "21:59"
start = "22:00"
end = "22:30"
queued_label = "q"
scheduled_label = "s"
rolled_label = "r"
manifest_label = "m"
manifest_title = "Night {night}"
conductor_bot = "Infra-Bot"
team = "acme/sre"
attend_command = "/here"
release_command = "/go"
cancel_command = "/stop"

[workflows.release_pr]
enabled = true
repos = ["acme/app", "acme/web"]
org = "acme"
ruleset = "Std"
discover_ttl_secs = 10
cache = "~/rel-{org}.json"
production_branch = "prod"
staging_branch = "staging"
release_prefix = "rel/"
hotfix_prefix = "fix/"
go_tag_prefix = "ship/"
hold_tag_prefix = "wait/"
list_other_prs = false
release_now_command = "/now"
release_now_min_reason = 3
approval_queues_for_window = true
merge_method = "squash"
merged_keep_secs = 1
deploy_stuck_secs = 2
deploy_refresh_secs = 3
deploy_approval_comment = "merged {repo}#{number} {sha}"
`
	c := mustParse(t, src)
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings: %v", c.Warnings)
	}
	if c.GitHub != (GitHubConfig{Host: "github.example.com", Login: "alice"}) {
		t.Errorf("github = %+v", c.GitHub)
	}
	if want := []model.Repo{repo("acme/infra"), repo("acme/app")}; !reflect.DeepEqual(c.Reviews.Repos, want) {
		t.Errorf("repos = %v (dedupe case-insensitively, keep order)", c.Reviews.Repos)
	}
	if c.Reviews.CIRecheckSecs != 60 || !c.Reviews.Multi() {
		t.Errorf("reviews = %+v", c.Reviews)
	}
	if c.Display != (DisplayConfig{RefreshSecs: 30, Icons: "unicode", Theme: "mono", Motion: "off", Mascot: "none"}) {
		t.Errorf("display = %+v", c.Display)
	}
	if want := []string{"infra-bot", "github-actions"}; !reflect.DeepEqual(c.Bots.Logins, want) {
		t.Errorf("bots = %v", c.Bots.Logins)
	}

	ta := c.TerraformApprove
	if ta == nil {
		t.Fatal("terraform_approve off")
	}
	wantTA := &TerraformApproveConfig{
		Repos: []model.Repo{repo("acme/infra")}, DigestBot: "infra-bot", PlanWorkflow: "tf-plan.yml",
		GateContext: "tf/gate", GateDonePrefixes: []string{"done:"}, ApproveCommand: "/ok",
		ConfirmDestroysFlag: "--yes-destroy", OutsideWindowFlag: "--late", OutsideWindowMinReason: 5,
		CeremonyWindow: &CeremonyWindow{Timezone: "Europe/London", From: clock.HM{H: 22}, To: clock.HM{H: 22, M: 30}},
	}
	if !reflect.DeepEqual(ta, wantTA) {
		t.Errorf("terraform_approve =\n %+v\nwant\n %+v", ta, wantTA)
	}
	if ta.CeremonyWindow.Local() == nil || ta.CeremonyWindow.Local().Loc.String() != "Europe/London" {
		t.Error("ceremony window Local()")
	}

	mw := c.MaintenanceWindow
	if mw == nil {
		t.Fatal("maintenance_window off")
	}
	wantMW := &MaintenanceWindowConfig{
		ManifestRepo: repo("acme/ops"), Timezone: "Europe/London", TZLabel: "UK",
		Cutoff: clock.HM{H: 20}, ReleaseFrom: clock.HM{H: 21, M: 59}, Start: clock.HM{H: 22}, End: clock.HM{H: 22, M: 30},
		QueuedLabel: "q", ScheduledLabel: "s", RolledLabel: "r", ManifestLabel: "m", ManifestTitle: "Night {night}",
		ConductorBot: "infra-bot", Team: "acme/sre", AttendCommand: "/here", ReleaseCommand: "/go", CancelCommand: "/stop",
	}
	if !reflect.DeepEqual(mw, wantMW) {
		t.Errorf("maintenance_window =\n %+v\nwant\n %+v", mw, wantMW)
	}
	if l := mw.Local(); l == nil || l.Label != "UK" {
		t.Error("maintenance window Local()")
	}

	rp := c.ReleasePR
	if rp == nil {
		t.Fatal("release_pr off")
	}
	wantRP := &ReleasePRConfig{
		Auto: false, Repos: []model.Repo{repo("acme/app"), repo("acme/web")}, Org: "acme", Ruleset: "Std",
		DiscoverTTLSecs: 10, Cache: filepath.FromSlash("/home/alice/rel-acme.json"), ProductionBranch: "prod", StagingBranch: "staging",
		ReleasePrefix: "rel/", HotfixPrefix: "fix/", GoTagPrefix: "ship/", HoldTagPrefix: "wait/", ListOtherPRs: false,
		ReleaseNowCommand: "/now", ReleaseNowMinReason: 3, ApprovalQueuesForWindow: true, MergeMethod: "squash",
		MergedKeepSecs: 1, DeployStuckSecs: 2, DeployRefreshSecs: 3, DeployApprovalComment: "merged {repo}#{number} {sha}",
	}
	if !reflect.DeepEqual(rp, wantRP) {
		t.Errorf("release_pr =\n %+v\nwant\n %+v", rp, wantRP)
	}
	if got := rp.DeployComment(repo("acme/app"), 42, "abc123"); got != "merged acme/app#42 abc123" {
		t.Errorf("DeployComment = %q", got)
	}
}

func TestWorkflowDerivedDefaults(t *testing.T) {
	src := `
[reviews]
repos = ["acme/infra", "acme/app"]

[workflows.terraform_approve]
enabled = true

[workflows.maintenance_window]
enabled = true

[workflows.release_pr]
enabled = true
`
	c, err := parseEnv([]byte(src), "cfg.toml", Overrides{}, fakeEnv(map[string]string{"HOME": "/home/alice", "XDG_CACHE_HOME": "/xdg/cache"}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.TerraformApprove.Repos, c.Reviews.Repos) {
		t.Errorf("terraform repos = %v", c.TerraformApprove.Repos)
	}
	if c.TerraformApprove.CeremonyWindow != nil {
		t.Error("ceremony window should be nil when absent")
	}
	if c.MaintenanceWindow.ManifestRepo != repo("acme/infra") {
		t.Errorf("manifest repo = %v", c.MaintenanceWindow.ManifestRepo)
	}
	if !c.ReleasePR.Auto || c.ReleasePR.Org != "acme" {
		t.Errorf("release_pr = %+v", c.ReleasePR)
	}
	if c.ReleasePR.Cache != filepath.FromSlash("/xdg/cache/dillodash/release-repos-acme.json") {
		t.Errorf("cache = %q", c.ReleasePR.Cache)
	}
	wantWarn := "[workflows.maintenance_window] manifest_repo: unset with 2 review repos, using the first (acme/infra)"
	if !reflect.DeepEqual(c.Warnings, []string{wantWarn}) {
		t.Errorf("warnings = %v", c.Warnings)
	}

	// without XDG_CACHE_HOME the default lives under ~/.cache
	c = mustParse(t, "[reviews]\nrepos = [\"acme/infra\"]\n[workflows.release_pr]\nenabled = true\n")
	if c.ReleasePR.Cache != filepath.FromSlash("/home/alice/.cache/dillodash/release-repos-acme.json") {
		t.Errorf("cache = %q", c.ReleasePR.Cache)
	}
	// an explicit cache ignores XDG_CACHE_HOME but still expands ~ and {org}
	c, err = parseEnv([]byte("[reviews]\nrepos = [\"acme/infra\"]\n[workflows.release_pr]\nenabled = true\ncache = \"~/x/{org}.json\"\n"),
		"cfg.toml", Overrides{}, fakeEnv(map[string]string{"HOME": "/home/alice", "XDG_CACHE_HOME": "/xdg/cache"}))
	if err != nil || c.ReleasePR.Cache != filepath.FromSlash("/home/alice/x/acme.json") {
		t.Errorf("cache = %q, %v", c.ReleasePR.Cache, err)
	}
	// defaults for the workflow structs themselves
	if d := DefaultReleasePR(); !d.Auto || d.Cache != DefaultReleaseCache || d.MergeMethod != "merge" {
		t.Errorf("DefaultReleasePR = %+v", d)
	}
	if d := DefaultMaintenanceWindow(); d.ManifestTitle != "Window {night}" || d.Start != (clock.HM{H: 23}) {
		t.Errorf("DefaultMaintenanceWindow = %+v", d)
	}
	if d := DefaultTerraformApprove(); d.OutsideWindowMinReason != 20 || len(d.GateDonePrefixes) != 2 {
		t.Errorf("DefaultTerraformApprove = %+v", d)
	}
}

func TestWorkflowOff(t *testing.T) {
	// enabled = false: off, no warning, but still validated
	c := mustParse(t, minimal+"[workflows.release_pr]\nenabled = false\nmerge_method = \"squash\"\n")
	if c.ReleasePR != nil || len(c.Warnings) != 0 {
		t.Fatalf("release_pr = %+v, warnings = %v", c.ReleasePR, c.Warnings)
	}
	_, err := parse(t, minimal+"[workflows.release_pr]\nenabled = false\nmerge_method = \"ff\"\n")
	if err == nil || !strings.Contains(err.Error(), `[workflows.release_pr] merge_method: "ff" is not one of merge, squash, rebase`) {
		t.Fatalf("disabled tables are still validated: %v", err)
	}
	// a disabled workflow does not trip cross-checks that need it
	c = mustParse(t, minimal+"[workflows.release_pr]\nenabled = false\napproval_queues_for_window = true\n")
	if c.ReleasePR != nil {
		t.Fatal("should be off")
	}
	c = mustParse(t, "[reviews]\nrepos = [\"acme/infra\", \"globex/app\"]\n[workflows.release_pr]\nenabled = false\n")
	if c.ReleasePR != nil || len(c.Warnings) != 0 {
		t.Fatalf("org derivation only matters when enabled: %v", c.Warnings)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // substring of err.Error()
	}{
		{"invalid toml", "[reviews\nrepos = 1", "cfg.toml: invalid TOML at line 1"},
		{"repos required", "[display]\n", `cfg.toml: [reviews] repos: required: at least one "owner/name" (or pass --repo)`},
		{"repos empty", "[reviews]\nrepos = []\n", `[reviews] repos: must list at least one "owner/name"`},
		{"repos bad entry", "[reviews]\nrepos = [\"acme\"]\n", `[reviews] repos: "acme" is not "owner/name"`},
		{"repos bad entry slashes", "[reviews]\nrepos = [\"acme/infra/x\"]\n", `[reviews] repos: "acme/infra/x" is not "owner/name"`},
		{"repos wrong type", "[reviews]\nrepos = \"acme/infra\"\n", `[reviews] repos: expected an array of strings, got a string`},
		{"repos entry wrong type", "[reviews]\nrepos = [1]\n", `[reviews] repos: entry 1: expected a string, got an integer`},
		{"section wrong type", "reviews = 1\n", `reviews: expected a table, got an integer`},
		{"int wrong type", minimal + "ci_recheck_secs = \"soon\"\n", `[reviews] ci_recheck_secs: expected an integer, got a string`},
		{"int float", minimal + "ci_recheck_secs = 1.5\n", `[reviews] ci_recheck_secs: expected an integer, got a float`},
		{"int negative", minimal + "ci_recheck_secs = -1\n", `[reviews] ci_recheck_secs: must not be negative, got -1`},
		{"string wrong type", minimal + "[github]\nhost = 1\n", `[github] host: expected a string, got an integer`},
		{"host empty", minimal + "[github]\nhost = \"\"\n", `[github] host: must not be empty`},
		{"host url", minimal + "[github]\nhost = \"https://github.com\"\n", `[github] host: "https://github.com" must be a bare hostname like "github.com"`},
		{"bool wrong type", minimal + "[workflows.release_pr]\nenabled = \"yes\"\n", `[workflows.release_pr] enabled: expected a boolean (true or false), got a string`},
		{"icons", minimal + "[display]\nicons = \"emoji\"\n", `[display] icons: "emoji" is not one of nerd, unicode, ascii`},
		{"theme", minimal + "[display]\ntheme = \"dark\"\n", `[display] theme: "dark" is not one of auto, mono`},
		{"motion", minimal + "[display]\nmotion = \"some\"\n", `[display] motion: "some" is not one of full, off`},
		{"mascot", minimal + "[display]\nmascot = \"cat\"\n", `[display] mascot: "cat" is not one of sprite, ascii, none`},
		{"refresh type", minimal + "[display]\nrefresh_secs = \"5m\"\n", `[display] refresh_secs: expected an integer, got a string`},
		{"bots empty entry", minimal + "[bots]\nlogins = [\"infra-bot\", \" \"]\n", `[bots] logins: entry 2 is empty`},
		{"bots wrong type", minimal + "[bots]\nlogins = \"infra-bot\"\n", `[bots] logins: expected an array of strings, got a string`},
		{"ceremony missing keys", minimal + "[workflows.terraform_approve]\nenabled = true\nceremony_window = { timezone = \"America/New_York\", from = \"23:00\" }\n",
			`[workflows.terraform_approve] ceremony_window: needs timezone, from and to (missing to)`},
		{"ceremony wrong type", minimal + "[workflows.terraform_approve]\nenabled = true\nceremony_window = \"23:00-23:28\"\n",
			`[workflows.terraform_approve] ceremony_window: expected a table like { timezone = "America/New_York", from = "23:00", to = "23:30" }, got a string`},
		{"ceremony tz", minimal + "[workflows.terraform_approve]\nenabled = true\nceremony_window = { timezone = \"Mars/Olympus\", from = \"23:00\", to = \"23:28\" }\n",
			`[workflows.terraform_approve.ceremony_window] timezone: "Mars/Olympus" is not an IANA timezone (like "America/New_York")`},
		{"ceremony hm", minimal + "[workflows.terraform_approve]\nenabled = true\nceremony_window = { timezone = \"America/New_York\", from = \"25:00\", to = \"23:28\" }\n",
			`[workflows.terraform_approve.ceremony_window] from: "25:00" is not HH:MM (00:00-23:59)`},
		{"ta repos", minimal + "[workflows.terraform_approve]\nenabled = true\nrepos = [\"infra\"]\n", `[workflows.terraform_approve] repos: "infra" is not "owner/name"`},
		{"ta empty string", minimal + "[workflows.terraform_approve]\nenabled = true\napprove_command = \"\"\n", `[workflows.terraform_approve] approve_command: must not be empty`},
		{"mw title", minimal + "[workflows.maintenance_window]\nenabled = true\nmanifest_title = \"Window tonight\"\n", `[workflows.maintenance_window] manifest_title: "Window tonight" must contain {night}`},
		{"mw tz", minimal + "[workflows.maintenance_window]\nenabled = true\ntimezone = \"Eastern\"\n", `[workflows.maintenance_window] timezone: "Eastern" is not an IANA timezone`},
		{"mw tz empty", minimal + "[workflows.maintenance_window]\nenabled = true\ntimezone = \"\"\n", `[workflows.maintenance_window] timezone: must be an IANA timezone like "America/New_York"`},
		{"mw hm", minimal + "[workflows.maintenance_window]\nenabled = true\ncutoff = \"9pm\"\n", `[workflows.maintenance_window] cutoff: "9pm" is not HH:MM (00:00-23:59)`},
		{"mw hm type", minimal + "[workflows.maintenance_window]\nenabled = true\nstart = 2300\n", `[workflows.maintenance_window] start: expected a string like "23:00", got an integer`},
		{"mw manifest repo", minimal + "[workflows.maintenance_window]\nenabled = true\nmanifest_repo = \"infra\"\n", `[workflows.maintenance_window] manifest_repo: "infra" is not "owner/name"`},
		{"mw team", minimal + "[workflows.maintenance_window]\nenabled = true\nteam = \"sre\"\n", `[workflows.maintenance_window] team: "sre" is not "org/team"`},
		{"rp repos string", minimal + "[workflows.release_pr]\nenabled = true\nrepos = \"all\"\n", `[workflows.release_pr] repos: "all" must be "auto" or a list of "owner/name"`},
		{"rp repos empty", minimal + "[workflows.release_pr]\nenabled = true\nrepos = []\n", `[workflows.release_pr] repos: must be "auto" or a non-empty list of "owner/name"`},
		{"rp repos entry", minimal + "[workflows.release_pr]\nenabled = true\nrepos = [\"app\"]\n", `[workflows.release_pr] repos: "app" is not "owner/name"`},
		{"rp repos type", minimal + "[workflows.release_pr]\nenabled = true\nrepos = true\n", `[workflows.release_pr] repos: must be "auto" or a list of "owner/name", got a boolean`},
		{"rp prefixes", minimal + "[workflows.release_pr]\nenabled = true\nhotfix_prefix = \"release/\"\n", `[workflows.release_pr] hotfix_prefix: "release/" must differ from release_prefix`},
		{"rp merge", minimal + "[workflows.release_pr]\nenabled = true\nmerge_method = \"fast-forward\"\n", `[workflows.release_pr] merge_method: "fast-forward" is not one of merge, squash, rebase`},
		{"rp placeholder", minimal + "[workflows.release_pr]\nenabled = true\ndeploy_approval_comment = \"{repo} by {author}\"\n", `[workflows.release_pr] deploy_approval_comment: unknown placeholder {author} (allowed: {repo} {number} {sha})`},
		{"rp org", "[reviews]\nrepos = [\"acme/infra\", \"globex/app\"]\n[workflows.release_pr]\nenabled = true\n", `[workflows.release_pr] org: unset and the review repos span several owners (acme, globex); set it`},
		{"rp queues", minimal + "[workflows.release_pr]\nenabled = true\napproval_queues_for_window = true\n", `[workflows.release_pr] approval_queues_for_window: true needs [workflows.maintenance_window] enabled = true`},
		{"rp cache empty", minimal + "[workflows.release_pr]\nenabled = true\ncache = \"\"\n", `[workflows.release_pr] cache: must not be empty`},
		{"rp cache type", minimal + "[workflows.release_pr]\nenabled = true\ncache = 1\n", `[workflows.release_pr] cache: expected a string, got an integer`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := parse(t, c.src)
			if err == nil {
				t.Fatalf("want error containing %q, got none (warnings %v)", c.want, cfg.Warnings)
			}
			if cfg != nil {
				t.Error("config should be nil on error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q\n  does not contain %q", err.Error(), c.want)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error is not a *config.Error: %T", err)
			}
		})
	}
}

func TestMultipleErrorsAreAllReported(t *testing.T) {
	_, err := parse(t, "[reviews]\nrepos = [\"acme\"]\n[display]\nicons = \"x\"\ntheme = \"y\"\n")
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{`[reviews] repos: "acme"`, `[display] icons: "x"`, `[display] theme: "y"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %q", want, err.Error())
		}
	}
}

func TestErrorFormat(t *testing.T) {
	cases := []struct {
		e    Error
		want string
	}{
		{Error{Path: "cfg.toml", Where: "[display] icons", Why: "bad"}, "cfg.toml: [display] icons: bad"},
		{Error{Path: "cfg.toml", Why: "not found"}, "cfg.toml: not found"},
		{Error{Where: "--repo", Why: "bad"}, "--repo: bad"},
		{Error{Why: "bad"}, "bad"},
	}
	for _, c := range cases {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestWarnings(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"unknown key with hint", minimal + "[display]\nrefresh_sec = 5\n",
			[]string{"[display] refresh_sec: unknown key, ignored (did you mean refresh_secs?)"}},
		{"unknown key hyphen", minimal + "[display]\nrefresh-secs = 50\n",
			[]string{"[display] refresh-secs: unknown key, ignored (did you mean refresh_secs?)"}},
		{"unknown key no hint", minimal + "[display]\ncolour = \"blue\"\n",
			[]string{"[display] colour: unknown key, ignored"}},
		{"unknown table with hint", minimal + "[displays]\nicons = \"ascii\"\n",
			[]string{"[displays]: unknown table, ignored (did you mean [display]?)"}},
		{"unknown table no hint", minimal + "[plugins]\nx = 1\n",
			[]string{"[plugins]: unknown table, ignored"}},
		{"top-level key exact", "refresh_secs = 5\n" + minimal,
			[]string{"refresh_secs: unknown key, ignored (did you mean [display] refresh_secs?)"}},
		{"top-level key fuzzy", "refresh_sec = 5\n" + minimal,
			[]string{"refresh_sec: unknown key, ignored (did you mean [display] refresh_secs?)"}},
		{"unknown workflow", minimal + "[workflows.release_prs]\nenabled = true\n",
			[]string{"[workflows] release_prs: unknown table, ignored (did you mean [workflows.release_pr]?)"}},
		{"unknown workflow key", minimal + "[workflows.release_pr]\nenabled = false\nmerge = \"squash\"\n",
			[]string{"[workflows.release_pr] merge: unknown key, ignored (did you mean merge_method?)"}},
		{"unknown ceremony key", minimal + "[workflows.terraform_approve]\nenabled = false\nceremony_window = { timezone = \"America/New_York\", from = \"23:00\", to = \"23:28\", until = \"x\" }\n",
			[]string{"[workflows.terraform_approve.ceremony_window] until: unknown key, ignored"}},
		{"refresh floor", minimal + "[display]\nrefresh_secs = 2\n",
			[]string{"[display] refresh_secs: 2 is below the floor of 5, using 5"}},
		{"reserved repos", minimal + "[repos.\"acme/infra\"]\nicons = \"ascii\"\n",
			[]string{"[repos]: reserved for a future release, ignored"}},
		{"enabled missing", minimal + "[workflows.release_pr]\nmerge_method = \"squash\"\n",
			[]string{"[workflows.release_pr] enabled: missing, the workflow stays off (add enabled = true to turn it on)"}},
		{"window order", minimal + "[workflows.maintenance_window]\nenabled = true\ncutoff = \"23:10\"\n",
			[]string{"[workflows.maintenance_window] cutoff, release_from, start, end: expected cutoff < release_from < start < end, got 23:10, 22:59, 23:00, 23:30"}},
		{"ceremony order", minimal + "[workflows.terraform_approve]\nenabled = true\nceremony_window = { timezone = \"America/New_York\", from = \"23:28\", to = \"23:00\" }\n",
			[]string{"[workflows.terraform_approve] ceremony_window: from 23:28 is not before to 23:00; the window is same-day and will never be open"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := parse(t, c.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(cfg.Warnings, c.want) {
				t.Fatalf("warnings\n got %q\nwant %q", cfg.Warnings, c.want)
			}
		})
	}
}

func TestRefreshFloorClamps(t *testing.T) {
	c := mustParse(t, minimal+"[display]\nrefresh_secs = 2\n")
	if c.Display.RefreshSecs != MinRefreshSecs {
		t.Fatalf("refresh = %d", c.Display.RefreshSecs)
	}
}

func TestOverrides(t *testing.T) {
	one := 1
	c, err := parseEnv([]byte(minimal+"[display]\nicons = \"ascii\"\n"), "cfg.toml", Overrides{
		Repos: []string{"acme/app", "acme/web", "acme/app"}, RefreshSecs: &one, Icons: "unicode", Motion: "off", Mono: true,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := []model.Repo{repo("acme/app"), repo("acme/web")}; !reflect.DeepEqual(c.Reviews.Repos, want) {
		t.Errorf("--repo replaces the list: %v", c.Reviews.Repos)
	}
	if c.Display != (DisplayConfig{RefreshSecs: 5, Icons: "unicode", Theme: "mono", Motion: "off", Mascot: "sprite"}) {
		t.Errorf("display = %+v", c.Display)
	}
	if !reflect.DeepEqual(c.Warnings, []string{"--refresh: 1 is below the floor of 5, using 5"}) {
		t.Errorf("warnings = %v", c.Warnings)
	}

	// --repo satisfies the requirement when the file has none
	c, err = parseEnv([]byte("[display]\n"), "cfg.toml", Overrides{Repos: []string{"acme/infra"}}, fakeEnv(nil))
	if err != nil || len(c.Reviews.Repos) != 1 {
		t.Fatalf("--repo without file repos: %v %v", err, c)
	}
	// and the --theme flag by itself
	c, err = parseEnv([]byte(minimal), "cfg.toml", Overrides{Theme: "mono"}, fakeEnv(nil))
	if err != nil || c.Display.Theme != "mono" {
		t.Fatalf("--theme: %v", err)
	}

	// bad override values are errors located at the flag
	for _, tc := range []struct {
		ov   Overrides
		want string
	}{
		{Overrides{Repos: []string{"nope"}}, `--repo: "nope" is not "owner/name"`},
		{Overrides{Icons: "emoji"}, `--icons: "emoji" is not one of nerd, unicode, ascii`},
		{Overrides{Theme: "dark"}, `--theme: "dark" is not one of auto, mono`},
		{Overrides{Motion: "some"}, `--motion: "some" is not one of full, off`},
	} {
		_, err := parseEnv([]byte(minimal), "cfg.toml", tc.ov, fakeEnv(nil))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("override %+v: got %v, want %q", tc.ov, err, tc.want)
		}
	}
}

func TestMatchNight(t *testing.T) {
	mw := DefaultMaintenanceWindow()
	mw.ManifestTitle = "Window {night} 23:00 ET"
	cases := []struct {
		title string
		night string
		ok    bool
	}{
		{"Window 2026-10-21 23:00 ET", "2026-10-21", true},
		{"  Window 2026-10-21 23:00 ET\n", "2026-10-21", true},
		{"Window 2026-10-21 23:00 ET (rerun)", "", false},
		{"Re: Window 2026-10-21 23:00 ET", "", false},
		{"Window tonight 23:00 ET", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		night, ok := mw.MatchNight(c.title)
		if night != c.night || ok != c.ok {
			t.Errorf("MatchNight(%q) = %q,%v; want %q,%v", c.title, night, ok, c.night, c.ok)
		}
	}
	if got := mw.Title("2026-10-21"); got != "Window 2026-10-21 23:00 ET" {
		t.Errorf("Title = %q", got)
	}
	// a custom title with regexp metacharacters
	mw.ManifestTitle = "[window] {night} (23:00)"
	if night, ok := mw.MatchNight("[window] 2026-01-02 (23:00)"); !ok || night != "2026-01-02" {
		t.Errorf("custom title: %q %v", night, ok)
	}
	mw.ManifestTitle = "no placeholder"
	if _, ok := mw.MatchNight("no placeholder"); ok {
		t.Error("a title without {night} matches nothing")
	}
}

func TestIsBot(t *testing.T) {
	b := BotsConfig{Logins: []string{"github-actions", "dependabot", "infra-bot"}}
	for login, want := range map[string]bool{
		"github-actions": true, "GitHub-Actions": true, "github-actions[bot]": true,
		"dependabot-preview": true, "renovate[bot]": true, "infra-bot": true,
		"alice": false, "": false, "bot": false,
	} {
		if got := b.IsBot(login); got != want {
			t.Errorf("IsBot(%q) = %v", login, got)
		}
	}
}

func TestSuggest(t *testing.T) {
	cands := []string{"refresh_secs", "icons", "theme", "motion", "mascot"}
	cases := map[string]string{
		"refresh_sec": "refresh_secs", "Icons": "icons", "icon": "icons", "refresh-secs": "refresh_secs",
		"colour": "", "background_image": "", "them": "theme",
	}
	for in, want := range cases {
		if got := suggest(in, cands); got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
	if levenshtein("kitten", "sitting") != 3 || levenshtein("", "abc") != 3 || levenshtein("a", "a") != 0 {
		t.Error("levenshtein")
	}
}
