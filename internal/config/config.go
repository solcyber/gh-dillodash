// Package config reads dillodash's TOML config: which repos to review,
// how to draw the dashboard, which logins are bots, and the three optional
// workflows (terraform approve, maintenance window, release PRs). Auth is
// the gh CLI's business, so there is no token or user section.
//
// Load finds and parses a file; Parse is the pure core the tests drive.
// Problems come in two kinds: errors (a wrong type, an invalid choice, a
// missing required key) stop the load, while warnings (unknown keys with a
// "did you mean", a clamped refresh, a missing enabled = ...) are collected
// in Config.Warnings for the app to show. FindPath implements the search
// order and spots a legacy pr-dash config.ini, which MigrateINI converts.
package config

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Config is a loaded, validated config. A nil workflow pointer means that
// workflow is off.
type Config struct {
	Path     string
	GitHub   GitHubConfig
	Reviews  ReviewsConfig
	Display  DisplayConfig
	Bots     BotsConfig
	Warnings []string

	TerraformApprove  *TerraformApproveConfig
	MaintenanceWindow *MaintenanceWindowConfig
	ReleasePR         *ReleasePRConfig
}

// GitHubConfig is [github].
type GitHubConfig struct {
	Host  string // "github.com" or a GHES hostname
	Login string // expected login; "" = whoever gh is signed in as
}

// ReviewsConfig is [reviews].
type ReviewsConfig struct {
	Repos         []model.Repo // >= 1, deduped, in config order
	CIRecheckSecs int
}

// Multi reports whether more than one repo is reviewed (refs then carry
// the repo name).
func (r ReviewsConfig) Multi() bool { return len(r.Repos) > 1 }

// DisplayConfig is [display].
type DisplayConfig struct {
	RefreshSecs int    // >= MinRefreshSecs
	Icons       string // nerd | unicode | ascii
	Theme       string // auto | mono
	Motion      string // full | off
	Mascot      string // sprite | ascii | none
}

// MinRefreshSecs is the floor for refresh_secs; lower values are clamped
// with a warning.
const MinRefreshSecs = 5

// BotsConfig is [bots].
type BotsConfig struct {
	Logins []string // lower-cased, deduped
}

// IsBot reports whether a login is a bot: it ends in "[bot]", or it
// equals or starts with a configured bot login (case-insensitive), so
// "dependabot" also covers "dependabot[bot]" and "dependabot-preview".
func (b BotsConfig) IsBot(login string) bool {
	l := strings.ToLower(strings.TrimSpace(login))
	if l == "" {
		return false
	}
	if strings.HasSuffix(l, "[bot]") {
		return true
	}
	for _, bot := range b.Logins {
		if strings.HasPrefix(l, bot) {
			return true
		}
	}
	return false
}

// CeremonyWindow is the terraform ceremony's nightly slot: a same-day
// wall-clock span in a zone.
type CeremonyWindow struct {
	Timezone string
	From, To clock.HM
}

// Local is the window's zone (the timezone was validated at parse time).
func (w *CeremonyWindow) Local() *clock.Local {
	l, _ := clock.NewLocal(w.Timezone, "")
	return l
}

// TerraformApproveConfig is [workflows.terraform_approve].
type TerraformApproveConfig struct {
	Repos                  []model.Repo // the review repos when unset
	DigestBot              string
	PlanWorkflow           string
	GateContext            string
	GateDonePrefixes       []string
	ApproveCommand         string
	ConfirmDestroysFlag    string
	OutsideWindowFlag      string
	OutsideWindowMinReason int
	CeremonyWindow         *CeremonyWindow // nil = no window restriction
}

// MaintenanceWindowConfig is [workflows.maintenance_window].
type MaintenanceWindowConfig struct {
	ManifestRepo   model.Repo // the first review repo when unset
	Timezone       string
	TZLabel        string // "" = derived (EST/EDT)
	Cutoff         clock.HM
	Start          clock.HM
	ReleaseFrom    clock.HM
	End            clock.HM
	QueuedLabel    string
	ScheduledLabel string
	RolledLabel    string
	ManifestLabel  string
	ManifestTitle  string // contains {night}
	ConductorBot   string
	Team           string // "org/team", hints only
	AttendCommand  string
	ReleaseCommand string
	CancelCommand  string
}

// Local is the window's zone with its label (both validated at parse).
func (m *MaintenanceWindowConfig) Local() *clock.Local {
	l, _ := clock.NewLocal(m.Timezone, m.TZLabel)
	return l
}

// Title renders manifest_title for a night ("2026-10-21").
func (m *MaintenanceWindowConfig) Title(night string) string {
	return strings.ReplaceAll(m.ManifestTitle, "{night}", night)
}

// MatchNight reads the night (YYYY-MM-DD) out of a manifest issue title
// shaped like manifest_title; ok is false when the title does not match.
func (m *MaintenanceWindowConfig) MatchNight(title string) (string, bool) {
	re := nightRE(m.ManifestTitle)
	if re == nil {
		return "", false
	}
	sub := re.FindStringSubmatch(strings.TrimSpace(title))
	if sub == nil {
		return "", false
	}
	return sub[1], true
}

const nightPlaceholder = "{night}"

func nightRE(pattern string) *regexp.Regexp {
	pre, post, ok := strings.Cut(pattern, nightPlaceholder)
	if !ok {
		return nil
	}
	// a second {night} in the suffix is matched literally; one capture is enough
	return regexp.MustCompile(`^` + regexp.QuoteMeta(strings.TrimSpace(pre)) + `\s*(\d{4}-\d{2}-\d{2})\s*` + regexp.QuoteMeta(strings.TrimSpace(post)) + `$`)
}

