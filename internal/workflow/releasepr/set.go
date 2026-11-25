package releasepr

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// ---- the whole release: one A for every release that is ready ----
//
// With set_approve, A on a release offers the set: every open release
// (never a hotfix) that the viewer may approve and that is not already
// on its way. One confirm lists them in lane order; yes posts every
// review, each pinned to the head on screen, and for NOW one reason and
// then every command. Each review and each command is the viewer's own,
// on its own PR, exactly as A on each row would post it; the set only
// saves the keystrokes. With server_lane the manifest repo keeps the
// lane order, so the commands go out together and nothing here waits.

// setMember is one release of the set and whether the viewer's approval
// already stands on its head.
type setMember struct {
	pr       *model.PR
	approved bool
}

// setLeftOut is a release that is not in the set, and why.
type setLeftOut struct {
	pr  *model.PR
	why string
}

// releaseSet is the set A offers for pr: its members in lane order and
// the releases left out. ok is false when the set is off, pr is not a
// member, or fewer than two releases are ready (A on one release is the
// plain path).
func (w *Workflow) releaseSet(app workflow.App, pr *model.PR) (members []setMember, left []setLeftOut, ok bool) {
	if !w.cfg.SetApprove || pr == nil {
		return nil, nil, false
	}
	st := w.stateOf(app)
	if st == nil {
		return nil, nil, false
	}
	me := app.Me()
	in := false
	for _, cand := range st.Releases {
		if why := w.setExcludes(app, cand, me); why != "" {
			if why != "-" {
				left = append(left, setLeftOut{cand, why})
			}
			continue
		}
		members = append(members, setMember{pr: cand, approved: workflow.IApproved(app, cand)})
		if cand.Ident() == pr.Ident() {
			in = true
		}
	}
	if !in || len(members) < 2 {
		return nil, nil, false
	}
	sort.SliceStable(members, func(i, j int) bool { return w.laneLess(members[i].pr, members[j].pr) })
	sort.SliceStable(left, func(i, j int) bool { return w.laneLess(left[i].pr, left[j].pr) })
	return members, left, true
}

// setExcludes is why cand is not in the set: "" for a member, "-" for a
// PR that is not a candidate at all (merged, closed, not a release, a
// hotfix), else the reason the confirm names.
func (w *Workflow) setExcludes(app workflow.App, cand *model.PR, me string) string {
	if cand == nil || (cand.State != "" && cand.State != "OPEN") || w.NotARelease(cand) || w.kindOf(cand) != KindRelease {
		return "-"
	}
	if w.inFlight(cand) || w.recentlyPosted(cand) {
		return "-" // on its way already (or this session just sent it): nothing to post
	}
	if l := w.cfg.LaneQueuedLabel; w.cfg.ServerLane && l != "" && cand.Labels.Has(l) {
		return "-" // waiting its turn in the release lane
	}
	if !app.Config().GitHub.WriteAllowed(cand.Repo) {
		return "read-only here (allowed_repos)"
	}
	switch {
	case cand.IsDraft:
		return "a draft"
	case me != "" && strings.EqualFold(cand.Author.Login, me):
		return "yours: another approver reviews it"
	case cand.ReviewDecision == "CHANGES_REQUESTED":
		return "changes requested"
	}
	if ci, ok := cand.Get(ExtCI).(model.CICluster); ok && len(ci.Failed) > 0 {
		return "checks failing: " + strings.Join(ci.Failed, ", ")
	}
	return ""
}

// recentlyPosted reports a release this session sent NOW within
// setRepostGuard: until the manifest repo's lane label or its run shows,
// its row may still read scheduled, and the set must not send it twice.
func (w *Workflow) recentlyPosted(pr *model.PR) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.progress[pr.Ident()]
	return ok && !t.nowPostedAt.IsZero() && clock.Now().Sub(t.nowPostedAt) < setRepostGuard
}

// setRepostGuard is how long a release this session sent stays out of
// the set, whatever its row reads.
const setRepostGuard = 15 * time.Minute

// laneLess orders releases by lane_order (repos not listed after the
// listed ones, by name), then by number.
func (w *Workflow) laneLess(a, b *model.PR) bool {
	ia, ib := w.laneIndex(a.Repo), w.laneIndex(b.Repo)
	if ia < 0 {
		ia = len(w.cfg.LaneOrder)
	}
	if ib < 0 {
		ib = len(w.cfg.LaneOrder)
	}
	if ia != ib {
		return ia < ib
	}
	if a.Repo.Name != b.Repo.Name {
		return a.Repo.Name < b.Repo.Name
	}
	return a.Number < b.Number
}

// The set's menu entries.
func setQueueLabel(n int, night string) string {
	return fmt.Sprintf("approve the whole release (%d): queue every one for %s window", n, night)
}

