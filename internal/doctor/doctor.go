// Package doctor is `gh dillodash config check`: it validates the config,
// finds the gh login and token, probes what the token can read in every
// repo, and says which workflows are on, all without starting the TUI.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Client is what the report needs of GitHub: the API plus where the
// token came from.
type Client interface {
	ghapi.Client
	Host() string
	TokenSource() string
}

// Run builds the client from the gh login and prints the report; it
// returns an error when something is broken.
func Run(ctx context.Context, w io.Writer, cfg *config.Config, version string) error {
	fmt.Fprintf(w, "dillodash %s\n", version)
	fmt.Fprintf(w, "config: %s\n", cfg.Path)
	var src config.Source
	var pulled bool
	if raw, err := os.ReadFile(cfg.Path); err == nil {
		if src, pulled = config.ParseSource(raw); pulled {
			fmt.Fprintf(w, "  source: %s @ %s, pulled %s\n", src.Spec(), model.ShortSHA(src.SHA), ago(src.At))
		}
	}
	for _, warn := range cfg.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	fmt.Fprintf(w, "reviews: %s\n", joinRepos(cfg.Reviews.Repos))
	gh, err := ghapi.New(ghapi.Options{Host: cfg.GitHub.Host, Version: version, Timeout: 15 * time.Second})
	if err != nil {
		fmt.Fprintf(w, "auth: FAILED: %s\n", err)
		return ErrFailed
	}
	return Report(ctx, w, cfg, gh, src, pulled)
}

// ErrFailed is what Run returns for anything the report printed as
// FAILED, so the caller prints one summary line, not the detail twice.
var ErrFailed = errors.New("config check failed (see above)")

// BotHint is the second half of the bot-login message: how to run as
// yourself, which depends on where the token came from.
func BotHint(source string) string {
	if ghapi.TokenFromEnv(source) {
		return "unset " + source + " to use gh's own login, or export your own token in it"
	}
	return "gh auth switch (or GH_TOKEN=<your token>) to run as yourself"
}

