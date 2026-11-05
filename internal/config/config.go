// Package config reads dillodash's TOML config: which repos to review,
// how to draw the dashboard, which logins are bots, and the four optional
// workflows (terraform approve, maintenance window, release PRs, waiting
// runs). Auth is
// the gh CLI's business, so there is no token or user section.
//
// Load finds and parses a file; Parse is the pure core the tests drive.
// Problems come in two kinds: errors (a wrong type, an invalid choice, a
// missing required key) stop the load, while warnings (unknown keys with a
// "did you mean", a clamped refresh, a missing enabled = ...) are collected
// in Config.Warnings for the app to show. FindPath implements the search
// order and spots a legacy INI config, which MigrateINI converts.
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
	Update   UpdateConfig
	Merge    MergeConfig // [merge]
	Agent    AgentConfig // [agent]
	Warnings []string

	TerraformApprove  *TerraformApproveConfig
	MaintenanceWindow *MaintenanceWindowConfig
	ReleasePR         *ReleasePRConfig
	WaitingRuns       *WaitingRunsConfig
	// Dispatches are the [[workflows.dispatch]] entries X may run; none
	// means X is off.
	Dispatches []DispatchConfig
}

// GitHubConfig is [github].
type GitHubConfig struct {
	Host  string // "github.com" or a GHES hostname
	Login string // expected login; "" = whoever gh is signed in as
	// ApproversTeam is "org/team": before posting a command a bot
	// validates by its author, dillodash checks the login is a member
	// ("" = no check beyond the bot rule)
	ApproversTeam string
	// ApproversTeamStrict refuses the command when the team's membership
	// cannot be read (a token without the organization's Members read,
	// or an error), instead of warning and posting unchecked.
	ApproversTeamStrict bool
	// AllowedRepos, when set, are the only repos dillodash writes to:
	// every comment, review, merge, command, label, assignment, state
	// change, dispatch and run action on any other repo is refused, even
	// one opened with ':'. Empty = no restriction.
	AllowedRepos []model.Repo
}

// WriteAllowed reports whether dillodash may write to repo: always when
// allowed_repos is unset, else only to a listed repo (case-insensitive).
func (g GitHubConfig) WriteAllowed(repo model.Repo) bool {
	if len(g.AllowedRepos) == 0 {
		return true
	}
	for _, r := range g.AllowedRepos {
		if strings.EqualFold(r.String(), repo.String()) {
			return true
		}
	}
	return false
}

// AgentConfig is [agent]: the agent issues are handed to. An agent that
// cannot be @-mentioned (a GitHub App's bot) watches a label instead.
type AgentConfig struct {
	// HandoffLabel is the label H adds to hand the open item to the
	// agent ("" = H is off).
	HandoffLabel string
	// HandoffComment, when set, is posted with the label (the agent's
	// first instruction, a note for the humans watching).
	HandoffComment string
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
	// Clock draws the header clock: UTC, then the same instant in
	// ClockTimezone (labelled ClockTZLabel, or the zone's abbreviation).
	Clock         bool
	ClockTimezone string // IANA zone, validated at parse
	ClockTZLabel  string // "" = derived (EST/EDT)
	// Room draws the dashboard as seen through a window from a room
	// while the release window is open (W steps out of it and back).
	Room bool
	// Profile is a short badge drawn in the header ("" = none): which
	// config this dashboard runs, when one person runs more than one.
	Profile string
}

// ClockLocal is the header clock's second zone with its label (both
// validated at parse).
func (d DisplayConfig) ClockLocal() *clock.Local {
	l, _ := clock.NewLocal(d.ClockTimezone, d.ClockTZLabel)
	return l
}

// UpdateConfig is [update]: how dillodash keeps itself current.
type UpdateConfig struct {
	// Auto installs a newer release in the background (verified) and
	// offers a restart; false only warns, for a pinned host.
	Auto bool
	// Channel is which releases count: "stable" is the only one, the
	// published releases (never a draft or a prerelease).
	Channel string
}

// MinRefreshSecs is the floor for refresh_secs; lower values are clamped
// with a warning.
const MinRefreshSecs = 5

// MinProgressPollSecs is the floor of release_pr.progress_poll_secs
// when it is on: each poll lists Actions runs and reads a run's jobs.
const MinProgressPollSecs = 10

// BotsConfig is [bots].
type BotsConfig struct {
	Logins []string // lower-cased, deduped
}