// ReleasePRConfig is [workflows.release_pr].
type ReleasePRConfig struct {
	Auto                    bool         // repos = "auto": discover by ruleset
	Repos                   []model.Repo // explicit list when !Auto
	Org                     string
	Ruleset                 string
	DiscoverTTLSecs         int
	Cache                   string // expanded: ~ and {org} resolved
	ProductionBranch        string
	StagingBranch           string
	ReleasePrefix           string
	HotfixPrefix            string
	GoTagPrefix             string
	HoldTagPrefix           string
	ListOtherPRs            bool
	ReleaseNowCommand       string
	ReleaseNowMinReason     int
	ApprovalQueuesForWindow bool
	MergeMethod             string // merge | squash | rebase
	MergedKeepSecs          int
	DeployStuckSecs         int
	DeployRefreshSecs       int
	DeployApprovalComment   string // placeholders {repo} {number} {sha}
}

// DeployComment renders deploy_approval_comment for a merged release.
func (r *ReleasePRConfig) DeployComment(repo model.Repo, number int, sha string) string {
	rep := strings.NewReplacer("{repo}", repo.String(), "{number}", itoa(number), "{sha}", sha)
	return rep.Replace(r.DeployApprovalComment)
}

// Default is a Config with every default and no repos. Workflows are off
// (nil); their defaults are DefaultTerraformApprove, DefaultMaintenanceWindow
// and DefaultReleasePR.
func Default() *Config {
	return &Config{
		GitHub:  GitHubConfig{Host: "github.com"},
		Reviews: ReviewsConfig{CIRecheckSecs: 900},
		Display: DisplayConfig{RefreshSecs: 300, Icons: "nerd", Theme: "auto", Motion: "full", Mascot: "sprite"},
		Bots:    BotsConfig{Logins: []string{"github-actions", "dependabot"}},
	}
}

// DefaultTerraformApprove is [workflows.terraform_approve] with every
// default; Repos stays nil (= the review repos).
func DefaultTerraformApprove() *TerraformApproveConfig {
	return &TerraformApproveConfig{
		DigestBot:              "github-actions",
		PlanWorkflow:           "plan.yml",
		GateContext:            "terraform/apply-gate",
		GateDonePrefixes:       []string{"applied:", "ceremony executed:"},
		ApproveCommand:         "/approve",
		ConfirmDestroysFlag:    "--confirm-destroys",
		OutsideWindowFlag:      "--outside-window",
		OutsideWindowMinReason: 20,
	}
}

// DefaultMaintenanceWindow is [workflows.maintenance_window] with every
// default; ManifestRepo stays zero (= the first review repo).
func DefaultMaintenanceWindow() *MaintenanceWindowConfig {
	return &MaintenanceWindowConfig{
		Timezone:       "America/New_York",
		Cutoff:         clock.HM{H: 21, M: 0},
		Start:          clock.HM{H: 23, M: 0},
		ReleaseFrom:    clock.HM{H: 22, M: 59},
		End:            clock.HM{H: 23, M: 30},
		QueuedLabel:    "window:queued",
		ScheduledLabel: "window:scheduled",
		RolledLabel:    "window:rolled",
		ManifestLabel:  "window-manifest",
		ManifestTitle:  "Window {night}",
		ConductorBot:   "github-actions",
		AttendCommand:  "/attend",
		ReleaseCommand: "/release",
		CancelCommand:  "/window-cancel",
	}
}

// DefaultReleaseCache is the cache default before ~, $XDG_CACHE_HOME and
// {org} are resolved.
const DefaultReleaseCache = "~/.cache/dillodash/release-repos-{org}.json"

// DefaultReleasePR is [workflows.release_pr] with every default; Org stays
// "" (= the owner shared by the review repos) and Cache unexpanded.
func DefaultReleasePR() *ReleasePRConfig {
	return &ReleasePRConfig{
		Auto:                  true,
		Ruleset:               "release",
		DiscoverTTLSecs:       86400,
		Cache:                 DefaultReleaseCache,
		ProductionBranch:      "main",
		StagingBranch:         "stage",
		ReleasePrefix:         "release/",
		HotfixPrefix:          "hotfix/",
		GoTagPrefix:           "go/",
		HoldTagPrefix:         "hold/",
		ListOtherPRs:          true,
		ReleaseNowCommand:     "/release-now",
		ReleaseNowMinReason:   20,
		MergeMethod:           "merge",
		MergedKeepSecs:        43200,
		DeployStuckSecs:       2700,
		DeployRefreshSecs:     30,
		DeployApprovalComment: "dillodash: release {repo}#{number} merged as {sha}",
	}
}

// Overrides are the command-line flags that beat the file.
type Overrides struct {
	Repos       []string // --repo (repeatable): replaces [reviews] repos
	RefreshSecs *int     // --refresh
	Icons       string   // --icons
	Theme       string   // --theme
	Motion      string   // --motion
	Mono        bool     // --mono: theme = "mono"
}

// Error is one config problem: "<path>: [section] key: why". Path and
// Where may be empty, in which case they are left out.
type Error struct {
	Path  string
	Where string // "[section] key", "[section]" or a flag like "--repo"
	Why   string
}

func (e *Error) Error() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{e.Path, e.Where, e.Why} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ": ")
}

func itoa(n int) string { return strconv.Itoa(n) }
