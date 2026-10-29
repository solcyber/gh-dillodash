package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/external"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Actions run on the action goroutine with a lockedApp; anything that
// touches model state goes through app or an explicit lock. The fields
// read bare here (gh, wf, cfg, ctx, me, multi, repos) never change after
// New.

// mergeable are the GitHub mergeStateStatus values a merge may try
// (UNKNOWN: GitHub has not computed it yet; the merge call itself still
// refuses a blocked PR).
var mergeable = map[string]bool{"CLEAN": true, "HAS_HOOKS": true, "UNSTABLE": true, "UNKNOWN": true, "": true}

// openDetail fetches an item and shows it.
func (m *Model) openDetail(app *lockedApp, repo model.Repo, number int, kind string, listed *model.PR) {
	app.Flash(fmt.Sprintf("opening %s...", m.where(repo, number)))
	started := fx.Now()
	var detail any
	var err error
	if kind == "issue" {
		detail, err = m.gh.IssueDetail(m.ctx, repo, number)
		if i, ok := detail.(*model.Issue); ok && i != nil {
			i.Repo = repo
		}
	} else {
		detail, err = m.fetchPRDetail(m.ctx, repo, number, listed)
		kind = "pr"
	}
	if err != nil {
		app.Fail(err.Error())
		return
	}
	m.mu.Lock()
	m.showDetail(detail, kind, started, listed)
	m.mu.Unlock()
}

// showDetail switches to the detail view (lock held).
func (m *Model) showDetail(detail any, kind string, started float64, listed *model.PR) {
	if m.status != "" && m.statusKind == "spin" {
		m.status = ""
	}
	m.detailKind = kind
	m.detail = detail
	m.detailListed = listed
	m.detailLoadedAt = started
	m.detailVer++
	m.view = ViewDetail
	m.scroll = 0
	m.searchQ = ""
	m.raw = false             // rendered markdown, whatever the last PR was shown as
	delete(m.glide, "detail") // a fresh page starts at the top, no glide from the old offset
	m.errText = ""
}

// doApprove is the 'a' key: a workflow may take it over; otherwise a
// plain GitHub review approval after a confirmation.
func (m *Model) doApprove(app *lockedApp) {
	d := app.Detail()
	pr, ok := d.(*model.PR)
	if !ok {
		if i, ok := d.(*model.Issue); ok {
			app.Notice(fmt.Sprintf("%s is an issue — nothing to approve; c comments, t tags someone", m.where(m.repoOf(i), i.Number)))
		}
		return
	}
	where := m.where(m.repoOf(pr), pr.Number)
	state := pr.State
	if state == "" {
		state = "OPEN"
	}
	if state != "OPEN" {
		// merged/closed while you were reading: a review is meaningless
		app.Notice(fmt.Sprintf("%s is %s — nothing to approve", where, strings.ToLower(state)))
		return
	}
	if pr.IsDraft {
		app.Notice(fmt.Sprintf("%s is a draft — GitHub refuses reviews on drafts", where))
		return
	}
	if strings.EqualFold(pr.Author.Login, m.me) {
		app.Notice(fmt.Sprintf("you authored %s — a self-approval counts for nothing", where))
		return
	}
	if app.IApproved(pr) {
		app.Notice(fmt.Sprintf("%s is already approved by you for this head — a second review changes nothing", where))
		return
	}
	if m.wf.Approve(app, pr, app.DetailListed()) {
		return
	}
	if !app.Confirm(fmt.Sprintf("submit a GitHub review approval of %s as %s?", where, m.me)) {
		app.Notice("approve cancelled")
		return
	}
	if err := m.gh.Approve(m.ctx, m.repoOf(pr), pr.Number, ""); err != nil {
		app.Fail(fmt.Sprintf("review approval of %s failed: %s%s", where, err, deploys.TokenHint(err, "Pull requests", m.repoOf(pr))))
		return
	}
	app.MarkApproved(pr)
	app.OK(fmt.Sprintf("review-approved %s as %s", where, m.me))
	app.Celebrate(false)
	app.Refresh(true)
}

