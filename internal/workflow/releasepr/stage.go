package releasepr

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// unitPR finds the staging PR number in a unit's subject: a merge commit
// or a squash.
var unitPR = regexp.MustCompile(`Merge pull request #(\d+)|\(#(\d+)\)\s*$`)

// stageSummary is a repo's synthetic stage row: units ahead of
// production by tag. The compare is fetched only when the (prod, stage)
// pair moved.
func (w *Workflow) stageSummary(ctx context.Context, gh ghapi.Client, repo model.Repo, prod, stage, cut string) (*Stage, error) {
	row := &Stage{Repo: repo}
	if prod == "" || stage == "" {
		return row, nil
	}
	var compare ghapi.CompareResult
	w.stageMu.Lock()
	cached, ok := w.stageCache[repo]
	w.stageMu.Unlock()
	switch {
	case prod == stage:
		compare = ghapi.CompareResult{}
	case ok && cached.prod == prod && cached.stage == stage:
		compare = cached.compare
	default:
		c, err := gh.Compare(ctx, repo, w.cfg.ProductionBranch, w.cfg.StagingBranch)
		if err != nil {
			return row, err
		}
		compare = c
		// the compare reads the branches, which may have moved since the
		// oids were read: kept under those oids only when its head is the
		// staging oid (else the next refresh asks again, an ETag 304 when
		// nothing moved)
		if n := len(c.Commits); n > 0 && c.Commits[n-1].SHA == stage {
			w.stageMu.Lock()
			w.stageCache[repo] = stageCached{prod: prod, stage: stage, compare: c}
			w.stageMu.Unlock()
		}
	}
	goTags, err := gh.TagRefs(ctx, repo, w.cfg.GoTagPrefix)
	if err != nil {
		return row, err
	}
	holdTags, err := gh.TagRefs(ctx, repo, w.cfg.HoldTagPrefix)
	if err != nil {
		return row, err
	}
	u := StageUnits(compare, goTags, holdTags, w.cfg.GoTagPrefix, w.cfg.HoldTagPrefix)
	if n, ok := GoPastCut(compare, goTags, w.cfg.GoTagPrefix, cut); ok {
		u.GoPastCut, u.Cut = n, cut
	}
	row.Units = &u
	return row, nil
}

// StageUnits is what the staging branch carries ahead of production,
// from a compare page (prod...stage) and the go/<N> and hold/<N> tag
// names: walk the first-parent chain from the head (the page's last
// commit) while it stays on the page, one unit per first-parent commit
// (a staging PR's merge commit or squash; its PR number from the
// subject), and count them by tag. Side commits inside a merged branch
// are not on the chain and do not count.
func StageUnits(compare ghapi.CompareResult, goTags, holdTags []string, goPrefix, holdPrefix string) Units {
	goSet := tagSet(goTags, goPrefix)
	holdSet := tagSet(holdTags, holdPrefix)
	ahead := compare.AheadBy
	if ahead == 0 && compare.TotalCommits > 0 {
		ahead = compare.TotalCommits
	}
	if ahead == 0 {
		ahead = len(compare.Commits)
	}
	out := Units{Ahead: ahead, Truncated: ahead > len(compare.Commits)}
	bySHA := make(map[string]ghapi.CompareCommit, len(compare.Commits))
	for _, c := range compare.Commits {
		bySHA[c.SHA] = c
	}
	sha := ""
	if n := len(compare.Commits); n > 0 {
		sha = compare.Commits[n-1].SHA
	}
	seen := map[string]bool{}
	for sha != "" && !seen[sha] {
		c, ok := bySHA[sha]
		if !ok {
			break // production, or past the page
		}
		seen[sha] = true
		subject, _, _ := strings.Cut(c.Message, "\n")
		n := ""
		if m := unitPR.FindStringSubmatch(subject); m != nil {
			n = m[1]
			if n == "" {
				n = m[2]
			}
		}
		out.Units++
		switch {
		case n != "" && goSet[n]:
			out.Go++
		case n != "" && holdSet[n]:
			out.Hold++
		default:
			out.Untagged++
		}
		sha = ""
		if len(c.Parents) > 0 {
			sha = c.Parents[0]
		}
	}
	return out
}

// GoPastCut counts the go units on the staging chain newer than cut (the
// open release's head): the first-parent walk from the staging head
// down to cut. ok is false when cut is empty or not on the walked
// chain (production, or past the compare page).
func GoPastCut(compare ghapi.CompareResult, goTags []string, goPrefix, cut string) (n int, ok bool) {
	if cut == "" {
		return 0, false
	}
	goSet := tagSet(goTags, goPrefix)
	bySHA := make(map[string]ghapi.CompareCommit, len(compare.Commits))
	for _, c := range compare.Commits {
		bySHA[c.SHA] = c
	}
	sha := ""
	if k := len(compare.Commits); k > 0 {
		sha = compare.Commits[k-1].SHA
	}
	seen := map[string]bool{}
	for sha != "" && !seen[sha] {
		if sha == cut {
			return n, true
		}
		c, found := bySHA[sha]
		if !found {
			return 0, false
		}
		seen[sha] = true
		subject, _, _ := strings.Cut(c.Message, "\n")
		if m := unitPR.FindStringSubmatch(subject); m != nil {
			num := m[1]
			if num == "" {
				num = m[2]
			}
			if goSet[num] {
				n++
			}
		}
		sha = ""
		if len(c.Parents) > 0 {
			sha = c.Parents[0]
		}
	}
	return 0, false
}

