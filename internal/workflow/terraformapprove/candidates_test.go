package terraformapprove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

var infra = model.Repo{Owner: "acme", Name: "infra"}

var oldSHA = "59e47039" + strings.Repeat("0", 32)

const (
	headDate = "2026-10-22T18:00:00Z"
	headSHA  = "6a2b8e9aed68543e8d38f122d05d79584af89ad6"
	idA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	idC      = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	idD      = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	idE      = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	idF      = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	setID    = idF
	cid      = idC
)

// testCfg is the acme fixture config: reviews on acme/infra, the digest
// bot infra-bot, a 23:00-23:28 New York ceremony window and the default
// maintenance window labelled ET.
func testCfg() *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = []model.Repo{infra}
	ta := config.DefaultTerraformApprove()
	ta.DigestBot = "infra-bot"
	ta.CeremonyWindow = &config.CeremonyWindow{Timezone: "America/New_York", From: clock.HM{H: 23}, To: clock.HM{H: 23, M: 28}}
	cfg.TerraformApprove = ta
	mw := config.DefaultMaintenanceWindow()
	mw.TZLabel = "ET"
	mw.ManifestRepo = infra
	cfg.MaintenanceWindow = mw
	return cfg
}

func at(iso string) time.Time {
	t, err := clock.ParseUTC(iso)
	if err != nil {
		panic(err)
	}
	return t
}

func comment(body string) model.Comment { return commentAt(body, "2026-10-22T18:10:00Z") }

func commentAt(body, iso string) model.Comment {
	return model.Comment{Author: model.Actor{Login: "infra-bot"}, CreatedAt: at(iso), Body: body}
}

func commentBy(login, body, iso string) model.Comment {
	return model.Comment{Author: model.Actor{Login: login}, CreatedAt: at(iso), Body: body}
}

func makePR(comments []model.Comment, head string) *model.PR {
	pr := &model.PR{Repo: infra, Number: 3622, Title: "PR", State: "OPEN",
		Author: model.Actor{Login: "bob"}, HeadRefName: "x", BaseRefName: "main", HeadRefOid: head}
	pr.Commits.Nodes = append(pr.Commits.Nodes, struct {
		Commit model.Commit `json:"commit"`
	}{model.Commit{CommittedDate: at(headDate)}})
	pr.Comments.Nodes = comments
	pr.Comments.TotalCount = len(comments)
	return pr
}

// prAt is a PR with no head sha and the given head commit date (the
// plan-candidate fixtures from before plan-sets).
func prAt(headISO string, comments ...model.Comment) *model.PR {
	pr := makePR(comments, "")
	pr.Commits.Nodes[0].Commit.CommittedDate = at(headISO)
	return pr
}

func digest(stack, pid string, destroys, run int, window string) string {
	body := fmt.Sprintf("### plan `%s`\n\nPlan-id: `%s`\nPlan: 0 to add, 2 to change, %d to destroy.\n", stack, pid, destroys)
	if window != "" {
		body += fmt.Sprintf("Window (routing live): **%s**\n", window)
	}
	return body + fmt.Sprintf("https://github.com/acme/infra/actions/runs/%d\n", run)
}

type member struct {
	stack    string
	pid      string
	destroys int
}

func planSet(head string, members ...member) string {
	var ms []map[string]any
	total := 0
	for _, m := range members {
		ms = append(ms, map[string]any{"stack": m.stack, "plan_id": m.pid, "destroys": m.destroys})
		total += m.destroys
	}
	meta, _ := json.Marshal(map[string]any{"set_id": setID, "head_sha": head, "members": ms, "skipped": []string{}})
	return fmt.Sprintf("### plan-set\n%d change-bearing stack(s), %d destroy(s) in total.\n"+
		"To approve every stack in this set, in the order above: `/approve %s`\n<!-- plan-set: %s -->",
		len(members), total, setID, meta)
}

func ceremony(head, window string) string {
	w := "Window: non-disruptive: executes as soon as the `/approve` validates."
	if window == "disruptive" {
		w = "Window: **disruptive**: executes only when the `/approve` lands inside the maintenance window (23:00 to 23:28 ET)."
	}
	return fmt.Sprintf("### ceremony `ceremonies/2026-10-22-tag-queue.sh`\n\nCeremony-id: `%s`\nHead: `%s`\nSummary: tag a queue\n"+
		"Accounts: `main` = nonprod (123456789012)\n%s\n\nDry-run: **passed**\n\nTo approve: `/approve %s`", cid, head, w, cid)
}

