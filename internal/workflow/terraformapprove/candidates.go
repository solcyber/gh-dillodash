package terraformapprove

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Candidate states.
const (
	StateLive         = "live"
	StateSuperseded   = "superseded"
	StateNoChanges    = "no changes"
	StateSkipped      = "skipped by CI"
	StateNotCIPlanned = "not CI-planned"
	StateDryRunFailed = "dry-run failed"
	StateHandRun      = "hand-run"
	StateInPlanSet    = "in plan-set"
)

// Candidate is one stack's plan state as the comment thread tells it.
type Candidate struct {
	Stack    string
	PlanID   string // "" for a notice without a plan
	Destroys *int   // nil when the digest's summary line could not be read
	RunID    int64  // the digest's Actions run; 0 for hand-run and ceremony digests
	Hand     bool   // a hand-run digest (no plan.yml run)
	Created  time.Time
	State    string
	Ceremony bool
	Head     string // a ceremony digest's head sha
	Window   string // a ceremony digest's window word (disruptive, non-disruptive)
	Approved bool   // an /approve of this id is on the thread
	Applied  bool   // an apply SUCCESS / window VERIFIED names this id
	Outcome  string // the newest non-success outcome after the last /approve, or ""
	// Set marks the synthetic plan-set candidate of a multi-stack PR.
	Set      bool
	Members  []SetMember
	Progress [2]int // set only: members applied, members in total
}

// SetMember is one stack of a plan-set.
type SetMember struct {
	Stack    string `json:"stack"`
	PlanID   string `json:"plan_id"`
	Destroys *int   `json:"destroys"`
}

// PlanSet is the plan bot's plan-set trailer for the PR's current head.
type PlanSet struct {
	SetID   string
	HeadSHA string
	Members []SetMember
	Created time.Time
}

// Verdict is a digest's maintenance-window verdict.
type Verdict struct {
	Verdict  string // "disruptive · revertible", "non-disruptive", ...
	Shadow   bool   // the advisory wording from before routing went live
	Ceremony bool
}

// HeadPlanRuns is the Ext key EnrichDetail stores the ids of the plan
// workflow's runs for the PR's head under ([]int64).
const HeadPlanRuns = "head_plan_runs"

type event struct {
	idx   int
	label string
}

// refusalLabel is a refusal's first line as an outcome label: "apply
// refused — the PR head moved after /approve (approved aaa, now bbb)."
func refusalLabel(body string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	t := strings.ReplaceAll(first, "**", "")
	t = strings.ReplaceAll(t, "`", "")
	t = strings.Trim(t, " :")
	r := []rune(t)
	if len(r) > 0 {
		r[0] = []rune(strings.ToLower(string(r[0])))[0]
	}
	if len(r) > 100 {
		r = r[:100]
	}
	return string(r)
}

func intp(n int) *int { return &n }

