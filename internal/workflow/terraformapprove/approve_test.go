package terraformapprove

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

const reason = "customer outage, cert expires 14:00 ET"

// script is the scripted answers of one approve run.
type script struct {
	confirms []bool
	prompts  []string
	picks    []int
}

// approve runs the workflow's Approve on pr as alice with the script and
// returns the app (its log, questions and fake client) and the hook's
// answer.
func approve(t *testing.T, cfg *config.Config, pr *model.PR, s script) (*wftest.App, bool) {
	t.Helper()
	app := wftest.New(cfg, "alice")
	app.DetailItm = pr
	app.Confirms, app.Prompts, app.Picks = s.confirms, s.prompts, s.picks
	w := New(cfg.TerraformApprove, cfg.MaintenanceWindow)
	return app, w.Approve(app, pr, nil)
}

func posted(app *wftest.App) []string {
	var out []string
	for _, c := range app.Fake.CommentsPosted {
		out = append(out, fmt.Sprintf("%d:%s", c.Number, c.Body))
	}
	return out
}

func one(number int, body string) []string { return []string{fmt.Sprintf("%d:%s", number, body)} }

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasLog(app *wftest.App, prefix, sub string) bool {
	for _, l := range app.Log {
		if strings.HasPrefix(l, prefix) && strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func simple(stack, pid string, destroys int) *model.PR {
	return makePR([]model.Comment{comment(digest(stack, pid, destroys, 100, ""))}, "sha1")
}

func noSummary(stack, pid string) *model.PR {
	body := fmt.Sprintf("### plan `%s`\n\nPlan-id: `%s`\nhttps://github.com/acme/infra/actions/runs/100\n", stack, pid)
	return makePR([]model.Comment{comment(body)}, "sha1")
}

func TestApproveBodies(t *testing.T) {
	cfg := testCfg()
	app, handled := approve(t, cfg, simple("prod/07-warehouse", idA, 0), script{confirms: []bool{true}})
	check(t, "0 destroys: handled", handled)
	check(t, "0 destroys: posts the bare /approve", eq(posted(app), one(3622, "/approve "+idA)))
	check(t, "0 destroys: review approval follows", len(app.Fake.Approvals) == 1 && app.Fake.Approvals[0] == 3622)
	check(t, "0 destroys: the confirm shows the exact command", len(app.Questions) == 1 && strings.Contains(app.Questions[0], "/approve "+idA))
	check(t, "0 destroys: success message + refresh re-queued", hasLog(app, "ok:", "posted /approve") && len(app.Refreshes) == 1 && app.Refreshes[0])
	check(t, "0 destroys: marked approved at this head", app.Approved[model.Ident{Repo: "infra", Number: 3622}] == "sha1")
	check(t, "0 destroys: celebrates big", len(app.Celebrated) == 1 && app.Celebrated[0])

	app, _ = approve(t, cfg, simple("prod/07-warehouse", idA, 0), script{confirms: []bool{false}})
	check(t, "0 destroys, declined: nothing posted", len(app.Fake.CommentsPosted) == 0 && len(app.Fake.Approvals) == 0 && hasLog(app, "notice:", "approve cancelled"))

	pr2 := simple("prod/07-warehouse", idA, 2)
	app, _ = approve(t, cfg, pr2, script{prompts: []string{"2"}})
	check(t, "2 destroys, typed 2: flag added", eq(posted(app), one(3622, "/approve "+idA+" --confirm-destroys=2")))
	check(t, "2 destroys: the prompt shows the exact command", strings.Contains(app.Prompted[0], "/approve "+idA+" --confirm-destroys=2"))
	for _, typed := range []string{"3", "", "02"} {
		app, _ = approve(t, cfg, pr2, script{prompts: []string{typed}})
		check(t, fmt.Sprintf("2 destroys, typed %q: nothing posted", typed), len(app.Fake.CommentsPosted) == 0 && len(app.Fake.Approvals) == 0)
	}
	app, _ = approve(t, cfg, pr2, script{})
	check(t, "2 destroys, Esc: nothing posted", len(app.Fake.CommentsPosted) == 0 && hasLog(app, "notice:", "not confirmed"))

	// an unparseable destroy count: typed 0 must NOT add --confirm-destroys=0
	prU := noSummary("prod/07-warehouse", idA)
	app, _ = approve(t, cfg, prU, script{prompts: []string{"0"}, confirms: []bool{true}})
	check(t, "unparseable, typed 0: no flag", eq(posted(app), one(3622, "/approve "+idA)))
	app, _ = approve(t, cfg, prU, script{prompts: []string{"3"}, confirms: []bool{true}})
	check(t, "unparseable, typed 3: flag 3", eq(posted(app), one(3622, "/approve "+idA+" --confirm-destroys=3")))
	check(t, "unparseable: the confirm shows the flagged command", strings.Contains(app.Questions[0], "--confirm-destroys=3"))
	app, _ = approve(t, cfg, prU, script{prompts: []string{"3"}, confirms: []bool{false}})
	check(t, "unparseable, typed 3 then declined: nothing posted", len(app.Fake.CommentsPosted) == 0)
	for _, typed := range []string{"²", "", "x1"} {
		app, _ = approve(t, cfg, prU, script{prompts: []string{typed}, confirms: []bool{true}})
		check(t, fmt.Sprintf("unparseable, typed %q: cancelled", typed), len(app.Fake.CommentsPosted) == 0)
	}
	app, _ = approve(t, cfg, prU, script{confirms: []bool{true}})
	check(t, "unparseable, Esc: cancelled", len(app.Fake.CommentsPosted) == 0)

	// ten live plans in one run: the menu reaches the tenth
	var ten []model.Comment
	for i := 1; i <= 10; i++ {
		ten = append(ten, comment(digest(fmt.Sprintf("prod/s%02d", i), strings.Repeat(fmt.Sprintf("%x", i), 64), 0, 200, "")))
	}
	app, _ = approve(t, cfg, makePR(ten, "sha1"), script{picks: []int{9}, confirms: []bool{true}})
	check(t, "menu lists all ten plans", len(app.Menus) == 1 && len(app.Menus[0]) == 11)
	check(t, "the tenth plan is postable", eq(posted(app), one(3622, "/approve "+idA)))
	app, _ = approve(t, cfg, makePR(ten, "sha1"), script{})
	check(t, "menu cancelled: nothing posted", len(app.Fake.CommentsPosted) == 0)
}

func TestApproveReviewOnly(t *testing.T) {
	cfg := testCfg()
	nochange := "### plan `prod/07-warehouse` — no changes\n"
	app, handled := approve(t, cfg, makePR([]model.Comment{comment(nochange)}, "sha1"), script{confirms: []bool{true}})
	check(t, "no live plan: handled, review only, no comment", handled && len(app.Fake.CommentsPosted) == 0 && len(app.Fake.Approvals) == 1)
	check(t, "no live plan: the confirm lists the stacks' states", strings.Contains(app.Questions[0], "prod/07-warehouse: no changes"))
	check(t, "no live plan: says no /approve posted", hasLog(app, "ok:", "no /approve posted"))
	app, _ = approve(t, cfg, makePR([]model.Comment{comment(nochange)}, "sha1"), script{confirms: []bool{false}})
	check(t, "no live plan, declined: nothing", len(app.Fake.Approvals) == 0)

	app, handled = approve(t, cfg, makePR(nil, "sha1"), script{confirms: []bool{true}})
	check(t, "no plan digest at all: not handled, nothing asked", !handled && len(app.Questions) == 0 && len(app.Fake.Approvals) == 0)

	applied := makePR([]model.Comment{comment(digest("s/x", idA, 0, 100, "")),
		commentAt("Apply SUCCESS\nPlan-id: `"+idA+"`", "2026-10-22T18:30:00Z")}, "sha1")
	app, _ = approve(t, cfg, applied, script{confirms: []bool{false}})
	check(t, "applied plan: the confirm says so", strings.Contains(app.Questions[0], "already applied") && strings.Contains(app.Questions[0], "review"))
	app, _ = approve(t, cfg, applied, script{confirms: []bool{true}})
	check(t, "applied plan: review only, no /approve re-posted", len(app.Fake.CommentsPosted) == 0 && len(app.Fake.Approvals) == 1)
	verified := makePR([]model.Comment{comment(digest("s/x", idA, 0, 100, "")),
		commentAt("## Window apply VERIFIED — `s/x`\nPlan-id: `"+idA+"`", "2026-10-22T18:30:00Z")}, "sha1")
	app, _ = approve(t, cfg, verified, script{confirms: []bool{true}})
	check(t, "window-verified plan: review only", len(app.Fake.CommentsPosted) == 0 && len(app.Fake.Approvals) == 1)
	reposted := makePR([]model.Comment{comment(digest("s/x", idA, 1, 100, "")),
		commentBy("carol", "/approve "+idA+" --confirm-destroys=1", "2026-10-22T18:30:00Z")}, "sha1")
	app, _ = approve(t, cfg, reposted, script{})
	check(t, "destroys > 0: the re-post note is shown too", strings.Contains(app.Prompted[0], "re-posts"))

	// a failed post: error shown, no approval mark, no review
	app = wftest.New(cfg, "alice")
	pr := simple("s/x", idA, 0)
	app.DetailItm, app.Confirms = pr, []bool{true}
	app.Fake.Fail["comment"] = errors.New("POST -> 500: boom")
	New(cfg.TerraformApprove, cfg.MaintenanceWindow).Approve(app, pr, nil)
	check(t, "failed /approve: error says NOT posted, nothing marked", hasLog(app, "fail:", "NOT posted") && len(app.Approved) == 0 && len(app.Fake.Approvals) == 0)
	// a failed review after a posted approve: still marked, noted
	app = wftest.New(cfg, "alice")
	app.DetailItm, app.Confirms = pr, []bool{true}
	app.Fake.Fail["approve"] = errors.New("POST -> 500: boom")
	New(cfg.TerraformApprove, cfg.MaintenanceWindow).Approve(app, pr, nil)
	check(t, "failed review after the post: marked, the note says so", len(app.Approved) == 1 && hasLog(app, "ok:", "review approval failed"))
}

func windowed(verdict string, destroys int, shadow bool) *model.PR {
	wording := "routing live"
	if shadow {
		wording = "advisory, shadow mode: nothing routes on this yet"
	}
	return makePR([]model.Comment{comment(digest("prod/07-warehouse", idA, destroys, 100, "") +
		fmt.Sprintf("\nWindow (%s): **%s** — x", wording, verdict))}, "sha1")
}

func TestApproveWindow(t *testing.T) {
	cfg := testCfg()
	app, _ := approve(t, cfg, windowed("disruptive · revertible", 0, false), script{picks: []int{0}, confirms: []bool{true}})
	check(t, "disruptive: asks queue-or-now first", len(app.Menus) == 1 && strings.Contains(app.Menus[0][0], "QUEUES"))
	check(t, "disruptive: the menu names the configured window", strings.Contains(app.Menus[0][0], "23:00 ET maintenance window"))
	check(t, "disruptive, queue: the confirm says it queues, and for which night",
		strings.Contains(app.Questions[0], "queues the apply for") && strings.Contains(app.Questions[0], "23:00 ET window") &&
			(strings.Contains(app.Questions[0], "tonight's") || strings.Contains(app.Questions[0], "tomorrow night's")))
	check(t, "disruptive, queue: plain /approve", eq(posted(app), one(3622, "/approve "+idA)))

	app, _ = approve(t, cfg, windowed("disruptive · NOT revertible", 0, false), script{picks: []int{1}, prompts: []string{reason}, confirms: []bool{true}})
	check(t, "disruptive, now: --outside-window with the reason", eq(posted(app), one(3622, fmt.Sprintf(`/approve %s --outside-window="%s"`, idA, reason))))
	check(t, "disruptive, now: the confirm shows the flag", strings.Contains(app.Questions[len(app.Questions)-1], fmt.Sprintf(`--outside-window="%s"`, reason)))
	check(t, "disruptive, now: the reason prompt counts to the minimum and refuses quotes",
		app.PromptOps[0].MinLen == cfg.TerraformApprove.OutsideWindowMinReason && app.PromptOps[0].Forbid == `"`)
	check(t, "not revertible: the menu says an attended night is needed", strings.Contains(app.Menus[0][0], "/attend and /release"))

	app, _ = approve(t, cfg, windowed("disruptive · revertible", 2, false), script{picks: []int{1}, prompts: []string{reason, "2"}})
	check(t, "outside-window + destroys: both flags", eq(posted(app), one(3622, fmt.Sprintf(`/approve %s --outside-window="%s" --confirm-destroys=2`, idA, reason))))
	check(t, "the summary names both flags", hasLog(app, "ok:", "--confirm-destroys=2 --outside-window"))
	for _, bad := range []string{"too short reason", `has a "quote" in it, long enough`} {
		app, _ = approve(t, cfg, windowed("disruptive · revertible", 0, false), script{picks: []int{1}, prompts: []string{bad}, confirms: []bool{true}})
		check(t, fmt.Sprintf("outside-window reason %q refused, nothing posted", bad), len(app.Fake.CommentsPosted) == 0)
		check(t, "...and the backstop's refusal is an error", hasLog(app, "fail:", "20 characters"))
	}
	app, _ = approve(t, cfg, windowed("disruptive · revertible", 0, false), script{picks: []int{1}, confirms: []bool{true}})
	check(t, "reason prompt cancelled: nothing posted", len(app.Fake.CommentsPosted) == 0 && hasLog(app, "notice:", "approve cancelled"))
	app, _ = approve(t, cfg, windowed("disruptive · revertible", 0, false), script{confirms: []bool{true}})
	check(t, "window question cancelled: nothing posted", len(app.Fake.CommentsPosted) == 0)
	app, _ = approve(t, cfg, windowed("disruptive · revertible", 0, true), script{confirms: []bool{true}})
	check(t, "shadow-mode verdict: no window question", eq(posted(app), one(3622, "/approve "+idA)) && len(app.Menus) == 0)
	app, _ = approve(t, cfg, windowed("non-disruptive", 0, false), script{confirms: []bool{true}})
	check(t, "non-disruptive: no window question", len(app.Menus) == 0)

	// without the window workflow a disruptive plan is a plain approve
	noWin := testCfg()
	noWin.MaintenanceWindow = nil
	app, _ = approve(t, noWin, windowed("disruptive · revertible", 0, false), script{confirms: []bool{true}})
	check(t, "no window workflow: no window question, plain /approve", len(app.Menus) == 0 && eq(posted(app), one(3622, "/approve "+idA)))

	// the window's strings follow the config
	custom := testCfg()
	custom.MaintenanceWindow.Start = clock.HM{H: 22, M: 30}
	custom.MaintenanceWindow.TZLabel = ""
	custom.TerraformApprove.OutsideWindowFlag = "--emergency"
	custom.TerraformApprove.OutsideWindowMinReason = 5
	app, _ = approve(t, custom, windowed("disruptive · revertible", 0, false), script{picks: []int{1}, prompts: []string{"hurry"}, confirms: []bool{true}})
	check(t, "configured flag and minimum", eq(posted(app), one(3622, `/approve `+idA+` --emergency="hurry"`)) && app.PromptOps[0].MinLen == 5)
	check(t, "configured start and a derived zone label", strings.Contains(app.Menus[0][0], "22:30 E"))
}

func TestApprovePlanSetsAndCeremonies(t *testing.T) {
	cfg := testCfg()
	nd := []model.Comment{
		comment(digest("nonprod/15-window-canary", idA, 0, 200, "non-disruptive")),
		comment(digest("prod/07-warehouse", idB, 0, 200, "non-disruptive")),
		comment(planSet(headSHA, member{"nonprod/15-window-canary", idA, 0}, member{"prod/07-warehouse", idB, 0}))}
	app, _ := approve(t, cfg, makePR(nd, headSHA), script{confirms: []bool{true}})
	check(t, "plan-set approve posts the SET id, no picker", eq(posted(app), one(3622, "/approve "+setID)) && len(app.Menus) == 0)

	setReason := "state-only rewrites, identical re-sends"
	app, _ = approve(t, cfg, makePR(twoStacks(), headSHA), script{picks: []int{1}, prompts: []string{setReason, "1"}, confirms: []bool{true}})
	check(t, "disruptive plan-set: --outside-window + summed --confirm-destroys",
		eq(posted(app), one(3622, fmt.Sprintf(`/approve %s --outside-window="%s" --confirm-destroys=1`, setID, setReason))))

	saved := clock.Now
	defer func() { clock.Now = saved }()
	clock.Now = func() time.Time { return at("2026-10-22T19:00:00Z") } // 15:00 ET: outside
	cer := makePR([]model.Comment{comment(ceremony(headSHA, "disruptive"))}, headSHA)
	app, _ = approve(t, cfg, cer, script{picks: []int{0}})
	check(t, "disruptive ceremony outside the window: cancel posts nothing", len(app.Fake.CommentsPosted) == 0)
	check(t, "the ceremony menu names the configured span", strings.Contains(app.Menus[0][0], "23:00-23:28 EDT"))
	app, _ = approve(t, cfg, cer, script{picks: []int{1}, prompts: []string{reason}, confirms: []bool{true}})
	check(t, "disruptive ceremony outside window: --outside-window, never --confirm-destroys",
		eq(posted(app), one(3622, fmt.Sprintf(`/approve %s --outside-window="%s"`, cid, reason))))
	app, _ = approve(t, cfg, cer, script{picks: []int{1}, prompts: []string{"short"}, confirms: []bool{true}})
	check(t, "disruptive ceremony: a short reason cancels", len(app.Fake.CommentsPosted) == 0)
	clock.Now = func() time.Time { return at("2026-10-23T03:10:00Z") } // 23:10 ET: inside
	app, _ = approve(t, cfg, cer, script{confirms: []bool{true}})
	check(t, "disruptive ceremony inside the window: plain /approve", eq(posted(app), one(3622, "/approve "+cid)) && len(app.Menus) == 0)
	check(t, "...and the confirm says it runs now", strings.Contains(app.Questions[0], "runs it now"))
	clock.Now = saved
	app, _ = approve(t, cfg, makePR([]model.Comment{comment(ceremony(headSHA, ""))}, headSHA), script{confirms: []bool{true}})
	check(t, "non-disruptive ceremony: plain /approve", eq(posted(app), one(3622, "/approve "+cid)))
	noCW := testCfg()
	noCW.TerraformApprove.CeremonyWindow = nil
	app, _ = approve(t, noCW, cer, script{confirms: []bool{true}})
	check(t, "no ceremony window configured: a disruptive ceremony is a plain /approve", eq(posted(app), one(3622, "/approve "+cid)) && len(app.Menus) == 0)
}

func TestApproveMultiRepoRef(t *testing.T) {
	cfg := testCfg()
	cfg.Reviews.Repos = []model.Repo{infra, {Owner: "acme", Name: "web-portal"}}
	app, _ := approve(t, cfg, simple("prod/07-warehouse", idA, 0), script{confirms: []bool{true}})
	check(t, "several repos: the messages name the repo", hasLog(app, "ok:", "on infra#3622"))
	check(t, "the comment lands on the PR's repo", app.Fake.CommentsPosted[0].Repo == infra)
}

func TestLegendAndHooks(t *testing.T) {
	w := New(testCfg().TerraformApprove, nil)
	rows := w.LegendRows()
	check(t, "two legend rows", len(rows) == 2)
	joined := ""
	for _, r := range rows {
		for _, s := range r {
			joined += s.Text
		}
	}
	check(t, "the legend names both chips and the command", strings.Contains(joined, AppliedLabel) && strings.Contains(joined, RequeueLabel) && strings.Contains(joined, "/approve <id>"))
	check(t, "markdown hooks are the terraform set", len(w.InlinePatterns()) == 2 && w.LineRenderer("/approve "+idA, 80) != nil && w.CodeLineSegs("+ resource", "") != nil)
	check(t, "chip styles and needs-you", w.ChipStyles()[AppliedLabel] == "chip.await" && len(w.NeedsYou()) == 2)
	check(t, "the bot login comes from the config", len(w.BotLogins()) == 1 && w.BotLogins()[0] == "infra-bot")
	var _ workflow.Workflow = w
}