func approveC(pid, iso string) model.Comment { return commentBy("carol", "/approve "+pid, iso) }

func outcome(heading, pid, label string) model.Comment {
	return comment(fmt.Sprintf("## %s\n\nStack: `x`\n%s: `%s`\n", heading, label, pid))
}

func check(t *testing.T, name string, cond bool) {
	t.Helper()
	if !cond {
		t.Errorf("FAIL - %s", name)
	}
}

func byStack(cands []Candidate) map[string]Candidate {
	out := map[string]Candidate{}
	for _, c := range cands {
		out[c.Stack] = c
	}
	return out
}

func liveOf(cands []Candidate) []Candidate {
	var out []Candidate
	for _, c := range cands {
		if c.State == StateLive {
			out = append(out, c)
		}
	}
	return out
}

func setOf(cands []Candidate) Candidate {
	for _, c := range cands {
		if c.Set {
			return c
		}
	}
	return Candidate{}
}

// ---- hand-run digests ----

const (
	skipNotice = "### plan `mgmt/sso` — skipped by CI\n" +
		"This stack's provider uses a credential path the apply workflow's OIDC cannot follow. Hand-apply path: ..."
	ciDigest = "### plan `prod/07-warehouse`\nPlan-id: `" + idB + "`\n" +
		"Plan: 1 to add, 0 to change, 0 to destroy.\nhttps://github.com/acme/infra/actions/runs/100"
)

var handDigest = "## Plan digest (hand-run under alice's SSO, device-code delegation)\n\n" +
	"**Plan-id:** `" + idC + "`\n**Summary:** `Plan: 4 to add, 0 to change, 0 to destroy.`\n\n" +
	"binary preserved at `terraform/live/mgmt/sso/plan.binary` in that worktree.\n" +
	"**To approve:** reply `/approve " + idC + "` — 0 destroys."