func setNowLabel(n int) string {
	return fmt.Sprintf("release the whole set NOW (%d), in lane order — one reason for all", n)
}

// setList is the confirm's list: one line per member, in lane order, and
// the releases left out.
func setList(members []setMember, left []setLeftOut) string {
	var b strings.Builder
	for i, m := range members {
		mark := ""
		if m.approved {
			mark = " (your approval stands)"
		}
		fmt.Fprintf(&b, "  %d. %s at %s%s\n", i+1, where(m.pr), model.ShortSHA(m.pr.HeadRefOid), mark)
	}
	for _, l := range left {
		fmt.Fprintf(&b, "  left out: %s (%s)\n", where(l.pr), l.why)
	}
	return strings.TrimRight(b.String(), "\n")
}

// approveSet posts the set: every review the viewer has not posted on
// that head yet, then, for now, one command per member in lane order.
// Reviews go first and all of them: a NOW with a review that failed
// posts no command at all, so the lane never runs a set with a hole in
// it. A command that fails stops the rest, for the same reason.
func (w *Workflow) approveSet(app workflow.App, members []setMember, left []setLeftOut, now, queues bool) {
	me := app.Me()
	night := w.NightPhrase(app.Now())
	n := len(members)
	what := fmt.Sprintf("approving %d releases", n)
	if !workflow.PostGuard(app, what) {
		return
	}
	list := setList(members, left)
	body := ""
	if now {
		var ok bool
		q := fmt.Sprintf("approve %d releases as %s and post that on each, in lane order?\n%s", n, me, list)
		if body, ok = w.nowBody(app, q); !ok {
			return
		}
	} else {
		q := fmt.Sprintf("approve %d releases as %s?\n%s", n, me, list)
		if queues {
			q = fmt.Sprintf("approve %d releases as %s? (queues every one for %s window)\n%s", n, me, night, list)
		}
		for _, m := range members {
			if !m.approved {
				workflow.PrimeHead(app, m.pr)
			}
		}
		if !app.Confirm(q) {
			for _, m := range members {
				workflow.ForgetHead(m.pr)
			}
			app.Notice("approve canceled")
			return
		}
	}
	app.Flash(fmt.Sprintf("approving %d releases as %s...", n, me))
	var failed []string
	approved := 0
	for _, m := range members {
		if m.approved {
			continue
		}
		if !workflow.HeadStill(app, m.pr, "approval of "+where(m.pr)) {
			failed = append(failed, where(m.pr)+": its head moved")
			continue
		}
		if err := app.GH().Approve(app.Context(), m.pr.Repo, m.pr.Number, "", m.pr.HeadRefOid); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %s", where(m.pr), err))
			continue
		}
		app.MarkApproved(m.pr)
		approved++
	}
	toApprove := 0
	for _, m := range members {
		if !m.approved {
			toApprove++
		}
	}
	if len(failed) > 0 {
		lead := fmt.Sprintf("approved %d of %d", approved, toApprove)
		if now {
			app.Fail(fmt.Sprintf("%s; nothing released — failed: %s. Fix it, then A releases the set", lead, strings.Join(failed, "; ")))
		} else {
			app.Fail(fmt.Sprintf("%s; failed: %s. A on the set approves the rest", lead, strings.Join(failed, "; ")))
		}
		app.Refresh(true)
		return
	}
	if !now {
		if queues {
			app.OK(fmt.Sprintf("approved %d releases as %s — every one queued for %s window, in lane order", n, me, night))
		} else {
			app.OK(fmt.Sprintf("approved %d releases as %s — M merges each", n, me))
		}
		app.Celebrate(true)
		app.Refresh(true)
		return
	}
	// every dispatch names the whole set, in lane order: the manifest
	// repo's lane waits for an earlier member that has not joined yet
	names := make([]string, 0, n)
	for _, m := range members {
		names = append(names, fmt.Sprintf("%s#%d", m.pr.Repo.Name, m.pr.Number))
	}
	setNames := strings.Join(names, ",")
	posted := 0
	var unsure []string
	for i, m := range members {
		res := w.postSetCommand(app, m.pr, body, setNames)
		if res.unsure != "" {
			unsure = append(unsure, res.unsure)
		}
		if res.failed != "" {
			// one line: what failed, and what to do next. The member that
			// failed is on its way or needs its thread read, never A again.
			msg := fmt.Sprintf("released %d of %d; %s", posted, n, res.failed)
			if i+1 < len(members) {
				rest := make([]string, 0, len(members)-i-1)
				for _, r := range members[i+1:] {
					rest = append(rest, where(r.pr))
				}
				msg += fmt.Sprintf("; not posted: %s — A on %s releases those", strings.Join(rest, ", "), rest[0])
			}
			app.Fail(msg)
			app.RefreshSoon(45 * time.Second)
			return
		}
		posted++
	}
	if len(unsure) > 0 {
		app.Fail(fmt.Sprintf("posted %s on all %d, but telling the manifest repo may have failed for %s — watch those rows: press M only if one has not started in two minutes",
			w.cfg.ReleaseNowCommand, n, strings.Join(unsure, ", ")))
		app.Launch()
		app.RefreshSoon(45 * time.Second)
		return
	}
	app.OK(fmt.Sprintf("released the set as %s: %s posted on %d releases — the release lane runs them in lane order, one at a time", me, w.cfg.ReleaseNowCommand, n))
	app.Launch()
	w.offerSetSlotCancel(app, members)
	app.Refresh(true)
}