// ExtractPlanCandidates reconstructs per-stack plan state from the PR's
// comment thread (oldest first).
//
// Per stack, the newest plan event wins: a digest (plan-id + destroy
// count), a "no changes" notice, or a "skipped by CI" notice. Across
// stacks, digests are superseded by run recency: the plan workflow
// re-plans every stack the PR still touches on every push, so a digest
// from an older run than the newest digest anywhere on the PR means the
// stack was dropped from the PR. A digest posted before the current head
// commit is dead no matter what, and when the caller stored the head's
// plan runs (HeadPlanRuns), a digest from any other run planned an older
// head.
//
// Hand-run digests (a Plan-id but no "### plan `stack`" header, answering
// a "skipped by CI" notice) are attributed to their stack and exempt from
// the run-recency rule, but the validator no longer accepts them: they
// read as "hand-run". Ceremony digests are candidates of their own.
//
// Only "live" entries are valid approve candidates. The result is sorted
// by stack, with a multi-stack PR's plan-set appended last.
func ExtractPlanCandidates(pr *model.PR, cfg *config.TerraformApproveConfig) []Candidate {
	comments := pr.Comments.Nodes
	cmdRE := CommandRE(cfg)
	plans := map[string]*Candidate{}
	approved := map[string]bool{}
	applied := map[string]bool{}
	// id -> the newest /approve or apply outcome per plan/set/ceremony
	// id. A later /approve clears an earlier failure (re-posted = in
	// flight again); a later SUCCESS clears it.
	events := map[string]event{}
	lastApprove := ""
	for idx, c := range comments {
		body := c.Body
		if m := cmdRE.FindStringSubmatch(body); m != nil {
			approved[m[1]] = true
			events[m[1]] = event{idx, ""}
			lastApprove = m[1]
			continue
		}
		outcome := ""
		for _, o := range ApplyOutcomes {
			if o.Re.MatchString(body) {
				outcome = o.Label
				break
			}
		}
		if outcome != "" {
			target := lastApprove
			for _, re := range []*regexp.Regexp{PlanIDRE, CeremonyIDRE, ApprovedIDRE} {
				if m := re.FindStringSubmatch(body); m != nil {
					target = m[1]
					break
				}
			}
			if target != "" {
				events[target] = event{idx, outcome}
			}
			continue
		}
		if ApplyRefusedRE.MatchString(body) && lastApprove != "" {
			events[lastApprove] = event{idx, refusalLabel(body)}
			continue
		}
		mID := PlanIDRE.FindStringSubmatch(body)
		// "Window apply VERIFIED" is a window-mode apply that passed its
		// post-apply verification: applied, like SUCCESS.
		if mID != nil && (strings.Contains(body, "Apply SUCCESS") || strings.Contains(body, "Window apply VERIFIED")) {
			applied[mID[1]] = true
			events[mID[1]] = event{idx, ""}
			continue
		}
		if mCID := CeremonyIDRE.FindStringSubmatch(body); mCID != nil && strings.Contains(body, "Apply SUCCESS (ceremony)") {
			applied[mCID[1]] = true
			events[mCID[1]] = event{idx, ""}
			continue
		}
		mCer := CeremonyDigestRE.FindStringSubmatch(body)
		mCfail := CeremonyFailedRE.FindStringSubmatch(body)
		if mCer != nil || mCfail != nil {
			cand := &Candidate{Hand: true, Created: c.CreatedAt, Ceremony: true}
			if mCer != nil {
				cand.Stack, cand.PlanID, cand.Head = mCer[1], mCer[2], mCer[3]
				cand.Destroys = intp(0)
				cand.State = StateLive
				if mw := CeremonyWindowRE.FindStringSubmatch(body); mw != nil {
					cand.Window = mw[1]
				}
			} else {
				cand.Stack = mCfail[1]
				cand.State = StateDryRunFailed
			}
			plans[cand.Stack] = cand
			continue
		}
		if mN := PlanNoticeRE.FindStringSubmatch(body); mN != nil {
			plans[mN[1]] = &Candidate{Stack: mN[1], State: mN[2]}
			continue
		}
		stack := ""
		if mS := PlanStackRE.FindStringSubmatch(body); mS != nil {
			stack = mS[1]
		}
		hand := false
		// the apply workflow's own reports carry Plan-id lines but are
		// never hand-run digests
		if stack == "" && NotADigestRE.MatchString(body) {
			continue
		}
		if stack == "" {
			// No "### plan `stack`" header: a hand-run / bootstrap digest.
			var skipped []string
			for s, p := range plans {
				if p.State == StateSkipped {
					skipped = append(skipped, s)
				}
			}
			sort.Strings(skipped)
			isSkipped := func(s string) bool {
				for _, k := range skipped {
					if k == s {
						return true
					}
				}
				return false
			}
			// (a) a combined multi-stack digest pairs several skipped
			//     stacks with their plan-ids on one line each
			var pairs [][2]string
			for _, m := range PlanPairRE.FindAllStringSubmatch(body, -1) {
				if isSkipped(m[1]) {
					pairs = append(pairs, [2]string{m[1], m[2]})
				}
			}
			if len(pairs) >= 2 || (len(pairs) > 0 && mID == nil) {
				for _, p := range pairs {
					plans[p[0]] = &Candidate{Stack: p[0], PlanID: p[1], Hand: true,
						Created: c.CreatedAt, State: StateLive}
				}
				continue
			}
			// (b) a single hand-run digest: one Plan-id, attributed to the
			//     skipped stack it names, else a live-tree path, else the
			//     only skipped stack
			if mID != nil {
				hand = true
				var named []string
				for _, s := range skipped {
					if strings.Contains(body, s) {
						named = append(named, s)
					}
				}
				mPath := PlanLivePathRE.FindStringSubmatch(body)
				switch {
				case len(named) == 1:
					stack = named[0]
				case mPath != nil:
					stack = mPath[1]
				case len(skipped) == 1:
					stack = skipped[0]
				}
			}
		}
		if stack == "" || mID == nil {
			continue
		}
		cand := &Candidate{Stack: stack, PlanID: mID[1], Hand: hand, Created: c.CreatedAt, State: StateLive}
		// the LAST summary line, as the validator reads it
		if all := PlanDestroysRE.FindAllStringSubmatch(body, -1); len(all) > 0 {
			n, _ := strconv.Atoi(all[len(all)-1][1])
			cand.Destroys = intp(n)
		}
		if mR := PlanRunRE.FindStringSubmatch(body); mR != nil {
			cand.RunID, _ = strconv.ParseInt(mR[1], 10, 64)
		}
		plans[stack] = cand
	}

	// The decisive staleness signal: a digest posted BEFORE the current
	// head commit is dead; if the stack still had changes, a fresher
	// digest would exist.
	var headCommitted time.Time
	if hc := pr.HeadCommit(); hc != nil {
		headCommitted = hc.CommittedDate
	}
	var latestRun int64
	for _, p := range plans {
		if p.State == StateLive && p.RunID > latestRun {
			latestRun = p.RunID
		}
	}
	headRuns := map[int64]bool{}
	if runs, ok := pr.Get(HeadPlanRuns).([]int64); ok {
		for _, r := range runs {
			headRuns[r] = true
		}
	}
	out := make([]Candidate, 0, len(plans)+1)
	for _, p := range plans {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stack < out[j].Stack })
	headOid := pr.HeadRefOid
	for i := range out {
		cand := &out[i]
		if cand.State == StateLive && ((cand.RunID < latestRun && !cand.Hand) ||
			(len(headRuns) > 0 && cand.RunID != 0 && !cand.Hand && !headRuns[cand.RunID]) ||
			(!headCommitted.IsZero() && cand.Created.Before(headCommitted)) ||
			(cand.Ceremony && headOid != "" && cand.Head != "" && cand.Head != headOid)) {
			cand.State = StateSuperseded
		}
		if cand.State == StateLive && cand.Hand && !cand.Ceremony {
			// the validator takes a single plan only from the plan bot's
			// own "### plan" digest: a hand-run plan-id can only bounce
			cand.State = StateHandRun
		}
		cand.Approved = cand.PlanID != "" && approved[cand.PlanID]
		cand.Applied = cand.PlanID != "" && applied[cand.PlanID]
		if ev, ok := events[cand.PlanID]; ok && cand.PlanID != "" && !cand.Applied {
			cand.Outcome = ev.label
		}
	}
	// Multi-stack PRs: with more than one change-bearing stack the set id
	// is the only approvable id; its members stop being candidates.
	if pset := ExtractPlanSet(pr); pset != nil && len(pset.Members) > 1 {
		memberIDs := map[string]bool{}
		for _, m := range pset.Members {
			memberIDs[m.PlanID] = true
		}
		for i := range out {
			if out[i].State == StateLive && memberIDs[out[i].PlanID] {
				out[i].State = StateInPlanSet
			}
		}
		var destroys *int
		sum, known := 0, true
		for _, m := range pset.Members {
			if m.Destroys == nil {
				known = false
				break
			}
			sum += *m.Destroys
		}
		if known {
			destroys = intp(sum)
		}
		nApplied, allApplied := 0, len(memberIDs) > 0
		for id := range memberIDs {
			if applied[id] {
				nApplied++
			} else {
				allApplied = false
			}
		}
		// A set failed when a member's newest event is a failure that came
		// after the set's newest /approve (or a refusal attributed to the set).
		setEv, hasSet := events[pset.SetID]
		setIdx := -1
		if hasSet {
			setIdx = setEv.idx
		}
		var fails []event
		for id := range memberIDs {
			if ev, ok := events[id]; ok && ev.label != "" && ev.idx > setIdx {
				fails = append(fails, ev)
			}
		}
		setOutcome := ""
		if hasSet && setEv.label != "" {
			setOutcome = setEv.label
		} else if len(fails) > 0 {
			sort.Slice(fails, func(i, j int) bool {
				if fails[i].idx != fails[j].idx {
					return fails[i].idx < fails[j].idx
				}
				return fails[i].label < fails[j].label
			})
			setOutcome = fails[len(fails)-1].label
		}
		set := Candidate{
			Stack:    fmt.Sprintf("plan-set (%d stacks)", len(pset.Members)),
			PlanID:   pset.SetID,
			Destroys: destroys,
			Created:  pset.Created,
			State:    StateLive,
			Set:      true,
			Members:  pset.Members,
			Approved: approved[pset.SetID],
			Applied:  allApplied,
			Progress: [2]int{nApplied, len(memberIDs)},
		}
		if !allApplied {
			set.Outcome = setOutcome
		}
		out = append(out, set)
	}
	return out
}