func TestHandRunDigests(t *testing.T) {
	cfg := testCfg().TerraformApprove
	const head = "2026-06-11T15:55:37Z"
	ex := func(comments ...model.Comment) []Candidate {
		return ExtractPlanCandidates(prAt(head, comments...), cfg)
	}

	out := ex(commentAt(skipNotice, "2026-06-11T16:03:56Z"), commentAt(handDigest, "2026-06-11T16:21:56Z"))
	check(t, "single candidate", len(out) == 1)
	check(t, "stack from skip notice / path", out[0].Stack == "mgmt/sso")
	check(t, "hand-run (shown, not approvable)", out[0].State == StateHandRun)
	check(t, "bolded Plan-id parsed", out[0].PlanID == idC)
	check(t, "destroys parsed from bolded summary", out[0].Destroys != nil && *out[0].Destroys == 0)

	states := map[string]string{}
	for _, c := range ex(commentAt(ciDigest, "2026-06-11T16:00:00Z"), commentAt(skipNotice, "2026-06-11T16:01:00Z"),
		commentAt(handDigest, "2026-06-11T16:10:00Z")) {
		states[c.Stack] = c.State
	}
	check(t, "mixed: CI digest live", states["prod/07-warehouse"] == StateLive)
	check(t, "mixed: hand digest exempt from run-recency", states["mgmt/sso"] == StateHandRun)

	out = ex(commentAt(skipNotice, "2026-06-11T14:00:00Z"), commentAt(handDigest, "2026-06-11T14:10:00Z"))
	check(t, "pre-head hand digest superseded", out[0].State == StateSuperseded)

	out = ex(commentAt(skipNotice, "2026-06-11T16:00:00Z"), commentAt(handDigest, "2026-06-11T16:10:00Z"),
		commentAt(skipNotice, "2026-06-11T16:20:00Z"))
	check(t, "later skip notice wins", out[0].State == StateSkipped)

	hand2 := strings.ReplaceAll(handDigest, idC, idD)
	out = ex(commentAt(skipNotice, "2026-06-11T16:00:00Z"), commentAt(handDigest, "2026-06-11T16:10:00Z"),
		commentAt(hand2, "2026-06-11T16:20:00Z"))
	check(t, "newest hand digest wins", out[0].PlanID == idD && out[0].State == StateHandRun)

	noPath := "## Plan digest (hand-run)\n**Plan-id:** `" + idE + "`\n`Plan: 1 to add, 0 to change, 0 to destroy.`"
	out = ex(commentAt(skipNotice, "2026-06-11T16:00:00Z"), commentAt(noPath, "2026-06-11T16:10:00Z"))
	check(t, "no-path digest -> unique skipped stack", out[0].Stack == "mgmt/sso" && out[0].State == StateHandRun)

	check(t, "unattributable digest dropped", len(ex(commentAt(noPath, "2026-06-11T16:10:00Z"))) == 0)

	out = ex(commentAt(skipNotice, "2026-06-11T16:00:00Z"), commentAt(handDigest, "2026-06-11T16:10:00Z"),
		commentAt("/approve "+idC, "2026-06-11T16:20:00Z"))
	check(t, "/approve on hand digest detected", out[0].Approved)

	out = ex(commentAt(ciDigest, "2026-06-11T16:00:00Z"))
	check(t, "CI digest unchanged", out[0].State == StateLive && out[0].Stack == "prod/07-warehouse" &&
		out[0].PlanID == idB && out[0].RunID == 100)

	// a combined multi-stack bootstrap digest: two skipped stacks answered
	// by ONE comment listing both, with "->" glue and no "Plan-id:" label
	npSkip := "### plan `nonprod/01-security-hub` — skipped by CI\nbootstrap-tier provider; hand-apply path ..."
	pSkip := "### plan `prod/01-security-hub` — skipped by CI\nbootstrap-tier provider; hand-apply path ..."
	combined := "## Bootstrap plan-ids (computed locally)\n\n- **nonprod/01-security-hub** -> `" + idA +
		"`\n- **prod/01-security-hub** -> `" + idF + "`\n"
	out = ex(commentAt(npSkip, "2026-06-17T16:00:00Z"), commentAt(pSkip, "2026-06-17T16:01:00Z"),
		commentAt(combined, "2026-06-17T16:10:00Z"))
	by := byStack(out)
	check(t, "combined: two candidates", len(out) == 2)
	check(t, "combined: nonprod hash attributed", by["nonprod/01-security-hub"].PlanID == idA && by["nonprod/01-security-hub"].State == StateHandRun)
	check(t, "combined: prod hash attributed", by["prod/01-security-hub"].PlanID == idF && by["prod/01-security-hub"].State == StateHandRun)

	loose := "random mention of foo/bar and a hash `" + strings.Repeat("9", 64) + "`"
	check(t, "combined: unskipped stack/hash ignored", len(ex(commentAt(loose, "2026-06-17T16:10:00Z"))) == 0)
}

// ---- plan-sets and ceremonies ----

func twoStacks() []model.Comment {
	return []model.Comment{
		comment(digest("nonprod/15-window-canary", idA, 0, 200, "disruptive · NOT revertible")),
		comment(digest("prod/07-warehouse", idB, 1, 200, "non-disruptive")),
		comment(planSet(headSHA, member{"nonprod/15-window-canary", idA, 0}, member{"prod/07-warehouse", idB, 1})),
	}
}

func TestPlanSets(t *testing.T) {
	cfg := testCfg().TerraformApprove
	two := twoStacks()
	out := ExtractPlanCandidates(makePR(two, headSHA), cfg)
	live := liveOf(out)
	check(t, "the set is the ONLY live candidate", len(live) == 1 && live[0].PlanID == setID && live[0].Set)
	var memberStates []string
	for _, c := range out {
		if !c.Set {
			memberStates = append(memberStates, c.State)
		}
	}
	check(t, "members show as in plan-set", len(memberStates) == 2 && memberStates[0] == StateInPlanSet && memberStates[1] == StateInPlanSet)
	check(t, "destroys = sum of members", live[0].Destroys != nil && *live[0].Destroys == 1)
	check(t, "label names the set", live[0].Stack == "plan-set (2 stacks)")
	v, ok := PlanVerdicts(makePR(two, headSHA))[setID]
	check(t, "disruptive if any member is", ok && IsDisruptive(v))
	for _, c := range out {
		if c.State == StateInPlanSet {
			check(t, "member label tells you to approve the set", strings.Contains(PlanStateLabel(c, false, cfg), "approve the set"))
			break
		}
	}

	stale := append(two[:2:2], comment(planSet(oldSHA, member{"nonprod/15-window-canary", idA, 0}, member{"prod/07-warehouse", idB, 1})))
	check(t, "plan-set for an OLD head is ignored", setOf(ExtractPlanCandidates(makePR(stale, headSHA), cfg)).Set == false)

	one := []model.Comment{comment(digest("prod/09-x", idA, 0, 200, "")), comment(planSet(headSHA, member{"prod/09-x", idA, 0}))}
	live = liveOf(ExtractPlanCandidates(makePR(one, headSHA), cfg))
	check(t, "single-member set: the bare plan-id stays the candidate", len(live) == 1 && live[0].PlanID == idA)

	applied := append(append([]model.Comment{}, two...),
		comment("## Apply SUCCESS — `nonprod/15-window-canary` (member 1/2)\n\nPlan-id: `"+idA+"`"),
		comment("## Window apply VERIFIED — `prod/07-warehouse` (member 2/2)\n\nPlan-id: `"+idB+"`"))
	check(t, "plan-set applied once every member applied", setOf(ExtractPlanCandidates(makePR(applied, headSHA), cfg)).Applied)
}