// Report prints everything after the client exists: the token and the
// login, the source check, every repo's reach and the token's role in
// it, the budget and the workflows. Every review repo read-only fails
// the report: the dashboard would be watch-only in a way nobody chose. A login listed in [bots] fails the report: the dashboard
// would post reviews, comments and merges as the bot, and the audit
// trail of who approved is only meaningful when it is the human who
// decided.
func Report(ctx context.Context, w io.Writer, cfg *config.Config, gh Client, src config.Source, pulled bool) error {
	fmt.Fprintf(w, "host: %s\ntoken: from %s\n", gh.Host(), gh.TokenSource())
	if ghapi.TokenFromEnv(gh.TokenSource()) {
		fmt.Fprintf(w, "  note: %s in the environment overrides the gh login (gh auth status shows it); unset it to use gh's account\n", gh.TokenSource())
	}
	me, err := gh.Whoami(ctx)
	if err != nil {
		fmt.Fprintf(w, "login: FAILED: %s\n", err)
		return ErrFailed
	}
	var botErr error
	if cfg.Bots.IsBot(me) {
		botErr = ErrFailed
		fmt.Fprintf(w, "login: FAILED: %s is a bot login ([bots] logins, or the [bot] suffix); reviews, comments and merges would be signed by it — %s\n", me, BotHint(gh.TokenSource()))
	} else {
		fmt.Fprintf(w, "login: %s", me)
		if cfg.GitHub.Login != "" && !strings.EqualFold(cfg.GitHub.Login, me) {
			fmt.Fprintf(w, "  (config expects %s: gh auth switch)", cfg.GitHub.Login)
		}
		fmt.Fprintln(w)
	}
	if pulled {
		if _, sha, err := gh.FileContents(ctx, src.Repo, src.Path, ""); err != nil {
			fmt.Fprintf(w, "  source: could not check %s: %s\n", src.Spec(), err)
		} else if sha != src.SHA {
			fmt.Fprintf(w, "  source: OUTDATED — %s has a newer %s; run: gh dillodash config pull\n", src.Repo, src.Path)
		} else {
			fmt.Fprintln(w, "  source: current")
		}
	}
	failed := false
	listed, readOnly := 0, 0
	for _, repo := range cfg.Reviews.Repos {
		fmt.Fprintf(w, "repo %s:\n", repo)
		res, err := gh.ListOpen(ctx, []model.Repo{repo})
		if err != nil {
			fmt.Fprintf(w, "  list: FAILED: %s\n", err)
			failed = true
			continue
		}
		r := res[repo]
		if r.Err != nil {
			fmt.Fprintf(w, "  list: FAILED: %s\n", r.Err)
			failed = true
			continue
		}
		fmt.Fprintf(w, "  pull requests: %d open, issues: %d open\n", len(r.PRs), len(r.Issues))
		listed++
		switch {
		case r.Permission == "":
			fmt.Fprintln(w, "  write: not reported — fine-grained tokens report grants, not a role; a 403 names a missing one when it happens")
		case ghapi.CanWrite(r.Permission):
			fmt.Fprintf(w, "  write: yes (%s)\n", r.Permission)
		default:
			readOnly++
			fmt.Fprintf(w, "  write: NO (%s) — approve, comment, merge and deployment approval will 403\n", r.Permission)
		}
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  note: %s\n", n)
		}
		if _, err := gh.WorkflowRuns(ctx, repo, "0000000000000000000000000000000000000000"); err != nil {
			fmt.Fprintf(w, "  actions: not readable (%s) — the CI column falls back to '?' when the check rollup is hidden too\n", verbose(err))
		} else {
			fmt.Fprintln(w, "  actions: readable")
		}
	}
	if rate := gh.LastRate(); rate != nil && rate.Limit > 0 {
		fmt.Fprintf(w, "graphql budget: %d of %d points left (resets %s)\n", rate.Remaining, rate.Limit, rate.ResetAt.Local().Format("15:04"))
	}
	fmt.Fprintln(w, "workflows:")
	on := func(name string, enabled bool, detail string) {
		state := "off"
		if enabled {
			state = "on"
		}
		fmt.Fprintf(w, "  %-20s %s%s\n", name, state, detail)
	}
	on("terraform_approve", cfg.TerraformApprove != nil, "")
	if m := cfg.MaintenanceWindow; m != nil {
		on("maintenance_window", true, fmt.Sprintf("  manifests in %s, %s, window %s-%s", m.ManifestRepo, m.Timezone, m.Start, m.End))
	} else {
		on("maintenance_window", false, "")
	}
	if r := cfg.ReleasePR; r != nil {
		repos := "auto (ruleset " + r.Ruleset + " in " + r.Org + ")"
		if !r.Auto {
			repos = joinRepos(r.Repos)
		}
		on("release_pr", true, "  repos: "+repos)
	} else {
		on("release_pr", false, "")
	}
	if wr := cfg.WaitingRuns; wr != nil {
		on("waiting_runs", true, "  repos: "+joinRepos(wr.Repos))
	} else {
		on("waiting_runs", false, "")
	}
	if listed > 0 && readOnly == listed {
		failed = true
		fmt.Fprintln(w, "write: FAILED: every review repo is read-only to this token, so the dashboard could only watch — gh auth switch to an account with push, or ask for it")
	}
	fmt.Fprintln(w, "probed: the repository permission; not probed: fine-grained token grants (push without Pull requests: write still 403s on approve), Actions write for deployment approval, branch protection and rulesets — a 403 names the missing one when it happens")
	if botErr != nil || failed {
		return ErrFailed
	}
	return nil
}

func joinRepos(repos []model.Repo) string {
	parts := make([]string, len(repos))
	for i, r := range repos {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

// verbose is the long form of a GitHub error (method and URL) for the
// report, where detail helps.
func verbose(err error) string {
	if e, ok := ghapi.AsError(err); ok {
		return e.Verbose()
	}
	return err.Error()
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "at an unknown time"
	}
	return clock.Ago(t, clock.Now())
}