// ExtractPlanSet is the newest plan-set the plan bot posted for the PR's
// CURRENT head, or nil. A set posted for an older head is dead: the bot
// re-plans every push. When the PR carries no HeadRefOid (partial
// payloads), the newest set wins.
func ExtractPlanSet(pr *model.PR) *PlanSet {
	head := pr.HeadRefOid
	var best *PlanSet
	for _, c := range pr.Comments.Nodes {
		m := PlanSetMetaRE.FindStringSubmatch(c.Body)
		if m == nil {
			continue
		}
		var meta struct {
			SetID   string            `json:"set_id"`
			HeadSHA string            `json:"head_sha"`
			Members []json.RawMessage `json:"members"`
		}
		if err := json.Unmarshal([]byte(m[1]), &meta); err != nil {
			continue
		}
		if !HexIDRE.MatchString(meta.SetID) {
			continue
		}
		if head != "" && meta.HeadSHA != "" && meta.HeadSHA != head {
			continue
		}
		ps := &PlanSet{SetID: meta.SetID, HeadSHA: meta.HeadSHA, Created: c.CreatedAt}
		for _, raw := range meta.Members {
			var sm SetMember
			if err := json.Unmarshal(raw, &sm); err == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
				ps.Members = append(ps.Members, sm)
			}
		}
		best = ps
	}
	return best
}