func TestCeremonies(t *testing.T) {
	cfg := testCfg().TerraformApprove
	out := ExtractPlanCandidates(makePR([]model.Comment{comment(ceremony(headSHA, ""))}, headSHA), cfg)
	check(t, "ceremony digest is a live candidate", len(out) == 1 && out[0].State == StateLive && out[0].PlanID == cid &&
		out[0].Destroys != nil && *out[0].Destroys == 0 && out[0].Ceremony)
	out = ExtractPlanCandidates(makePR([]model.Comment{comment(ceremony(oldSHA, ""))}, headSHA), cfg)
	check(t, "ceremony digest for an old head is superseded", out[0].State == StateSuperseded)
	fail := "### ceremony `ceremonies/2026-10-22-tag-queue.sh`: dry-run FAILED (not approvable)\n\nsha256: `" + cid + "`"
	out = ExtractPlanCandidates(makePR([]model.Comment{comment(fail)}, headSHA), cfg)
	check(t, "failed dry-run is not approvable", out[0].State == StateDryRunFailed && strings.Contains(PlanStateLabel(out[0], false, cfg), "FAILED"))
	done := []model.Comment{comment(ceremony(headSHA, "")), comment("## Apply SUCCESS (ceremony)\n\nCeremony: `x`\nCeremony-id: `" + cid + "`")}
	check(t, "ceremony applied", ExtractPlanCandidates(makePR(done, headSHA), cfg)[0].Applied)
	v, ok := PlanVerdicts(makePR([]model.Comment{comment(ceremony(headSHA, "disruptive"))}, headSHA))[cid]
	check(t, "ceremony window verdict parsed", ok && IsDisruptive(v) && v.Ceremony)

	nci := "### plan `prod/01-x` — not CI-planned\nAccount `prod` has `ci: false`."
	out = ExtractPlanCandidates(makePR([]model.Comment{comment(nci)}, headSHA), cfg)
	check(t, "not CI-planned notice parsed", out[0].State == StateNotCIPlanned && strings.Contains(PlanStateLabel(out[0], false, cfg), "registry"))
}

func TestCeremonyWindow(t *testing.T) {
	w := New(testCfg().TerraformApprove, nil)
	check(t, "23:10 ET (EDT) is inside the ceremony window", w.inCeremonyWindow(at("2026-10-23T03:10:00Z")))
	check(t, "23:30 ET is outside", !w.inCeremonyWindow(at("2026-10-23T03:30:00Z")))
	check(t, "15:00 ET is outside", !w.inCeremonyWindow(at("2026-10-22T19:00:00Z")))
	w2 := New(config.DefaultTerraformApprove(), nil)
	check(t, "no ceremony window configured: never inside", !w2.inCeremonyWindow(at("2026-10-23T03:10:00Z")))
}

// ---- apply outcomes other than success (resume semantics) ----