// goWaiting is the go units waiting behind an approved release, from
// its repo's stage row: newer than the release's head, which an
// approved release keeps (a re-cut would dismiss the approval). 0 when
// the release is not approved, not the cut the row counted against, or
// nothing waits.
func (w *Workflow) goWaiting(st *State, pr *model.PR) int {
	if st == nil || pr == nil || pr.ReviewDecision != "APPROVED" || (pr.State != "" && pr.State != "OPEN") || pr.HeadRefOid == "" {
		return 0
	}
	for _, s := range st.Stages {
		if s == nil || s.Repo != pr.Repo || s.Units == nil {
			continue
		}
		if s.Units.Cut == pr.HeadRefOid {
			return s.Units.GoPastCut
		}
	}
	return 0
}

// goWaitingText is "2 go units waiting", or "".
func goWaitingText(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d go unit%s waiting", n, plural(n, "", "s"))
}

func tagSet(tags []string, prefix string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			out[strings.TrimPrefix(t, prefix)] = true
		}
	}
	return out
}

// StageText is the stage row's text: "stage == main", "stage: nothing
// ahead of main", or "stage: 3 units ahead of main · 1 go · 1 hold · 1
// untagged".
func StageText(u *Units, prod, stage string) string {
	if prod == "" {
		prod = "main"
	}
	if stage == "" {
		stage = "stage"
	}
	if u == nil || u.Units == 0 {
		if u != nil && u.Ahead == 0 {
			return stage + " == " + prod
		}
		return fmt.Sprintf("%s: nothing ahead of %s", stage, prod)
	}
	ge := ""
	if u.Truncated {
		ge = "≥"
	}
	sep := glyphs.G("sep")
	return fmt.Sprintf("%s: %s%d unit%s ahead of %s %s %d go %s %d hold %s %d untagged",
		stage, ge, u.Units, plural(u.Units, "", "s"), prod, sep, u.Go, sep, u.Hold, sep, u.Untagged)
}

// stageText renders a stage row with the configured branch names.
func (w *Workflow) stageText(s *Stage) string {
	return StageText(s.Units, w.cfg.ProductionBranch, w.cfg.StagingBranch)
}

// sinceTTL is how long a since-the-cut read is kept while the staging
// tip and the cut stand still: the first tier can move without either,
// so it is short. It spares the request on the quick refreshes around a
// write or a deploy; an ordinary refresh reads again, one cheap request
// per repo with an open release.
const sinceTTL = time.Minute

// sinceListed is how many PR numbers per branch the stage row's notice
// names before "and N more": it is one status line.
const sinceListed = 8

type sinceCached struct {
	at    time.Time
	cut   time.Time
	stage string
	since *SinceCut
}

// cutOf finds the open release the stage row answers to (a release
// cut, never a hotfix or another branch) and when it was cut.
//
// A cut is pinned on a staging commit, so the moment is when that commit
// landed on staging: the merge time of the staging PR whose merge commit
// is the release head. GitHub stamps a merge a second or two after the
// merge commit, and the search compares merge times, so the commit's own
// time would count the cut's last unit as merged "since". A re-cut moves
// the moment with the head. A head that is no staging merge (someone
// updated the release branch) falls back to its commit time, and a
// release whose head commit was not read to the PR's creation.
func (w *Workflow) cutOf(prs []*model.PR) (*model.PR, time.Time) {
	var rel *model.PR
	for _, pr := range prs {
		if pr == nil || pr.IsDraft || (pr.State != "" && pr.State != "OPEN") || w.Kind(pr) != KindRelease {
			continue
		}
		if rel == nil || pr.CreatedAt.After(rel.CreatedAt) {
			rel = pr
		}
	}
	if rel == nil {
		return nil, time.Time{}
	}
	c := rel.HeadCommit()
	// the last commit read is the head only when their ids agree (a PR
	// lists at most 250 commits); an id that was not read is trusted
	if c == nil || (c.Oid != "" && rel.HeadRefOid != "" && c.Oid != rel.HeadRefOid) {
		return rel, rel.CreatedAt
	}
	for _, l := range c.Landed.Nodes {
		if l.BaseRefName == w.cfg.StagingBranch && l.MergedAt != nil && !l.MergedAt.IsZero() &&
			l.MergeCommit != nil && l.MergeCommit.Oid != "" && l.MergeCommit.Oid == rel.HeadRefOid {
			return rel, *l.MergedAt
		}
	}
	if !c.CommittedDate.IsZero() {
		return rel, c.CommittedDate
	}
	return rel, rel.CreatedAt
}