// ApproveRefusals is {plan id: refusal line} for plans whose latest
// /approve the validator turned down ("**/approve refused:** ..." / "not
// routed" / "not dispatched"). A refusal answers the /approve before it; a
// fresh /approve of the same plan clears it. Only the digest bot's
// refusals count.
func ApproveRefusals(pr *model.PR, cfg *config.TerraformApproveConfig) map[string]string {
	cmdRE, refRE := CommandRE(cfg), RefusedRE(cfg)
	refused := map[string]string{}
	last := ""
	for _, c := range pr.Comments.Nodes {
		if m := cmdRE.FindStringSubmatch(c.Body); m != nil {
			last = m[1]
			delete(refused, last)
			continue
		}
		body := strings.TrimSpace(c.Body)
		if last != "" && isBot(c.Author.Login, cfg.DigestBot) && refRE.MatchString(body) {
			first, _, _ := strings.Cut(body, "\n")
			refused[last] = first
		}
	}
	return refused
}

// isBot reports whether login is the configured bot (a prefix match, so
// "github-actions" also covers "github-actions[bot]").
func isBot(login, bot string) bool {
	return bot != "" && strings.HasPrefix(strings.ToLower(login), strings.ToLower(bot))
}

// PlanStateLabel is a candidate's one-line state for the card and the
// pick menu; refused says the validator turned its last /approve down.
func PlanStateLabel(c Candidate, refused bool, cfg *config.TerraformApproveConfig) string {
	cmd := cfg.ApproveCommand
	switch c.State {
	case StateSuperseded:
		label := "SUPERSEDED — do not approve"
		if c.Approved {
			label += " (stale " + cmd + " posted!)"
		}
		return label
	case StateInPlanSet:
		if c.Applied {
			return "in plan-set — applied"
		}
		return "in plan-set — approve the set, not this plan"
	case StateNoChanges:
		return "no changes — no " + cmd + " needed"
	case StateSkipped:
		return "skipped by CI — see the comment"
	case StateNotCIPlanned:
		return "not CI-planned — the account is ci: false in the registry"
	case StateDryRunFailed:
		return "ceremony dry-run FAILED — fix and push"
	case StateHandRun:
		return "hand-run digest — not approvable; plan it in CI"
	}
	if c.Applied {
		return "applied"
	}
	if c.Outcome != "" {
		return c.Outcome
	}
	done, total := c.Progress[0], c.Progress[1]
	if c.Approved && 0 < done && done < total {
		return fmt.Sprintf("%d/%d applied — %s posted", done, total, cmd)
	}
	if c.Approved && refused {
		return cmd + " REFUSED — see the validator's reply"
	}
	if c.Approved {
		return cmd + " posted"
	}
	return "awaiting " + cmd
}