// doComment posts a comment on the open item: inline (c) or in $EDITOR
// (C), optionally seeded with a mention (t).
func (m *Model) doComment(app *lockedApp, useEditor bool, seed string) {
	d := app.Detail()
	var repo model.Repo
	var number int
	switch v := d.(type) {
	case *model.PR:
		repo, number = m.repoOf(v), v.Number
	case *model.Issue:
		repo, number = m.repoOf(v), v.Number
	default:
		return
	}
	where := m.where(repo, number)
	var body string
	if useEditor || seed != "" {
		argv, err := external.EditorCommand()
		if err != nil {
			app.Fail("couldn't start the editor: " + err.Error())
			return
		}
		txt, ok, err := app.EditInEditor(seed)
		if err != nil {
			app.Fail(fmt.Sprintf("couldn't start the editor (%s): %s", argv[0], err))
			return
		}
		if !ok {
			app.Notice("comment cancelled (editor exited non-zero)")
			return
		}
		if seed != "" && strings.TrimSpace(txt) == strings.TrimSpace(seed) {
			// quitting the editor on the bare "@login" must not post just
			// the mention
			app.Notice(fmt.Sprintf("comment cancelled (nothing added to %s)", strings.TrimSpace(seed)))
			return
		}
		body = txt
	} else {
		t, ok := app.Prompt(fmt.Sprintf("comment on %s (Enter posts, Esc cancels):", where), workflow.PromptOpts{})
		if !ok {
			app.Notice("comment cancelled")
			return
		}
		body = t
	}
	if strings.TrimSpace(body) == "" {
		app.Notice("comment cancelled (empty)")
		return
	}
	// one endpoint for both kinds: the issue-comment API is the
	// conversation thread for issues AND pull requests
	if err := m.gh.Comment(m.ctx, repo, number, body); err != nil {
		app.Fail("comment not posted: " + err.Error())
		return
	}
	app.OK("commented on " + where)
	app.Celebrate(false)
	app.Refresh(true)
}

// doTag: pick from the repo's assignable users, then compose in $EDITOR
// seeded with the @mention.
func (m *Model) doTag(app *lockedApp) {
	d := app.Detail()
	var repo model.Repo
	switch v := d.(type) {
	case *model.PR:
		repo = m.repoOf(v)
	case *model.Issue:
		repo = m.repoOf(v)
	default:
		return
	}
	m.mu.Lock()
	users, cached := m.users[repo]
	m.mu.Unlock()
	if !cached {
		u, err := m.gh.AssignableUsers(m.ctx, repo)
		if err != nil {
			app.Fail(fmt.Sprintf("couldn't list who is assignable in %s: %s", repo, err))
			return
		}
		m.mu.Lock()
		m.users[repo] = u
		m.mu.Unlock()
		users = u
	}
	var others []model.Actor
	for _, u := range users {
		if !strings.EqualFold(u.Login, m.me) {
			others = append(others, u)
		}
	}
	if len(others) == 0 {
		app.Notice("no other assignable users found in " + repo.String())
		return
	}
	labels := make([]string, len(others))
	for i, u := range others {
		labels[i] = fmt.Sprintf("@%-20s %s", u.Login, u.Name)
	}
	i := app.Pick("tag whom? ($EDITOR opens with the @mention; quit it unchanged to cancel)", labels)
	if i < 0 {
		app.Notice("tag cancelled")
		return
	}
	m.doComment(app, true, "@"+others[i].Login+" ")
}

// mergeMethod is the merge method M uses.
func (m *Model) mergeMethod() string {
	if m.cfg.ReleasePR != nil && m.cfg.ReleasePR.MergeMethod != "" {
		return m.cfg.ReleasePR.MergeMethod
	}
	return "merge"
}

// doMerge is the M key: merge the open, approved PR, naming its head sha
// so a branch that moved since is refused by GitHub. A workflow may
// refuse (MergeBlock) or run its own confirmation (MergeConfirm).
func (m *Model) doMerge(app *lockedApp) {
	pr, ok := app.Detail().(*model.PR)
	if !ok {
		app.Notice("M merges a PR; open one first")
		return
	}
	repo := m.repoOf(pr)
	where := m.where(repo, pr.Number)
	if why := m.wf.MergeBlock(pr, m.me); why != "" {
		app.Notice(where + ": " + why)
		return
	}
	switch {
	case pr.State == "MERGED":
		app.Notice(where + " is already merged")
		return
	case pr.State != "" && pr.State != "OPEN":
		app.Notice(where + " is " + strings.ToLower(pr.State))
		return
	case pr.IsDraft:
		app.Notice(where + " is a draft")
		return
	case pr.ReviewDecision != "APPROVED" && !app.LocalApproved(pr):
		app.Notice(where + " is not approved yet (a approves)")
		return
	case !mergeable[pr.MergeStateStatus]:
		app.Notice(fmt.Sprintf("%s is not mergeable: GitHub says %s", where, strings.ToLower(pr.MergeStateStatus)))
		return
	}
	head := pr.HeadRefOid
	base := pr.BaseRefName
	if base == "" {
		base = "the base branch"
	}
	what := fmt.Sprintf("merge %s (%s -> %s at %s) with a %s?", where, text.OrQ(pr.HeadRefName), base, model.ShortSHA(head), m.mergeMethod())
	ok, handled := m.wf.MergeConfirm(app, pr)
	if !handled {
		ok = app.Confirm(what)
	}
	if !ok {
		app.Notice("merge cancelled")
		return
	}
	res, err := m.gh.MergePR(m.ctx, repo, pr.Number, head, "", m.mergeMethod())
	if err != nil {
		app.Fail(fmt.Sprintf("merge of %s failed: %s%s", where, err, deploys.TokenHint(err, "Contents", repo)))
		return
	}
	m.mu.Lock()
	pr.State = "MERGED"
	pr.MergeCommit = &model.MergeCommit{Oid: res.SHA}
	m.detailVer++
	m.mu.Unlock()
	app.OK(fmt.Sprintf("merged %s as %s; D approves a deployment that waits on an environment", where, model.ShortSHA(res.SHA)))
	app.Celebrate(true)
	app.Refresh(true)
}

