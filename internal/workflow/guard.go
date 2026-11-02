package workflow

import (
	"fmt"
	"strings"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
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
// minutes. When the token cannot read the team, it warns and allows.
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
			app.Notice(fmt.Sprintf("couldn't check %s is in @%s (%s) — posting %s anyway", me, team, err, what))
			return true
		}
		memberMu.Lock()
		memberCache[key] = m
		memberMu.Unlock()
	}
	switch {
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
