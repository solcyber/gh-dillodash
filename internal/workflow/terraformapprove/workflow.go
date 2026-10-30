package terraformapprove

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/markdown"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Name is the workflow's config table name.
const Name = "terraform_approve"

// Chip labels this workflow adds; both want the viewer.
const (
	// AppliedLabel: the apply gate says every plan is applied; the PR
	// merges once reviewed.
	AppliedLabel = "applied, needs review"
	// RequeueLabel: the plan a maintenance-window entry was queued with
	// is dead (a later push re-planned it); the PR needs a fresh approve.
	RequeueLabel = "re-planned since queued"
)

// Reapprover is what the window workflow's annotation (pr.Get("window"))
// implements when the PR's queued plan died and a fresh approve is needed.
// Duck-typed so this package never imports the window package.
type Reapprover interface{ NeedsReapprove() bool }

// Workflow is the terraform approve workflow.
type Workflow struct {
	workflow.Base
	cfg    *config.TerraformApproveConfig
	window *config.MaintenanceWindowConfig // nil = no window workflow
	hooks  *markdown.Hooks
}

func init() {
	workflow.Register(Name, func(ctx *workflow.Context) (workflow.Workflow, error) {
		if ctx.Config == nil || ctx.Config.TerraformApprove == nil {
			return nil, nil
		}
		return New(ctx.Config.TerraformApprove, ctx.Config.MaintenanceWindow), nil
	})
}

// New builds the workflow; window is the maintenance window's config when
// that workflow is enabled (a plain approve of a disruptive plan then
// queues for its night), nil otherwise.
func New(cfg *config.TerraformApproveConfig, window *config.MaintenanceWindowConfig) *Workflow {
	return &Workflow{cfg: cfg, window: window, hooks: markdown.TerraformHooks()}
}

// Name is "terraform_approve".
func (w *Workflow) Name() string { return Name }

// Order places the workflow before the window's.
func (w *Workflow) Order() int { return 20 }

// Config is the workflow's config.
func (w *Workflow) Config() *config.TerraformApproveConfig { return w.cfg }

// BotLogins names the digest bot.
func (w *Workflow) BotLogins() []string { return []string{w.cfg.DigestBot} }

// ChipStyles styles the two labels like "awaiting review".
func (w *Workflow) ChipStyles() map[string]string {
	return map[string]string{AppliedLabel: "chip.await", RequeueLabel: "chip.await"}
}

// NeedsYou counts both labels as needing the viewer.
func (w *Workflow) NeedsYou() []string { return []string{AppliedLabel, RequeueLabel} }

// Classify: "re-planned since queued" when the window workflow says the
// queued plan is dead (that beats an old review approval: the approve is
// what is missing), else "applied, needs review" when the apply gate is
// done and no review decision stands. listed carries what the detail
// query does not (the gate, the window annotation).
func (w *Workflow) Classify(pr, listed *model.PR, me string, localApproved bool) *workflow.Classification {
	ctx := listed
	if ctx == nil {
		ctx = pr
	}
	if r, ok := ctx.Get("window").(Reapprover); ok && r != nil && r.NeedsReapprove() {
		return &workflow.Classification{Label: RequeueLabel, Style: "chip.await", NeedsYou: true}
	}
	if me != "" && pr.MyReviewState(me) == "APPROVED" {
		return nil
	}
	switch pr.ReviewDecision {
	case "APPROVED", "CHANGES_REQUESTED":
		return nil
	}
	if GateApplied(ctx, w.cfg) {
		return &workflow.Classification{Label: AppliedLabel, Style: "chip.await", NeedsYou: true}
	}
	return nil
}

// EnrichDetail stores the ids of the plan workflow's runs for the PR's
// head (HeadPlanRuns), which retire digests a run for an older head
// posted. Advisory: on any error it is left out and the validator stays
// the backstop.
func (w *Workflow) EnrichDetail(ctx context.Context, gh ghapi.Client, pr, listed *model.PR) {
	if pr.HeadRefOid == "" || gh == nil {
		return
	}
	runs, err := gh.WorkflowRuns(ctx, pr.Repo, pr.HeadRefOid)
	if err != nil {
		return
	}
	ids := []int64{}
	suffix := "/workflows/" + w.cfg.PlanWorkflow
	for _, r := range runs {
		if strings.HasSuffix(r.Path, suffix) {
			ids = append(ids, r.ID)
		}
	}
	pr.Set(HeadPlanRuns, ids)
}

// ---- detail card ----

