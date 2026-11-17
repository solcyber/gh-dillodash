package releasepr

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
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
func (w *Workflow) stageSummary(ctx context.Context, gh ghapi.Client, repo model.Repo, prod, stage string) (*Stage, error) {
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
// tip and the cut stand still: the first tier can move without either.
const sinceTTL = time.Minute

type sinceCached struct {
	at    time.Time
	cut   time.Time
	stage string
	since *SinceCut
}

// cutOf finds the open release the stage row answers to (a release
// cut, never a hotfix or another branch) and when it was cut: its head
// commit's time, which is when the last unit in it reached staging, so a
// re-cut moves the moment with it. The PR's creation is the fallback.
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
	at := rel.CreatedAt
	if c := rel.HeadCommit(); c != nil && !c.CommittedDate.IsZero() {
		at = c.CommittedDate
	}
	return rel, at
}

// sinceCut reads what merged below production since the repo's open
// release was cut. Nil when there is no open release or no first tier,
// and on a failed read (the row then says nothing rather than something
// stale).
func (w *Workflow) sinceCut(ctx context.Context, gh ghapi.Client, repo model.Repo, stageOid string, prs []*model.PR, now time.Time) *SinceCut {
	if w.first == "" {
		return nil
	}
	rel, at := w.cutOf(prs)
	if rel == nil || at.IsZero() {
		return nil
	}
	w.stageMu.Lock()
	hit, ok := w.sinceCache[repo]
	w.stageMu.Unlock()
	if ok && hit.cut.Equal(at) && hit.stage == stageOid && now.Sub(hit.at) < sinceTTL {
		return hit.since
	}
	s, err := gh.MergedSince(ctx, repo, w.first, w.cfg.StagingBranch, at)
	if err != nil {
		return nil
	}
	out := &SinceCut{Release: rel.Number, At: at, Tier: w.first, Since: s}
	w.stageMu.Lock()
	w.sinceCache[repo] = sinceCached{at: now, cut: at, stage: stageOid, since: out}
	w.stageMu.Unlock()
	return out
}

// sinceText is the stage row's tail while a release is open: "since the
// cut: +3 dev, +1 stage · dev ≠ stage", or just "dev == stage" when
// nothing merged. "" without a read.
func (w *Workflow) sinceText(s *Stage) string {
	c := s.Since
	if c == nil {
		return ""
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
		parts = append(parts, "since the cut: "+strings.Join(merged, ", "))
	}
	switch {
	case c.Same():
		parts = append(parts, fmt.Sprintf("%s == %s", c.Tier, w.cfg.StagingBranch))
	case c.Known():
		parts = append(parts, fmt.Sprintf("%s ≠ %s", c.Tier, w.cfg.StagingBranch))
	}
	return strings.Join(parts, " "+glyphs.G("sep")+" ")
}

// sinceNotice is the same in a sentence, with the PR numbers: what
// Enter on the stage row says.
func (w *Workflow) sinceNotice(s *Stage, now time.Time) string {
	c := s.Since
	if c == nil {
		return ""
	}
	list := func(prs []ghapi.MergedPR, n int) string {
		var out []string
		for _, p := range prs {
			out = append(out, fmt.Sprintf("#%d", p.Number))
		}
		if n > len(prs) {
			out = append(out, fmt.Sprintf("and %d more", n-len(prs)))
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
	msg := fmt.Sprintf("%s since release #%d was cut (%s): ", s.Repo.Name, c.Release, clock.Ago(c.At, now))
	if len(parts) == 0 {
		msg += "nothing merged"
	} else {
		msg += "merged " + strings.Join(parts, "; ")
	}
	switch {
	case c.Same():
		msg += fmt.Sprintf(" — %s and %s hold the same content", c.Tier, w.cfg.StagingBranch)
	case c.Known():
		msg += fmt.Sprintf(" — %s and %s differ", c.Tier, w.cfg.StagingBranch)
	}
	return msg
}