// PlanVerdicts is {plan id: verdict} from the "Window (...): **verdict**"
// line in each digest and the "Window: ..." line in each ceremony digest.
// A plan-set is disruptive if any member is: it borrows the first
// disruptive member's verdict, else any member's; with more than one
// member it is never revertible (it queues for an attended night).
func PlanVerdicts(pr *model.PR) map[string]Verdict {
	out := map[string]Verdict{}
	for _, c := range pr.Comments.Nodes {
		body := c.Body
		mID := PlanIDRE.FindStringSubmatch(body)
		mV := WindowVerdictRE.FindStringSubmatch(body)
		if mID != nil && mV != nil {
			out[mID[1]] = Verdict{Verdict: strings.TrimSpace(mV[2]),
				Shadow: strings.Contains(strings.ToLower(mV[1]), "shadow")}
		}
		if mCer := CeremonyDigestRE.FindStringSubmatch(body); mCer != nil {
			if mw := CeremonyWindowRE.FindStringSubmatch(body); mw != nil {
				out[mCer[2]] = Verdict{Verdict: mw[1], Ceremony: true}
			}
		}
	}
	if pset := ExtractPlanSet(pr); pset != nil {
		var member []Verdict
		for _, m := range pset.Members {
			if v, ok := out[m.PlanID]; ok {
				member = append(member, v)
			}
		}
		var worst []Verdict
		for _, v := range member {
			if IsDisruptive(v) {
				worst = append(worst, v)
			}
		}
		if len(worst) == 0 {
			worst = member
		}
		if len(worst) > 0 {
			v := worst[0]
			if len(pset.Members) > 1 && IsDisruptive(v) {
				v.Verdict = "disruptive · NOT revertible (multi-stack set)"
			}
			out[pset.SetID] = v
		}
	}
	return out
}

// IsDisruptive reads a verdict: "disruptive ..." but not "non-disruptive".
func IsDisruptive(v Verdict) bool {
	t := strings.ToLower(v.Verdict)
	return strings.Contains(t, "disruptive") && !strings.HasPrefix(t, "non")
}

// GateApplied reports whether the head commit's apply-gate status says
// every plan on the PR is applied. Such a PR is open only because it has
// not merged yet, usually for want of a code-owner review.
func GateApplied(pr *model.PR, cfg *config.TerraformApproveConfig) bool {
	hc := pr.HeadCommit()
	if hc == nil || hc.Rollup == nil {
		return false
	}
	for _, n := range hc.Rollup.Contexts.Nodes {
		if n.Context == cfg.GateContext {
			if n.State != "SUCCESS" {
				return false
			}
			for _, p := range cfg.GateDonePrefixes {
				if strings.HasPrefix(n.Description, p) {
					return true
				}
			}
			return false
		}
	}
	return false
}