// DetailCards draws the PR's plans in a rounded card: one row per stack
// with its state, plan-id, destroy count and maintenance-window verdict.
func (w *Workflow) DetailCards(app workflow.App, pr, listed *model.PR, width int) []text.Line {
	cands := ExtractPlanCandidates(pr, w.cfg)
	if len(cands) == 0 {
		return nil
	}
	refused := ApproveRefusals(pr, w.cfg)
	verdicts := PlanVerdicts(pr)
	inner := max(20, width-4)
	stackW := 0
	for _, c := range cands {
		stackW = max(stackW, text.Width(c.Stack))
	}
	stackW = min(36, stackW)
	out := []text.Line{scenes.CardTop(glyphs.Ico("shield")+"plans", width)}
	for _, c := range cands {
		isRefused := c.PlanID != "" && refused[c.PlanID] != ""
		label := PlanStateLabel(c, isRefused, w.cfg)
		var glyph, cstyle string
		switch {
		case c.State == StateLive && c.Applied:
			glyph, cstyle = "applied", "chip.plan.done"
		case c.State == StateLive && c.Outcome != "":
			glyph, cstyle = "warn", "chip.plan.warn"
		case c.State == StateLive && c.Approved:
			glyph, cstyle = "shield", "chip.plan.done"
			if isRefused {
				glyph, cstyle = "warn", "chip.plan.warn"
			}
		case c.State == StateLive:
			glyph, cstyle = "live", "chip.plan.live"
		case c.State == StateSuperseded:
			glyph, cstyle = "dead", "chip.plan.dead"
			if c.Approved {
				cstyle = "chip.plan.warn"
			}
		case c.State == StateNoChanges:
			glyph, cstyle = "nochange", "chip.plan.dead"
		default:
			glyph, cstyle = "skipped", "chip.plan.dead"
		}
		live := c.State == StateLive
		iconStyle, stackStyle := "plan.dead", "plan.dead"
		if live {
			iconStyle, stackStyle = "plan.live", "plan.stack"
		}
		segs := []text.Seg{text.S(glyphs.Ico(glyph), iconStyle), text.S(text.Pad(c.Stack, stackW)+"  ", stackStyle)}
		if c.PlanID != "" {
			segs = append(segs, text.S(c.PlanID[:12]+glyphs.G("more")+"  ", "plan.id"))
			switch {
			case c.Destroys == nil:
				segs = append(segs, text.S("destroys ?  ", "muted"))
			case *c.Destroys == 0:
				segs = append(segs, text.S("0 destroys  ", "destroys.0"))
			default:
				segs = append(segs, text.S(fmt.Sprintf("%d destroy%s  ", *c.Destroys, plural(*c.Destroys)), "destroys.n"))
			}
		}
		segs = append(segs, scenes.Chip(label, cstyle)...)
		if v, ok := verdicts[c.PlanID]; ok && c.PlanID != "" && live && !c.Applied {
			// no-break spaces: the verdict wraps as one piece
			t := glyphs.Ico("window") + v.Verdict
			if v.Shadow {
				t += " (advisory)"
			}
			segs = append(segs, text.S("  ", "text"), text.S(strings.ReplaceAll(t, " ", "\u00a0"), markdown.VerdictStyle(v.Verdict)))
		}
		out = append(out, scenes.CardLines(segs, width, inner)...)
	}
	allApplied, anyLive := true, false
	for _, c := range cands {
		if c.State == StateLive {
			anyLive = true
			if !c.Applied {
				allApplied = false
			}
		}
	}
	state := pr.State
	if state == "" {
		state = "OPEN"
	}
	if anyLive && allApplied && state == "OPEN" {
		out = append(out, scenes.CardLines([]text.Seg{
			text.S(glyphs.Ico("info"), "muted"),
			text.S(fmt.Sprintf("applied, but %s won't have it until this merges: ", baseOr(pr.BaseRefName)), "muted"),
			text.S(" a ", "chip.key"), text.S(" submits the review it's waiting for", "muted"),
		}, width, inner)...)
	}
	out = append(out, scenes.CardBottom(width))
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---- approve ----

var digitsRE = regexp.MustCompile(`^[0-9]+$`)

// inCeremonyWindow reports whether now falls in the ceremony window;
// false when none is configured.
func (w *Workflow) inCeremonyWindow(now time.Time) bool {
	cw := w.cfg.CeremonyWindow
	if cw == nil {
		return false
	}
	return cw.Local().InSpan(now, cw.From, cw.To)
}

// queueNightPhrase is whose window a plain approve queues for right now:
// "tonight's" until the window's cutoff, "tomorrow night's" after it.
func (w *Workflow) queueNightPhrase(now time.Time) string {
	local := w.window.Local()
	lt := local.In(now)
	if (clock.HM{H: lt.Hour(), M: lt.Minute()}).Minutes() >= w.window.Cutoff.Minutes() {
		return "tomorrow night's"
	}
	return "tonight's"
}

// Approve runs the plan protocol on the open PR: false when the PR has no
// plan digest at all (the core then offers a plain review). Otherwise it
// finds the live plan (or asks which), explains the maintenance window
// and the ceremony window, builds the exact command the validator wants,
// shows it, posts it, then submits the review the merge needs.
func (w *Workflow) Approve(app workflow.App, pr, listed *model.PR) bool {
	cfg := w.cfg
	allPlans := ExtractPlanCandidates(pr, cfg)
	if len(allPlans) == 0 {
		return false
	}
	me := app.Me()
	where := model.Ref(pr.Repo, pr.Number, app.MultiRepo())
	cmd := cfg.ApproveCommand
	// Your review may already stand (a plan queued for a window you now
	// apply NOW): the command is still offered, the review is not repeated.
	reviewed := workflow.IApproved(app, pr)
	// Only live plans are approvable; superseded digests and notices are
	// never offered. An applied plan is no candidate either: what an
	// applied, still-open PR wants is the review that lets it merge.
	var cands []Candidate
	for _, c := range allPlans {
		if c.State == StateLive && !c.Applied {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		if reviewed {
			app.Notice(fmt.Sprintf("%s is already approved by you for this head — a second review changes nothing", where))
			return true
		}
		var done, all []string
		for _, c := range allPlans {
			if c.State == StateLive && c.Applied {
				done = append(done, c.Stack)
			}
			all = append(all, c.Stack+": "+c.State)
		}
		var note string
		if len(done) > 0 {
			note = fmt.Sprintf("%s is already applied (%s): a review approval is all it needs to merge.", where, strings.Join(done, ", "))
		} else {
			note = fmt.Sprintf("No live plan to %s on %s (%s).", cmd, where, strings.Join(all, ", "))
		}
		if !app.Confirm(note + fmt.Sprintf(" Submit a GitHub review approval as %s?", me)) {
			app.Notice("approve cancelled")
			return true
		}
		if err := app.GH().Approve(app.Context(), pr.Repo, pr.Number, ""); err != nil {
			app.Fail("review approval failed: " + err.Error())
			return true
		}
		app.MarkApproved(pr)
		app.BumpDetail()
		app.OK(fmt.Sprintf("review-approved %s as %s (no %s posted — no live plan)", where, me, cmd))
		app.Celebrate(false)
		app.Refresh(true)
		return true
	}

	var cand Candidate
	if len(cands) == 1 {
		cand = cands[0]
	} else {
		refused := ApproveRefusals(pr, cfg)
		labels := make([]string, 0, len(cands))
		for _, c := range cands {
			d := "?"
			if c.Destroys != nil {
				d = strconv.Itoa(*c.Destroys)
			}
			labels = append(labels, fmt.Sprintf("%-32s %s...  destroys: %s  [%s]", c.Stack, c.PlanID[:12], d,
				PlanStateLabel(c, refused[c.PlanID] != "", cfg)))
		}
		i := app.Pick(fmt.Sprintf("post %s for which plan?", cmd), labels)
		if i < 0 || i >= len(cands) {
			app.Notice("approve cancelled")
			return true
		}
		cand = cands[i]
	}

	note := ""
	switch {
	case cand.Applied:
		note = "\nNote: this plan is already APPLIED."
	case cand.Outcome != "":
		note = fmt.Sprintf("\nNote: the last attempt reads: %s. Re-posting the same %s resumes it.", cand.Outcome, cmd)
	case cand.Approved:
		note = fmt.Sprintf("\nNote: an %s is already posted; this re-posts it.", cmd)
	}
	whereAs := fmt.Sprintf("on %s as %s", where, me)
	now := app.Now()

	// The maintenance window: a plain approve of a DISRUPTIVE plan queues
	// it for the window's start instead of applying. Say so before
	// posting, and offer the deliberate emergency path: apply now with
	// --outside-window="<reason>" (the reason is posted on the PR with
	// the command). Advisory "shadow mode" verdicts
	// queue nothing, so they get neither. Ceremonies never queue: outside
	// the ceremony window a plain approve is refused.
	outside := ""
	verdict, hasVerdict := PlanVerdicts(pr)[cand.PlanID]
	disruptive := hasVerdict && IsDisruptive(verdict)
	reasonPrompt := func(extra string) (string, bool) {
		return app.Prompt(fmt.Sprintf("Why can't this wait for the window? The reason goes on the PR%s "+
			"(at least %d characters, no double quotes); Esc cancels:", extra, cfg.OutsideWindowMinReason),
			workflow.PromptOpts{MinLen: cfg.OutsideWindowMinReason, Forbid: `"`})
	}
	switch {
	case cand.Ceremony && disruptive && cfg.CeremonyWindow == nil:
		// no ceremony window configured: nothing restricts it
	case cand.Ceremony && disruptive && w.inCeremonyWindow(now):
		cw := cfg.CeremonyWindow
		note += fmt.Sprintf("\nWindow: disruptive ceremony — inside %s, so this %s runs it now.", cw.Local().Span(cw.From, cw.To), cmd)
	case cand.Ceremony && disruptive:
		cw := cfg.CeremonyWindow
		span := cw.Local().Span(cw.From, cw.To)
		i := app.Pick(fmt.Sprintf("ceremony %s is disruptive — a plain %s outside %s is REFUSED (ceremonies are never queued)",
			cand.Stack, cmd, span),
			[]string{fmt.Sprintf("cancel — re-run 'a' between %s and %s %s", cw.From, cw.To, cw.Local().LabelAt(now)),
				fmt.Sprintf("run NOW, outside the window (%s) — real emergencies only", cfg.OutsideWindowFlag)})
		if i != 1 {
			app.Notice("approve cancelled")
			return true
		}
		reason, ok := reasonPrompt(" with the command")
		if !ok || len([]rune(reason)) < cfg.OutsideWindowMinReason || strings.Contains(reason, `"`) {
			app.Notice("approve cancelled")
			return true
		}
		outside = fmt.Sprintf(` %s="%s"`, cfg.OutsideWindowFlag, reason)
	case !cand.Ceremony && disruptive && !verdict.Shadow && w.window != nil:
		local := w.window.Local()
		start := local.T(w.window.Start)
		night := w.queueNightPhrase(now)
		attended := ""
		if strings.Contains(strings.ToLower(verdict.Verdict), "not revertible") {
			attended = fmt.Sprintf(" (not revertible: it runs only on a night someone attends: %s and %s on the manifest)",
				w.window.AttendCommand, w.window.ReleaseCommand)
		}
		i := app.Pick(fmt.Sprintf("plan %s is %s — a plain %s QUEUES it for the %s maintenance window%s",
			cand.Stack, verdict.Verdict, cmd, start, attended),
			[]string{fmt.Sprintf("queue it for %s window (plain %s) — the normal path", night, cmd),
				fmt.Sprintf("apply NOW, outside the window (%s) — real emergencies only", cfg.OutsideWindowFlag)})
		if i < 0 {
			app.Notice("approve cancelled")
			return true
		}
		if i == 1 {
			// the prompt counts down to the minimum and refuses Enter (and
			// the double quote) until the reason is valid; the check below
			// is the backstop
			reason, ok := reasonPrompt(" with the command")
			if !ok {
				app.Notice("approve cancelled")
				return true
			}
			if len([]rune(reason)) < cfg.OutsideWindowMinReason || strings.Contains(reason, `"`) {
				app.Fail(fmt.Sprintf("approve cancelled — the reason needs at least %d characters and no double quotes", cfg.OutsideWindowMinReason))
				return true
			}
			outside = fmt.Sprintf(` %s="%s"`, cfg.OutsideWindowFlag, reason)
		} else {
			note += fmt.Sprintf("\nWindow: %s — this %s queues the apply for %s %s window.", verdict.Verdict, cmd, night, start)
		}
	}

	// Build the command. The validator requires --confirm-destroys=N
	// exactly matching the destroy count when destroys >= 1, and its
	// ABSENCE when destroys == 0. Every question shows the exact comment.
	body := cmd + " " + cand.PlanID + outside // flags: any order
	destroys := 0
	var ok bool
	switch {
	case cand.Destroys == nil:
		typed, got := app.Prompt(fmt.Sprintf("plan %s: its destroy count can't be parsed from the digest.%s\n"+
			"Type the plan's destroy count (0 if it destroys nothing), or Esc to cancel:", cand.Stack, note), workflow.PromptOpts{})
		if !got {
			app.Notice("approve cancelled")
			return true
		}
		if !digitsRE.MatchString(typed) {
			app.Notice("approve cancelled (not a number)")
			return true
		}
		destroys, _ = strconv.Atoi(typed)
		if destroys > 0 { // 0 means NO flag: the validator refuses =0
			body += fmt.Sprintf(" %s=%d", cfg.ConfirmDestroysFlag, destroys)
		}
		ok = app.Confirm(fmt.Sprintf("%s%s\nPost that %s?", body, note, whereAs))
	case *cand.Destroys > 0:
		destroys = *cand.Destroys
		body += fmt.Sprintf(" %s=%d", cfg.ConfirmDestroysFlag, destroys)
		typed, _ := app.Prompt(fmt.Sprintf("plan %s DESTROYS %d resource(s). Posting %s:\n%s%s\nType %d to confirm, or Esc to cancel:",
			cand.Stack, destroys, whereAs, body, note, destroys), workflow.PromptOpts{Note: glyphs.G("hazard") + " destroys " + strconv.Itoa(destroys)})
		if typed != strconv.Itoa(destroys) {
			app.Notice("approve cancelled (destroy count not confirmed)")
			return true
		}
		ok = true
	default:
		ok = app.Confirm(fmt.Sprintf("%s%s\nPost that %s?", body, note, whereAs))
	}
	if !ok {
		app.Notice("approve cancelled")
		return true
	}
	if !CommandRE(cfg).MatchString(body) {
		app.Fail(fmt.Sprintf("%s NOT posted: %q does not match the validator's grammar", cmd, body))
		return true
	}
	if err := app.GH().Comment(app.Context(), pr.Repo, pr.Number, body); err != nil {
		app.Fail(fmt.Sprintf("%s NOT posted: %s", cmd, err))
		return true
	}
	// the approve comment is the operative approval: mark the row now,
	// even if the follow-up review approval fails
	app.MarkApproved(pr)
	app.BumpDetail()
	// the eventual merge needs a code-owner review too; do it in the
	// same keystroke, but don't fail the approve if it errors
	reviewNote := " + review approved"
	if reviewed {
		reviewNote = " (your review approval stands)"
	} else if err := app.GH().Approve(app.Context(), pr.Repo, pr.Number, ""); err != nil {
		reviewNote = fmt.Sprintf(" (review approval failed: %s)", err)
	}
	flagNote := ""
	if destroys > 0 {
		flagNote = fmt.Sprintf(" %s=%d", cfg.ConfirmDestroysFlag, destroys)
	}
	if outside != "" {
		flagNote += " " + cfg.OutsideWindowFlag
	}
	app.OK(fmt.Sprintf("posted %s %s...%s on %s%s", cmd, cand.PlanID[:12], flagNote, where, reviewNote))
	app.Celebrate(true)
	app.Refresh(true)
	return true
}

// ---- help, legend, markdown ----

// LegendRows adds the two chips and the plan-symbol legend.
func (w *Workflow) LegendRows() [][]text.Seg {
	label := func(t string) text.Seg { return text.S(text.Pad(t, 10), "section") }
	sp := text.S("  ", "text")
	chips := []text.Seg{label("")}
	chips = append(chips, scenes.Chip(AppliedLabel, "chip.await")...)
	chips = append(chips, text.S("  merges once reviewed", "muted"), sp)
	chips = append(chips, scenes.Chip(RequeueLabel, "chip.await")...)
	chips = append(chips, text.S("  needs a fresh "+w.cfg.ApproveCommand, "muted"))
	plans := []text.Seg{label("plans"), text.S("+ create", "tf.create"), sp,
		text.S("- destroy", "tf.destroy"), sp, text.S("-/+ replace", "tf.replace"), sp,
		text.S("~ update", "tf.update"), sp, text.S("<= read", "tf.read"), sp,
		text.S(" "+w.cfg.ApproveCommand+" <id> ", "approve.cmd")}
	return [][]text.Seg{chips, plans}
}

// InlinePatterns are the plan-summary and approve-mention highlighters.
func (w *Workflow) InlinePatterns() []workflow.InlinePattern { return w.hooks.Inline }

// LineRenderer takes over the operative approve line, the validator's
// refusals and the window verdict lines.
func (w *Workflow) LineRenderer(line string, width int) []text.Line { return w.hooks.Line(line, width) }

// CodeLineSegs colors plan symbols inside code fences.
func (w *Workflow) CodeLineSegs(line, lang string) []text.Seg { return w.hooks.CodeLine(line, lang) }

// baseOr names the base branch, or "the base branch" when unknown.
func baseOr(b string) string {
	if b == "" {
		return "the base branch"
	}
	return b
}