func TestApplyOutcomes(t *testing.T) {
	cfg := testCfg().TerraformApprove
	two := twoStacks()
	ex := func(cs []model.Comment) []Candidate { return ExtractPlanCandidates(makePR(cs, headSHA), cfg) }
	setThread := append(append([]model.Comment{}, two...), approveC(setID, "2026-10-22T19:00:00Z"),
		comment("## Apply SUCCESS — `nonprod/15-window-canary` (member 1 of 2)\n\nPlan-id: `"+idA+"`"),
		outcome("Apply FAILURE — `prod/07-warehouse` (member 2 of 2)", idB, "Plan-id"))
	s := setOf(ex(setThread))
	check(t, "set: a member FAILURE after the set /approve marks the set failed", s.Outcome != "" && strings.Contains(s.Outcome, "FAILED") && !s.Applied)
	check(t, "set label says re-post resumes", strings.Contains(PlanStateLabel(s, false, cfg), "resume"))
	check(t, "set progress counted", s.Progress == [2]int{1, 2})
	for _, c := range ex(setThread) {
		if !c.Set && c.PlanID == idA {
			check(t, "applied member labelled in plan-set — applied", PlanStateLabel(c, false, cfg) == "in plan-set — applied")
		}
	}
	s = setOf(ex(append(append([]model.Comment{}, setThread...), approveC(setID, "2026-10-22T19:30:00Z"))))
	check(t, "re-posting the set /approve clears the failure", s.Outcome == "" && strings.Contains(PlanStateLabel(s, false, cfg), "1/2 applied"))
	s = setOf(ex(append(append([]model.Comment{}, setThread...), approveC(setID, "2026-10-22T19:00:00Z"),
		comment("## Apply SUCCESS — `prod/07-warehouse` (member 2 of 2)\n\nPlan-id: `"+idB+"`"))))
	check(t, "resumed set that succeeds reads applied", s.Applied && s.Outcome == "")

	single := []model.Comment{comment(digest("prod/09-x", idA, 0, 200, "")), approveC(idA, "2026-10-22T19:00:00Z")}
	with := func(c model.Comment) Candidate { return ex(append(append([]model.Comment{}, single...), c))[0] }
	c := with(comment("**Apply refused — the PR head moved after `/approve`** (approved `aaa`, now `bbb`)."))
	check(t, "plain Apply refused attributes to the newest /approve", strings.HasPrefix(c.Outcome, "apply refused"))
	c = with(comment("**Apply refused (maintenance window):** window mode outside the slot."))
	check(t, "maintenance-window refusal surfaces", strings.Contains(c.Outcome, "refused"))
	c = with(outcome("Apply REFUSED — saved plan is stale (nothing applied) — `prod/09-x` (member 1 of 1)", idA, "Plan-id"))
	check(t, "stale-plan refusal says push to re-plan", strings.Contains(c.Outcome, "re-plan"))
	c = with(outcome("Window apply FAILED verification — AUTO-REVERTED (the PR stays unmerged) — `prod/09-x` (member 1 of 1)", idA, "Plan-id"))
	check(t, "auto-reverted window apply surfaces", strings.Contains(c.Outcome, "AUTO-REVERTED"))
	c = with(outcome("Window apply FAILED verification — NOT reverted, human paged — `x`", idA, "Plan-id"))
	check(t, "not-reverted window failure surfaces", strings.Contains(c.Outcome, "NOT reverted"))
	c = with(outcome("Window apply VERIFIED — `prod/09-x` (member 1 of 1)", idA, "Plan-id"))
	check(t, "window VERIFIED is applied, no outcome", c.Applied && c.Outcome == "")
	c = ex([]model.Comment{comment(ceremony(headSHA, "")), approveC(cid, "2026-10-22T19:00:00Z"), outcome("Ceremony FAILED (exit 1)", cid, "Ceremony-id")})[0]
	check(t, "ceremony FAILED surfaces", strings.Contains(c.Outcome, "ceremony FAILED"))
	c = ex([]model.Comment{comment(ceremony(headSHA, "")), approveC(cid, "2026-10-22T19:00:00Z"), outcome("Ceremony NOT RUN", cid, "Ceremony-id")})[0]
	check(t, "ceremony NOT RUN surfaces", strings.Contains(c.Outcome, "NOT RUN"))

	hist := append(append([]model.Comment{}, two...), approveC(setID, "2026-10-22T19:00:00Z"),
		outcome("Apply FAILURE — `nonprod/15-window-canary` (member 1 of 2)", idA, "Plan-id"))
	check(t, "a failed set is not /approve posted", PlanStateLabel(setOf(ex(hist)), false, cfg) != "/approve posted")

	// the apply workflow's reports are never hand-run digests
	skipnote := comment("### plan `mgmt/sso` — skipped by CI\nnope")
	psum := comment("## Plan-set APPLIED (1/1) — apply-gate stays pending\n\nApproved: `" + idB + "`\n| 1 | `prod/07-warehouse` | Plan-id: `" + idB + "` |")
	up := byStack(ex([]model.Comment{skipnote, comment(digest("prod/07-warehouse", idB, 0, 200, "")), approveC(idB, "2026-10-22T19:00:00Z"), psum}))["mgmt/sso"]
	check(t, "plan-set summary is not attributed to a skipped stack", up.State == StateSkipped && up.PlanID == "")

	for _, word := range []string{"SKIPPED", "CANCELLED"} {
		c := with(outcome("Apply "+word+" — `prod/09-x` (member 1 of 1)", idA, "Plan-id"))
		check(t, "Apply "+word+": did not run", strings.Contains(c.Outcome, "did not run"))
	}
	incomplete := comment("## Plan-set INCOMPLETE (0/2 applied) — apply-gate stays pending\n\nApproved: `" + setID +
		"` · Run: https://github.com/acme/infra/actions/runs/1\n\n| order | stack | plan-id | result |\n|---|---|---|---|\n| 1 | `nonprod/15-window-canary` | `aaaaaaaaaaaa…` | not run |")
	s = setOf(ex(append(append([]model.Comment{}, two...), approveC(setID, "2026-10-22T19:00:00Z"),
		outcome("Apply SKIPPED — `nonprod/15-window-canary` (member 1 of 2)", idA, "Plan-id"), incomplete)))
	check(t, "SKIPPED + INCOMPLETE: the set reads INCOMPLETE", strings.Contains(PlanStateLabel(s, false, cfg), "INCOMPLETE"))
	s = setOf(ex(append(append([]model.Comment{}, two...), approveC(setID, "2026-10-22T19:00:00Z"), incomplete, approveC(setID, "2026-10-22T20:00:00Z"))))
	check(t, "a re-posted /approve clears INCOMPLETE", PlanStateLabel(s, false, cfg) == "/approve posted")

	for _, rf := range []struct{ body, want string }{
		{"**Ceremony refused: the approval did not re-validate at execution time.**\n\nwhy", "ceremony refused"},
		{"**Window item refused before apply (P0):** base moved. Nothing was applied; the item stays queued.", "window item refused before apply"},
		{"**Dropped from the maintenance-window queue (night of 2026-10-22):** the approval no longer validates\n\nApprove the current digest again to re-queue it.",
			"dropped from the maintenance-window queue"},
	} {
		c := with(comment(rf.body))
		check(t, "refusal read: "+rf.want, strings.HasPrefix(c.Outcome, rf.want))
	}
	check(t, "refusal labels drop the markdown",
		refusalLabel("**Apply refused — the PR head moved after `/approve`** (approved `aaa`, now `bbb`).") ==
			"apply refused — the PR head moved after /approve (approved aaa, now bbb).")
	nd := makePR([]model.Comment{comment(ceremony(headSHA, "")), approveC(cid, "2026-10-22T19:00:00Z"),
		comment("**/approve not dispatched:** the ceremony approval validated, but firing `ceremony-approved` failed.")}, headSHA)
	_, refused := ApproveRefusals(nd, cfg)[cid]
	check(t, "/approve not dispatched is a refusal", refused)
}