// IsBot reports whether a login is a bot: it ends in "[bot]", or it
// equals a configured bot login, with or without that suffix
// (case-insensitive). The match is exact, not a prefix: a configured
// "al" must not make "alice" a bot.
func (b BotsConfig) IsBot(login string) bool {
	l := strings.ToLower(strings.TrimSpace(login))
	if l == "" {
		return false
	}
	if strings.HasSuffix(l, "[bot]") {
		return true
	}
	for _, bot := range b.Logins {
		if l == bot {
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
	AppliedLabel   string // the conductor's label on a verified release
	FailedLabel    string // and on a failed one
	PendingLabel   string // and on one merged whose deploy waits on an environment approval
	ManifestLabel  string
	ManifestTitle  string // contains {night}
	ConductorBot   string
	Team           string // "org/team", hints only
	AttendCommand  string
	ReleaseCommand string
	CancelCommand  string

	// The night's later rules, as the conductor runs them.
	//
	// MissedFreezeMarker is the text a conductor comment carries when the
	// run found the manifest still open (the freeze never ran): nothing
	// fires that night. MissedFreezeGraceMins is how long after the
	// cutoff an open manifest counts as a missed freeze without that
	// comment (0 = only the comment). MissedFreezeHint is the recovery
	// the night shows ("" = a generic one).
	MissedFreezeMarker    string
	MissedFreezeGraceMins int
	MissedFreezeHint      string
	// LateSkip is when the run stops starting new items; Closer is when
	// the conductor closes the night. Both are wall clock in the window's
	// zone, nil = not set (not shown).
	LateSkip *clock.HM
	Closer   *clock.HM

	// Reserved is reserved_from / reserved_to / reserved_stacks: the
	// span the apply dispatcher refuses an immediate apply to a reserved
	// stack without the outside-window flag (nil = none).
	Reserved *ReservedWindow
}

// MissedHint is the recovery hint of a missed freeze.
func (m *MaintenanceWindowConfig) MissedHint() string {
	if strings.TrimSpace(m.MissedFreezeHint) != "" {
		return strings.TrimSpace(m.MissedFreezeHint)
	}
	return "nothing runs on this night; whoever runs the conductor can start it by hand"
}

// Local is the window's zone with its label (both validated at parse).
func (m *MaintenanceWindowConfig) Local() *clock.Local {
	l, _ := clock.NewLocal(m.Timezone, m.TZLabel)
	return l
}

// Title renders manifest_title for a night ("2026-10-21"): {night} is
// the night, {hhmm} the window's start.
func (m *MaintenanceWindowConfig) Title(night string) string {
	return strings.NewReplacer(nightPlaceholder, night, hhmmPlaceholder, m.Start.String()).Replace(m.ManifestTitle)
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

const (
	nightPlaceholder = "{night}"
	hhmmPlaceholder  = "{hhmm}"
)

// wallRE is a wall clock written into a title ("22:00").
var wallRE = regexp.MustCompile(`\b\d{2}:\d{2}\b`)

// titlePart quotes a literal part of manifest_title for the match: a
// {hhmm} placeholder, and any wall clock written literally, match any
// HH:MM. A conductor that titles its manifests with the start time
// keeps matching across a change of that time (a manifest opened under
// the old title is still read).
func titlePart(s string) string {
	var b strings.Builder
	for i, lit := range strings.Split(s, hhmmPlaceholder) {
		if i > 0 {
			b.WriteString(`\d{2}:\d{2}`)
		}
		last := 0
		for _, loc := range wallRE.FindAllStringIndex(lit, -1) {
			b.WriteString(regexp.QuoteMeta(lit[last:loc[0]]))
			b.WriteString(`\d{2}:\d{2}`)
			last = loc[1]
		}
		b.WriteString(regexp.QuoteMeta(lit[last:]))
	}
	return b.String()
}

func nightRE(pattern string) *regexp.Regexp {
	pre, post, ok := strings.Cut(pattern, nightPlaceholder)
	if !ok {
		return nil
	}
	// a second {night} in the suffix is matched literally; one capture is enough
	return regexp.MustCompile(`^` + titlePart(strings.TrimSpace(pre)) + `\s*(\d{4}-\d{2}-\d{2})\s*` + titlePart(strings.TrimSpace(post)) + `$`)
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
	ReleaseNowDispatch      bool   // fire a repository_dispatch on the manifest repo after posting the command
	ReleaseNowDispatchEvent string // its event_type
	ApplyWorkflow           string // the manifest repo's workflow file whose dispatch runs carry a release ("" = don't look)
	ApplyRunMatch           string // the run title substring naming a release; placeholders {repo} {number}
	ProgressPollSecs        int    // how often a release in flight is polled (0 = no live progress)
	ProgressTimeoutSecs     int    // give up following a release after this long
	ApprovalQueuesForWindow bool
	MergeMethod             string // merge | squash | rebase
	MergedKeepSecs          int
	DeployStuckSecs         int
	DeployRefreshSecs       int
	DeployApprovalComment   string // placeholders {repo} {number} {sha}
	// LaneOrder is the release repos' order on a manual night (short
	// names): after one is released, the next one's open release is
	// offered; empty = no order
	LaneOrder []string
	// ApplyBudgetMins is how long a release in flight is expected to
	// take, for the countdown on its row (0 = no countdown)
	ApplyBudgetMins int
}

// DeployComment renders deploy_approval_comment for a merged release.
func (r *ReleasePRConfig) DeployComment(repo model.Repo, number int, sha string) string {
	rep := strings.NewReplacer("{repo}", repo.String(), "{number}", strconv.Itoa(number), "{sha}", sha)
	return rep.Replace(r.DeployApprovalComment)
}

// WaitingRunsConfig is [workflows.waiting_runs].
type WaitingRunsConfig struct {
	Repos           []model.Repo // the review repos when unset
	ApprovalComment string       // placeholders {repo} {workflow} {run}
}

// Comment renders approval_comment for a run: the repo as owner/name,
// the workflow's name and the run id.
func (w *WaitingRunsConfig) Comment(repo model.Repo, workflow string, run int64) string {
	rep := strings.NewReplacer("{repo}", repo.String(), "{workflow}", workflow, "{run}", strconv.FormatInt(run, 10))
	return rep.Replace(w.ApprovalComment)
}

// DefaultWaitingRuns is [workflows.waiting_runs] with every default;
// Repos stays nil (= the review repos).
func DefaultWaitingRuns() *WaitingRunsConfig {
	return &WaitingRunsConfig{ApprovalComment: "dillodash: approved {workflow} run {run} in {repo}"}
}

// Default is a Config with every default and no repos. Workflows are off
// (nil); their defaults are DefaultTerraformApprove, DefaultMaintenanceWindow,
// DefaultReleasePR and DefaultWaitingRuns.
func Default() *Config {
	return &Config{
		GitHub:  GitHubConfig{}, // Host "": $GH_HOST, then gh's default host, then github.com
		Reviews: ReviewsConfig{CIRecheckSecs: 900},
		Display: DisplayConfig{RefreshSecs: 300, Icons: "nerd", Theme: "auto", Motion: "full", Mascot: "sprite", Clock: true, ClockTimezone: "America/New_York", Room: true},
		Bots:    BotsConfig{Logins: []string{"github-actions", "dependabot"}},
		Update:  UpdateConfig{Auto: true, Channel: "stable"},
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
	// an example window, not anyone's real one: set yours in the config
	return &MaintenanceWindowConfig{
		Timezone:       "UTC",
		Cutoff:         clock.HM{H: 12, M: 0},
		Start:          clock.HM{H: 16, M: 0},
		ReleaseFrom:    clock.HM{H: 15, M: 55},
		End:            clock.HM{H: 17, M: 0},
		QueuedLabel:    "window:queued",
		ScheduledLabel: "window:scheduled",
		RolledLabel:    "window:rolled",
		AppliedLabel:   "window:applied",
		FailedLabel:    "window:failed",
		PendingLabel:   "window:pending",
		ManifestLabel:  "window-manifest",
		ManifestTitle:  "Window {night}",
		ConductorBot:   "github-actions",
		AttendCommand:  "/attend",
		ReleaseCommand: "/release",
		CancelCommand:  "/window-cancel",

		MissedFreezeMarker:    "MISSED FREEZE",
		MissedFreezeGraceMins: 30,
	}
}

// DefaultReleaseCache is the cache default before ~, $XDG_CACHE_HOME and
// {org} are resolved.
const DefaultReleaseCache = "~/.cache/dillodash/release-repos-{org}.json"

// DefaultReleasePR is [workflows.release_pr] with every default; Org stays
// "" (= the owner shared by the review repos) and Cache unexpanded.
func DefaultReleasePR() *ReleasePRConfig {
	return &ReleasePRConfig{
		Auto:                    true,
		Ruleset:                 "release",
		DiscoverTTLSecs:         86400,
		Cache:                   DefaultReleaseCache,
		ProductionBranch:        "main",
		StagingBranch:           "stage",
		ReleasePrefix:           "release/",
		HotfixPrefix:            "hotfix/",
		GoTagPrefix:             "go/",
		HoldTagPrefix:           "hold/",
		ListOtherPRs:            true,
		ReleaseNowCommand:       "/release-now",
		ReleaseNowMinReason:     20,
		ReleaseNowDispatch:      true,
		ReleaseNowDispatchEvent: "release-now",
		ApplyWorkflow:           "apply.yml",
		ApplyRunMatch:           "rel:{repo}#{number}",
		ProgressPollSecs:        30,
		ProgressTimeoutSecs:     5400,
		MergeMethod:             "merge",
		MergedKeepSecs:          43200,
		DeployStuckSecs:         2700,
		DeployRefreshSecs:       30,
		DeployApprovalComment:   "dillodash: release {repo}#{number} merged as {sha}",
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