// mergeSHA is the merge commit of a merged PR, or "".
func mergeSHA(pr *model.PR) string {
	if pr.MergeCommit != nil {
		return pr.MergeCommit.Oid
	}
	return ""
}

// doDeployApprove is the D key: approve the environment deployments
// waiting on a merged PR's merge commit (from its detail, or a row a
// workflow points at), or on a workflow row of its own (a waiting run,
// RowDeployer), and nothing else.
func (m *Model) doDeployApprove(app *lockedApp) {
	var pr *model.PR
	if app.CurrentView() == ViewDetail {
		pr, _ = app.Detail().(*model.PR)
	} else {
		// a workflow row with deployments of its own (a waiting run) first
		if row, ok := app.Selected(); ok && m.wf.DeployRow(app, row) {
			return
		}
		pr = m.wf.DeployTarget(app)
		if pr == nil {
			if row, ok := app.Selected(); ok {
				if p, ok := row.Item.(*model.PR); ok && p.State == "MERGED" {
					pr = p
				}
			}
		}
	}
	if pr == nil {
		app.Notice("D approves the deployments of a merged PR or a waiting run; select or open one first")
		return
	}
	repo := m.repoOf(pr)
	where := m.where(repo, pr.Number)
	sha := mergeSHA(pr)
	if pr.State != "MERGED" || sha == "" {
		app.Notice(where + " is not merged yet (M merges it)")
		return
	}
	app.Flash(fmt.Sprintf("reading %s's deploys on %s...", where, model.ShortSHA(sha)))
	st, err := m.deployCache.Fetch(m.ctx, m.gh, repo, sha, true, app.Now())
	if err != nil {
		hint := ""
		if strings.Contains(err.Error(), "not accessible by personal access token") {
			hint = " — your token needs Actions: Read on " + repo.Name
		}
		app.Fail(fmt.Sprintf("couldn't read %s's deploys: %s%s", where, err, hint))
		return
	}
	m.mu.Lock()
	pr.Set("deploy", st)
	m.detailVer++
	m.mu.Unlock()
	var mine []deploys.Wait
	for _, w := range st.Waits {
		if w.Mine {
			mine = append(mine, w)
		}
	}
	if len(mine) == 0 {
		switch {
		case len(st.Waits) > 0:
			app.Notice(fmt.Sprintf("%s: %s on %s, but %s is not a reviewer of %s", where, plural(len(st.Waits), "wait"), model.ShortSHA(sha), m.me, strings.Join(st.Envs(), ", ")))
		case st.Phase == "running" || st.Phase == "none":
			app.Notice(fmt.Sprintf("%s: nothing waits on %s yet (%s) — dillodash says when it does", where, model.ShortSHA(sha), st.Text))
			app.RefreshSoon(m.deployRefresh())
		case st.Phase == "done" || st.Phase == "cancelled":
			app.OK(fmt.Sprintf("%s: %s — every run on %s finished", where, st.Text, model.ShortSHA(sha)))
		default:
			app.Notice(fmt.Sprintf("%s: nothing waits on %s — %s (the card lists the runs)", where, model.ShortSHA(sha), st.Text))
		}
		return
	}
	lines := make([]string, len(mine))
	for i, w := range mine {
		lines[i] = fmt.Sprintf("%s (%s, run %d)", w.EnvName, text.Ellip(text.OrQ(w.Run.Name), 40), w.Run.ID)
	}
	if !app.Confirm(fmt.Sprintf("approve %s of %s on merge %s as %s?\n%s", plural(len(mine), "deployment"), where, model.ShortSHA(sha), m.me, strings.Join(lines, "\n"))) {
		app.Notice("deployment approval cancelled")
		return
	}
	comment := m.wf.DeployComment(pr, model.ShortSHA(sha))
	if comment == "" {
		comment = fmt.Sprintf("dillodash: %s merged as %s", where, model.ShortSHA(sha))
	}
	done := 0
	var failed []string
	for _, w := range mine {
		if err := m.gh.ApproveDeployments(m.ctx, repo, w.Run.ID, []int64{w.EnvID}, comment); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %s%s", w.EnvName, err, deploys.TokenHint(err, "Deployments", repo)))
			continue
		}
		done++
	}
	m.deployCache.Forget(sha)
	if len(failed) > 0 {
		app.Fail(fmt.Sprintf("approved %d of %d; failed: %s", done, len(mine), strings.Join(failed, "; ")))
	} else {
		names := envNames(mine)
		app.OK(fmt.Sprintf("approved %s of %s (%s); dillodash watches for the next", plural(done, "deployment"), where, strings.Join(names, ", ")))
		app.Celebrate(false)
	}
	app.Refresh(true)
	app.RefreshSoon(m.deployRefresh())
}