// sinceCut reads what merged below production since the repo's open
// release was cut. Nil when there is no open release or the repo has no
// first tier, and on a failed read: the row then says nothing rather
// than something stale, and the error is the caller's to report.
func (w *Workflow) sinceCut(ctx context.Context, gh ghapi.Client, repo model.Repo, stageOid string, prs []*model.PR, lease *Lease, now time.Time) (*SinceCut, error) {
	if w.first == "" || !w.firstRepos[repo] {
		return nil, nil
	}
	rel, at := w.cutOf(prs)
	number := 0
	switch {
	case rel != nil && !at.IsZero():
		number = rel.Number
	case lease != nil:
		// someone is running a release and nothing is cut yet: what
		// merged since they began is what their sweep has to cover
		at = lease.Since
	default:
		return nil, nil
	}
	w.stageMu.Lock()
	hit, ok := w.sinceCache[repo]
	w.stageMu.Unlock()
	if ok && hit.cut.Equal(at) && hit.stage == stageOid && now.Sub(hit.at) < sinceTTL {
		return hit.since, nil
	}
	s, err := gh.MergedSince(ctx, repo, w.first, w.cfg.StagingBranch, at)
	if err != nil {
		return nil, err
	}
	out := &SinceCut{Release: number, At: at, Tier: w.first, Since: s}
	w.stageMu.Lock()
	w.sinceCache[repo] = sinceCached{at: now, cut: at, stage: stageOid, since: out}
	w.stageMu.Unlock()
	return out, nil
}

// sinceText is the stage row's tail while a release is open: "since the
// cut: +3 dev, +1 stage · dev ≠ stage", or just "dev == stage" when
// nothing merged. "" without a read.
func (w *Workflow) sinceText(s *Stage) string {
	c := s.Since
	if c == nil {
		return w.leaseText(s, clock.Now())
	}
	var merged []string
	if c.FirstCount > 0 {
		merged = append(merged, fmt.Sprintf("+%d %s", c.FirstCount, c.Tier))
	}
	if c.StageCount > 0 {
		merged = append(merged, fmt.Sprintf("+%d %s", c.StageCount, w.cfg.StagingBranch))
	}
	var parts []string
	if len(merged) > 0 {
		parts = append(parts, c.label()+": "+strings.Join(merged, ", "))
	}
	switch {
	case c.Same():
		parts = append(parts, fmt.Sprintf("%s == %s", c.Tier, w.cfg.StagingBranch))
	case c.Known():
		parts = append(parts, fmt.Sprintf("%s ≠ %s", c.Tier, w.cfg.StagingBranch))
	}
	if l := w.leaseText(s, clock.Now()); l != "" {
		parts = append([]string{l}, parts...)
	}
	return strings.Join(parts, " "+glyphs.G("sep")+" ")
}

// label names the moment the counts start from.
func (c *SinceCut) label() string {
	if c.Release == 0 {
		return "since the lease"
	}
	return "since the cut"
}

// leaseText is "lease: carol 42m", or "".
func (w *Workflow) leaseText(s *Stage, now time.Time) string {
	if s.Lease == nil {
		return ""
	}
	return fmt.Sprintf("lease: %s %s", s.Lease.Holder, deploys.FmtSecs(now.Sub(s.Lease.Since)))
}

// sinceNotice is the same in a sentence, with the PR numbers: what
// Enter on the stage row says. The verdict comes first, so a narrow
// screen clips the numbers, not the answer.
func (w *Workflow) sinceNotice(s *Stage, now time.Time) string {
	c := s.Since
	if c == nil {
		return ""
	}
	list := func(prs []ghapi.MergedPR, n int) string {
		var out []string
		for i, p := range prs {
			if i == sinceListed {
				break
			}
			out = append(out, fmt.Sprintf("#%d", p.Number))
		}
		if n > len(out) {
			out = append(out, fmt.Sprintf("and %d more", n-len(out)))
		}
		return strings.Join(out, " ")
	}
	var parts []string
	if c.FirstCount > 0 {
		parts = append(parts, fmt.Sprintf("into %s %s", c.Tier, list(c.First, c.FirstCount)))
	}
	if c.StageCount > 0 {
		parts = append(parts, fmt.Sprintf("into %s %s", w.cfg.StagingBranch, list(c.Stage, c.StageCount)))
	}
	msg := s.Repo.Name + ": "
	switch {
	case c.Same():
		msg += fmt.Sprintf("%s and %s hold the same content", c.Tier, w.cfg.StagingBranch)
	case c.Known():
		msg += fmt.Sprintf("%s and %s differ", c.Tier, w.cfg.StagingBranch)
	default:
		msg += fmt.Sprintf("couldn't compare %s with %s", c.Tier, w.cfg.StagingBranch)
	}
	if c.Release == 0 {
		msg += fmt.Sprintf(" — since the release lease began (%s): ", clock.Ago(c.At, now))
	} else {
		msg += fmt.Sprintf(" — since release #%d was cut (%s): ", c.Release, clock.Ago(c.At, now))
	}
	if len(parts) == 0 {
		msg += "nothing merged"
	} else {
		msg += "merged " + strings.Join(parts, "; ")
	}
	if s.Lease != nil {
		msg += fmt.Sprintf(" — %s holds the release lease", s.Lease.Holder)
	}
	return msg
}
