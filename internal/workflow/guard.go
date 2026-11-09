package workflow

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// The membership answers of this session, per team and login: a team's
// members do not change under a running dashboard often enough to read
// them before every command, and a refused post costs a live release
// minutes. Errors are not remembered.
var (
	memberMu    sync.Mutex
	memberCache = map[string]ghapi.TeamMembership{}
)

// ResetMembershipCache forgets the session's membership answers (tests).
func ResetMembershipCache() {
	memberMu.Lock()
	memberCache = map[string]ghapi.TeamMembership{}
	memberMu.Unlock()
}

// PostGuard is the identity check before posting a command that a bot
// accepts only from an approver (what names the command, e.g.
// "/release-now"). It refuses (a gold notice, false) when the posting
// login is a bot login, or when [github] approvers_team is set and
// the login is not an active member of it: the bot would refuse the post
// anyway, and posting under the wrong identity costs a live release
// minutes. When the token cannot read the team, it warns and allows;
// with [github] approvers_team_strict it refuses instead.
// The refusal names the login and, when the token came from the
// environment, the variable to unset.
func PostGuard(app App, what string) bool {
	me := app.Me()
	cfg := app.Config()
	hint := tokenHint(app)
	if cfg != nil && cfg.Bots.IsBot(me) {
		app.Notice(fmt.Sprintf("%s not posted: %s is a bot login, and the bot accepts it only from an approver%s", what, me, hint))
		return false
	}
	if cfg == nil || cfg.GitHub.ApproversTeam == "" {
		return true
	}
	team := cfg.GitHub.ApproversTeam
	org, slug, _ := strings.Cut(team, "/")
	key := strings.ToLower(team + " " + me)
	memberMu.Lock()
	m, ok := memberCache[key]
	memberMu.Unlock()
	if !ok {
		var err error
		m, err = app.GH().TeamMember(app.Context(), org, slug, me)
		if err != nil {
			if cfg.GitHub.ApproversTeamStrict {
				app.Notice(fmt.Sprintf("%s not posted: couldn't check %s is in @%s (%s), and approvers_team_strict refuses an unchecked post", what, me, team, err))
				return false
			}
			app.Notice(fmt.Sprintf("couldn't check %s is in @%s (%s) — posting %s anyway", me, team, err, what))
			return true
		}
		memberMu.Lock()
		memberCache[key] = m
		memberMu.Unlock()
	}
	switch {
	case !m.Readable && cfg.GitHub.ApproversTeamStrict:
		app.Notice(fmt.Sprintf("%s not posted: this token can't read @%s (it needs the organization's Members read), and approvers_team_strict refuses an unchecked post", what, team))
		return false
	case !m.Readable:
		app.Notice(fmt.Sprintf("couldn't read @%s with this token (it needs the organization's Members read) — posting %s as %s unchecked", team, what, me))
		return true
	case !m.Member:
		app.Notice(fmt.Sprintf("%s not posted: %s is not in @%s, so the bot would refuse it%s", what, me, team, hint))
		return false
	}
	return true
}

// tokenHint names the environment variable the token came from, when it
// did: the usual way to post as the wrong login is a GH_TOKEN or
// GITHUB_TOKEN exported for something else in this shell.
func tokenHint(app App) string {
	src, ok := app.GH().(interface{ TokenSource() string })
	if !ok {
		return ""
	}
	s := src.TokenSource()
	if !ghapi.TokenFromEnv(s) {
		return "; gh auth switch (or GH_TOKEN=<your token>) to post as yourself"
	}
	return fmt.Sprintf("; the token comes from %s — unset it to post as gh's own login, or export your own token in it", s)
}

// WriteRefused says why a write to repo is refused, before any prompt:
// the repo is not in [github] allowed_repos. False means go on. The
// client refuses the write again underneath (ghapi.WithAllowedRepos);
// this only spares the person a confirm that cannot be sent.
func WriteRefused(app App, repo model.Repo, what string) bool {
	cfg := app.Config()
	if cfg == nil || cfg.GitHub.WriteAllowed(repo) {
		return false
	}
	app.Notice(fmt.Sprintf("%s refused: %s is not in [github] allowed_repos — this dashboard is read-only there", what, repo))
	return true
}

// HeadStill re-reads pr's head just before an approval posts and
// refuses (a gold notice, false) when it moved since the reviewer read
// it: the confirm can sit open while someone pushes, and an approval
// belongs to the code that was on screen. The review itself is pinned
// to the head shown (commit_id), so when the re-read fails it allows.
func HeadStill(app App, pr *model.PR, what string) bool {
	if pr == nil || pr.HeadRefOid == "" {
		return true
	}
	now, err := app.GH().PRDetail(app.Context(), pr.Repo, pr.Number)
	if err != nil || now == nil {
		return true
	}
	if model.ShaMoved(pr.HeadRefOid, now.HeadRefOid) {
		app.Notice(fmt.Sprintf("%s not posted: %s#%d moved from %s to %s since you read it — r reloads it, then look again",
			what, pr.Repo.Name, pr.Number, model.ShortSHA(pr.HeadRefOid), model.ShortSHA(now.HeadRefOid)))
		app.Refresh(true)
		return false
	}
	return true
}

// Post posts body on repo#number as a comment and returns its id. When
// the post fails ambiguously (ghapi.Ambiguous: it may have landed), it
// re-reads the thread for a comment by this login with exactly body,
// posted in the last ten minutes: found, the post counts as done (err
// nil). unsure is true when the re-read itself failed, so the caller
// says "may have posted" and refreshes instead of "NOT posted", which
// would invite a second post of the same command.
func Post(app App, repo model.Repo, number int, body string) (id int64, unsure bool, err error) {
	id, err = app.GH().PostComment(app.Context(), repo, number, body)
	if err == nil || !ghapi.Ambiguous(err) {
		return id, false, err
	}
	comments, rerr := app.GH().PRComments(app.Context(), repo, number)
	if rerr != nil {
		is, ierr := app.GH().IssueDetail(app.Context(), repo, number)
		if ierr != nil || is == nil {
			return 0, true, err
		}
		comments = is.Comments.Nodes
	}
	since := app.Now().Add(-10 * time.Minute)
	want := strings.TrimSpace(body)
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if strings.EqualFold(c.Author.Login, app.Me()) && strings.TrimSpace(c.Body) == want && !c.CreatedAt.Before(since) {
			return c.ID, false, nil
		}
	}
	return 0, false, err
}

// PostFailed is the status line for a failed Post of what: "NOT posted"
// when it surely did not land, "may have posted" (and a refresh, so the
// thread shows which) when the re-read could not tell.
func PostFailed(app App, lead, what string, unsure bool, err error) {
	if unsure {
		app.Fail(fmt.Sprintf("%s%s may have posted (%s) — refreshing; check the thread before posting it again", lead, what, err))
		app.Refresh(true)
		return
	}
	app.Fail(fmt.Sprintf("%s%s NOT posted: %s", lead, what, err))
}