// ---- changed files / diffs ----

func (m *Model) openFiles(app *lockedApp, keepSelection bool) {
	pr, ok := app.Detail().(*model.PR)
	if !ok {
		return
	}
	repo := m.repoOf(pr)
	where := m.where(repo, pr.Number)
	app.Flash(fmt.Sprintf("loading files of %s...", where))
	files, err := m.gh.PRFiles(m.ctx, repo, pr.Number)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't load the files of %s: %s", where, err))
		return
	}
	m.mu.Lock()
	m.files = files
	m.view = ViewFiles
	if keepSelection {
		m.fileSel = min(m.fileSel, max(0, len(files)-1))
	} else {
		m.fileSel = 0
		delete(m.glide, "files")
	}
	m.searchQ, m.searchCur, m.hitsKey = "", 0, "" // a search belongs to one page
	m.errText = ""
	if m.statusKind == "spin" {
		m.status = ""
	}
	m.mu.Unlock()
}

// openPatch shows the selected file's diff (lock held).
func (m *Model) openPatch() {
	if len(m.files) == 0 {
		return
	}
	m.patchLines = nil
	m.patchStamp = patchStamp{-1, -1}
	m.patchScroll = 0
	m.searchQ, m.searchCur, m.hitsKey = "", 0, ""
	delete(m.glide, "patch")
	m.view = ViewPatch
}

// openDiffPager shows the whole-PR unified diff in $PAGER.
func (m *Model) openDiffPager(app *lockedApp) {
	pr, ok := app.Detail().(*model.PR)
	if !ok {
		return
	}
	repo := m.repoOf(pr)
	where := m.where(repo, pr.Number)
	app.Flash(fmt.Sprintf("fetching the diff of %s...", where))
	diff, err := m.gh.PRDiff(m.ctx, repo, pr.Number)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't fetch the diff of %s: %s", where, err))
		return
	}
	path, cleanup, err := external.WriteTempDiff(pr.Number, diff)
	if err != nil {
		app.Fail("couldn't write the diff: " + err.Error())
		return
	}
	defer cleanup()
	pager := external.PagerCommand()
	if err := app.RunExternal(append(pager, path)); err != nil {
		app.Fail(fmt.Sprintf("couldn't start the pager (%s): %s", pager[0], err))
	}
	m.mu.Lock()
	if m.statusKind == "spin" {
		m.status = ""
	}
	m.mu.Unlock()
}

// ---- go to, filter, search ----

// gotoRE: "12", "#12", "repo#12" or "owner/repo#12".
var gotoRE = regexp.MustCompile(`^(?:([\w.-]+(?:/[\w.-]+)?)#|#?\s*)(\d+)$`)