func TestHeadPlanRuns(t *testing.T) {
	cfg := testCfg().TerraformApprove
	pr := makePR([]model.Comment{comment(digest("prod/09-x", idA, 0, 100, ""))}, headSHA)
	pr.Set(HeadPlanRuns, []int64{200})
	check(t, "digest from a run that didn't plan the head: superseded", ExtractPlanCandidates(pr, cfg)[0].State == StateSuperseded)
	pr.Set(HeadPlanRuns, []int64{100})
	check(t, "digest from the head's own run: live", ExtractPlanCandidates(pr, cfg)[0].State == StateLive)
	pr.Set(HeadPlanRuns, []int64{})
	check(t, "head runs unknown (empty): nothing retired on it", ExtractPlanCandidates(pr, cfg)[0].State == StateLive)

	w := New(cfg, nil)
	gh := fake.New("alice")
	gh.Runs = map[string][]model.WorkflowRun{headSHA: {
		{ID: 200, Path: ".github/workflows/plan.yml"}, {ID: 100, Path: ".github/workflows/preflight.yml"}}}
	pr = makePR([]model.Comment{comment(digest("prod/09-x", idA, 0, 100, ""))}, headSHA)
	w.EnrichDetail(context.Background(), gh, pr, nil)
	runs, _ := pr.Get(HeadPlanRuns).([]int64)
	check(t, "EnrichDetail keeps only the plan workflow's runs for the head", len(runs) == 1 && runs[0] == 200)
	check(t, "...and the old-head digest reads superseded", ExtractPlanCandidates(pr, cfg)[0].State == StateSuperseded)
	gh.Fail["workflow_runs"] = errors.New("GET -> 403: Resource not accessible")
	pr = makePR([]model.Comment{comment(digest("prod/09-x", idA, 0, 100, ""))}, headSHA)
	w.EnrichDetail(context.Background(), gh, pr, nil)
	check(t, "an Actions error leaves the digest alone", pr.Get(HeadPlanRuns) == nil && ExtractPlanCandidates(pr, cfg)[0].State == StateLive)
}