// setPost is how one member's command went: failed (the set stops
// there) says what happened and what to do about it; unsure names a
// member whose dispatch may not have reached the manifest repo.
type setPost struct{ failed, unsure string }

// postSetCommand posts the command on one member and tells the manifest
// repo.
func (w *Workflow) postSetCommand(app workflow.App, pr *model.PR, body, set string) setPost {
	cmd := w.cfg.ReleaseNowCommand
	wh := where(pr)
	id, unsure, err := workflow.Post(app, pr.Repo, pr.Number, body)
	if err != nil {
		if unsure {
			return setPost{failed: fmt.Sprintf("%s: %s may have posted (%s) — read its thread before anything else", wh, cmd, err)}
		}
		return setPost{failed: fmt.Sprintf("%s: %s NOT posted (%s)", wh, cmd, err)}
	}
	w.noteComment(pr, app.Now(), id)
	target := w.manifestRepo(app)
	if target.IsZero() || !w.cfg.ReleaseNowDispatch || w.cfg.ReleaseNowCommentOnly {
		// no dispatch to fire, or the release repo's own workflow fires it
		w.notePosted(pr, app.Now(), true)
		return setPost{}
	}
	payload := map[string]any{"repo": pr.Repo.Name, "pr": pr.Number, "comment_id": id, "set": set}
	if err := app.GH().Dispatch(app.Context(), target, w.cfg.ReleaseNowDispatchEvent, payload); err != nil {
		if ghapi.Ambiguous(err) {
			w.notePosted(pr, app.Now(), true)
			return setPost{unsure: wh}
		}
		w.notePosted(pr, app.Now(), false)
		return setPost{failed: fmt.Sprintf("%s: posted %s, but %s was not told (%s) — A on its row fires the dispatch again", wh, cmd, target, err)}
	}
	w.notePosted(pr, app.Now(), true)
	return setPost{}
}

// offerSetSlotCancel asks once to take every member that holds a slot
// on a manifest off it, so the window does not pick up a release the
// set already sent: one question, then one cancel per item.
func (w *Workflow) offerSetSlotCancel(app workflow.App, members []setMember) {
	if w.win == nil {
		return
	}
	prov, _ := app.State(WindowName).(workflow.ManifestItemProvider)
	repo := w.manifestRepo(app)
	if prov == nil || repo.IsZero() {
		return
	}
	type slot struct {
		issue int
		text  string
	}
	var slots []slot
	var names []string
	for _, m := range members {
		it, ok := prov.ManifestItem(m.pr.Repo.Name, m.pr.Number)
		if !ok || it.Issue == 0 || (it.Final != nil && *it.Final) || !slotted(it) {
			continue
		}
		id := it.ID
		if !strings.HasPrefix(id, "rel:") {
			id = fmt.Sprintf("rel:%s#%d", m.pr.Repo.Name, m.pr.Number)
		}
		slots = append(slots, slot{it.Issue, w.win.CancelCommand + " " + id})
		names = append(names, where(m.pr))
	}
	if len(slots) == 0 {
		return
	}
	workflow.WarmMembership(app)
	if !app.Confirm(fmt.Sprintf("%s also hold a place on tonight's manifest: post %s for each there, so the window does not pick up a release already sent?",
		strings.Join(names, ", "), w.win.CancelCommand)) {
		app.Notice("the window still lists " + strings.Join(names, ", "))
		return
	}
	if !workflow.PostGuard(app, w.win.CancelCommand) {
		return
	}
	for _, s := range slots {
		if _, unsure, err := workflow.Post(app, repo, s.issue, s.text); err != nil {
			workflow.PostFailed(app, "", fmt.Sprintf("%s on #%d", s.text, s.issue), unsure, err)
			return
		}
	}
	app.OK(fmt.Sprintf("posted %d cancel(s) on the manifest; the conductor replies there within a minute", len(slots)))
	app.RefreshSoon(45 * time.Second)
}