// doGoto opens any #N (or repo#N): the PR first, then the issue.
func (m *Model) doGoto(app *lockedApp) {
	txt, _ := app.Prompt("go to # (a PR or issue number, repo#N or owner/repo#N; Esc cancels):", workflow.PromptOpts{})
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return
	}
	mm := gotoRE.FindStringSubmatch(txt)
	if mm == nil {
		app.Notice(fmt.Sprintf("go to: %q is not a number", txt))
		return
	}
	n, _ := strconv.Atoi(mm[2])
	if len(m.repos) == 0 {
		app.Notice("go to: no repos are configured")
		return
	}
	repo := m.repos[0]
	if mm[1] != "" {
		found := false
		for _, r := range m.repos {
			if strings.EqualFold(r.Name, mm[1]) || strings.EqualFold(r.String(), mm[1]) {
				repo, found = r, true
				break
			}
		}
		if !found {
			app.Notice(fmt.Sprintf("go to: no repo named %q in the config", mm[1]))
			return
		}
	}
	app.Flash(fmt.Sprintf("opening %s...", m.where(repo, n)))
	started := fx.Now()
	pr, err := m.fetchPRDetail(m.ctx, repo, n, nil)
	if err == nil {
		m.mu.Lock()
		m.showDetail(pr, "pr", started, nil)
		m.mu.Unlock()
		return
	}
	if e, ok := ghapi.AsError(err); ok && e.IsRateLimit() {
		app.Fail(fmt.Sprintf("couldn't open %s: %s", m.where(repo, n), err))
		return
	}
	iss, err2 := m.gh.IssueDetail(m.ctx, repo, n)
	if err2 != nil {
		app.Fail(fmt.Sprintf("%s: %s", m.where(repo, n), err2))
		return
	}
	iss.Repo = repo
	m.mu.Lock()
	m.showDetail(iss, "issue", started, nil)
	m.mu.Unlock()
}

// startFilter is '/' in the list: a live filter, re-applied as you type.
func (m *Model) startFilter(app *lockedApp) {
	m.mu.Lock()
	before := m.filter
	m.mu.Unlock()
	out, ok := app.Prompt("filter (words match #number, title, author, labels; Enter keeps, Esc restores):", workflow.PromptOpts{
		Initial: before,
		OnChange: func(t string) { // on the UI goroutine, lock held
			m.filter = t
			m.refilter()
		},
	})
	m.mu.Lock()
	if ok {
		m.filter = out
	} else {
		m.filter = before
	}
	m.refilter()
	m.mu.Unlock()
}

// startSearch is '/' in the detail and diff views.
func (m *Model) startSearch(app *lockedApp) {
	what := "this page"
	if app.CurrentView() == ViewPatch {
		what = "this diff"
	}
	q, ok := app.Prompt(fmt.Sprintf("search %s (smartcase; n/N next/prev, Esc clears):", what), workflow.PromptOpts{})
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ok || q == "" {
		m.searchQ = ""
		return
	}
	m.applySearch(q)
}

// ---- urls ----

// currentURL is the web URL of what the cursor is on (lock held).
func (m *Model) currentURL() string {
	if m.view == ViewFortress {
		return m.fortressURL()
	}
	if m.view != ViewList && m.detail != nil {
		suffix := ""
		if m.view == ViewFiles || m.view == ViewPatch {
			suffix = "/files"
		}
		switch d := m.detail.(type) {
		case *model.PR:
			return ghapi.PRURL(m.host, m.repoOf(d), d.Number) + suffix
		case *model.Issue:
			return ghapi.IssueURL(m.host, m.repoOf(d), d.Number)
		}
		return ""
	}
	row, ok := m.Selected()
	if !ok {
		return ""
	}
	if rk, ok := m.wf.RowKinds()[row.Kind]; ok && rk.URL != nil {
		if u := rk.URL(m, row); u != "" {
			return u
		}
	}
	switch v := row.Item.(type) {
	case *model.PR:
		return ghapi.PRURL(m.host, m.repoOf(v), v.Number)
	case *model.Issue:
		return ghapi.IssueURL(m.host, m.repoOf(v), v.Number)
	}
	return ""
}

func (m *Model) copyURL() {
	url := m.currentURL()
	if url == "" {
		return
	}
	if where := m.copy(url); where != "" {
		m.ok(fmt.Sprintf("copied %s to the %s", url, where))
	} else {
		m.notice("couldn't copy — the URL is " + url)
	}
}

func (m *Model) openInBrowser() {
	url := m.currentURL()
	if url == "" {
		return
	}
	if m.openURL(url) {
		m.ok("opened " + url)
		return
	}
	if where := m.copy(url); where != "" {
		m.ok(fmt.Sprintf("no browser on this host (SSH?) — copied %s to the %s", url, where))
	} else {
		m.notice("no browser here — the URL is " + url)
	}
}

// plural is "1 deployment" / "3 deployments".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// envNames is the sorted, deduplicated environment names of the waits.
func envNames(waits []deploys.Wait) []string {
	set := map[string]bool{}
	for _, w := range waits {
		set[w.EnvName] = true
	}
	names := make([]string, 0, len(set))
	for e := range set {
		names = append(names, e)
	}
	sort.Strings(names)
	return names
}