func TestVerdictsAndGrammar(t *testing.T) {
	cfg := testCfg().TerraformApprove
	v := PlanVerdicts(makePR([]model.Comment{
		comment(digest("nonprod/15-window-canary", idA, 0, 200, "disruptive · revertible")),
		comment(digest("prod/07-warehouse", idB, 0, 200, "non-disruptive")),
		comment(planSet(headSHA, member{"nonprod/15-window-canary", idA, 0}, member{"prod/07-warehouse", idB, 0}))}, headSHA))[setID]
	check(t, "a multi-stack set is never revertible", strings.Contains(v.Verdict, "NOT revertible"))
	v = PlanVerdicts(makePR([]model.Comment{comment(digest("prod/09-x", idA, 0, 200, "disruptive · revertible")),
		comment(planSet(headSHA, member{"prod/09-x", idA, 0}))}, headSHA))[setID]
	check(t, "a single-member set keeps its member's verdict", v.Verdict == "disruptive · revertible")

	w := New(cfg, testCfg().MaintenanceWindow)
	check(t, "after the cutoff a plain /approve queues for tomorrow night",
		w.queueNightPhrase(at("2026-10-23T01:30:00Z")) == "tomorrow night's" && w.queueNightPhrase(at("2026-10-22T22:00:00Z")) == "tonight's")

	re := CommandRE(cfg)
	check(t, "'/approve<TAB><id>' is not an /approve", !re.MatchString("/approve\t"+idA))
	check(t, "'/approve <id>' is", re.MatchString("/approve "+idA))

	custom := config.DefaultTerraformApprove()
	custom.ApproveCommand, custom.ConfirmDestroysFlag, custom.OutsideWindowFlag = "/ship", "--destroys", "--emergency"
	cre := CommandRE(custom)
	check(t, "the grammar follows the config", cre.MatchString(`/ship `+idA+` --destroys=2 --emergency="why not"`) && !cre.MatchString("/approve "+idA))
	check(t, "the refusal reply follows the config", RefusedRE(custom).MatchString("**/ship refused:** no"))

	// the destroy count comes from the LAST summary line
	twoLines := "### plan `prod/07-warehouse`\nPlan-id: `" + idA + "`\nearlier: Plan: 0 to add, 0 to change, 0 to destroy.\nPlan: 0 to add, 1 to change, 2 to destroy.\n"
	c := ExtractPlanCandidates(makePR([]model.Comment{comment(twoLines)}, headSHA), cfg)[0]
	check(t, "destroy count from the LAST summary line", c.Destroys != nil && *c.Destroys == 2)
}

func TestApprovedAndRefusals(t *testing.T) {
	cfg := testCfg().TerraformApprove
	approvedFlag := func(body string) bool {
		pr := makePR([]model.Comment{comment(digest("s/x", idA, 0, 200, "")), commentBy("carol", body, "2026-10-22T18:20:00Z")}, headSHA)
		return ExtractPlanCandidates(pr, cfg)[0].Approved
	}
	check(t, "exact /approve counts", approvedFlag("/approve "+idA))
	check(t, "/approve with flags counts", approvedFlag(`/approve `+idA+` --outside-window="restore the broken route"`))
	check(t, "/approve + chatter does not count", !approvedFlag("/approve "+idA+" please"))
	check(t, "leading whitespace does not count", !approvedFlag(" /approve "+idA))

	refusedPR := makePR([]model.Comment{comment(digest("s/x", idA, 0, 200, "")),
		commentBy("carol", "/approve "+idA, "2026-10-22T18:20:00Z"),
		commentAt("**/approve refused:** `s/x` holds a slot", "2026-10-22T18:21:00Z")}, headSHA)
	ref := ApproveRefusals(refusedPR, cfg)
	check(t, "refusal attributed to the /approve before it", ref[idA] == "**/approve refused:** `s/x` holds a slot")
	cand := ExtractPlanCandidates(refusedPR, cfg)[0]
	check(t, "refused label", strings.Contains(PlanStateLabel(cand, true, cfg), "REFUSED"))
	again := makePR(append(append([]model.Comment{}, refusedPR.Comments.Nodes...),
		commentBy("carol", "/approve "+idA, "2026-10-22T18:30:00Z")), headSHA)
	check(t, "a fresh /approve clears the refusal", len(ApproveRefusals(again, cfg)) == 0)
	human := makePR([]model.Comment{commentBy("carol", "/approve "+idA, "2026-10-22T18:20:00Z"),
		commentBy("mallory", "**/approve refused:** lol", "2026-10-22T18:21:00Z")}, headSHA)
	check(t, "only the bot's refusals count", len(ApproveRefusals(human, cfg)) == 0)
}

func TestGateAndClassify(t *testing.T) {
	cfg := testCfg().TerraformApprove
	gated := func(state, desc string) *model.PR {
		pr := &model.PR{Repo: infra, Number: 3588, Title: "applied"}
		pr.Commits.Nodes = append(pr.Commits.Nodes, struct {
			Commit model.Commit `json:"commit"`
		}{model.Commit{Rollup: &model.Rollup{}}})
		pr.Commits.Nodes[0].Commit.Rollup.Contexts.Nodes = []model.CheckContext{{}, {Context: "terraform/apply-gate", State: state, Description: desc}}
		return pr
	}
	applied := gated("SUCCESS", "applied: plan d1c66897d795 (1 stack(s)) via run 39870164173")
	check(t, "apply-gate applied: -> applied", GateApplied(applied, cfg))
	check(t, "ceremony executed -> applied", GateApplied(gated("SUCCESS", "ceremony executed: 1a2b3c via run 9"), cfg))
	check(t, "nothing to apply is not applied", !GateApplied(gated("SUCCESS", "no terraform/live changes; nothing to apply"), cfg))
	check(t, "pending is not applied", !GateApplied(gated("PENDING", "1 change-bearing plan(s) await /approve + apply"), cfg))
	check(t, "no rollup: not applied", !GateApplied(&model.PR{}, cfg))

	w := New(cfg, nil)
	c := w.Classify(applied, nil, "alice", false)
	check(t, "applied + no review: the applied chip", c != nil && c.Label == AppliedLabel && c.NeedsYou)
	ok := gated("SUCCESS", "applied: x")
	ok.ReviewDecision = "APPROVED"
	check(t, "applied + approved: left to the core", w.Classify(ok, nil, "alice", false) == nil)
	check(t, "detail view reads the gate from the listed row", w.Classify(&model.PR{}, applied, "alice", false).Label == AppliedLabel)

	requeue := &model.PR{Repo: infra, Number: 3609, ReviewDecision: "REVIEW_REQUIRED"}
	requeue.Set("window", reapprove(true))
	c = w.Classify(requeue, nil, "alice", false)
	check(t, "its chip says re-planned", c != nil && c.Label == RequeueLabel)
	requeue.Opinions.Nodes = []model.Review{{Author: model.Actor{Login: "alice"}, State: "APPROVED"}}
	check(t, "re-planned beats an old review approval", w.Classify(requeue, nil, "alice", false).Label == RequeueLabel)
	movedOn := &model.PR{Repo: infra, Number: 3609, ReviewDecision: "REVIEW_REQUIRED"}
	movedOn.Set("window", reapprove(false))
	check(t, "superseded (newer plan in flight): plain review chip", w.Classify(movedOn, nil, "alice", false) == nil)
}

type reapprove bool

func (r reapprove) NeedsReapprove() bool { return bool(r) }
