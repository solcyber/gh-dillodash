// Package terraformapprove is the terraform plan-approval workflow: a plan
// bot posts a digest per stack ("### plan `env/stack`" with a Plan-id, a
// destroy count and a maintenance-window verdict), a plan-set trailer for
// multi-stack PRs, and ceremony digests; a reviewer approves by posting
// "/approve <id>" with the flags the validator demands
// (--confirm-destroys=N when the plan destroys, --outside-window="reason"
// to skip the maintenance window). The apply workflow reports its
// outcome on the thread and the apply gate status says when everything
// is applied.
//
// The digest and outcome shapes below are the documented protocol and
// stay package constants; the command, its flags and the bot login come
// from the config.
package terraformapprove

import (
	"regexp"
	"sync"

	"github.com/solcyber/gh-dillodash/internal/config"
)

// The plan bot's digest shapes.
var (
	// PlanStackRE finds the stack a digest is for: "### plan `env/stack`".
	PlanStackRE = regexp.MustCompile("###\\s*plan\\s+`([^`]+)`")
	// PlanNoticeRE is a "### plan `stack` — no changes" / "— skipped by
	// CI" / "— not CI-planned" notice, which supersedes an earlier digest
	// for the stack (an em dash, an en dash or plain hyphens).
	PlanNoticeRE = regexp.MustCompile("###\\s*plan\\s+`([^`]+)`\\s*[—–-]+\\s*(no changes|skipped by CI|not CI-planned)")
	// PlanRunRE is the Actions run a digest links.
	PlanRunRE = regexp.MustCompile(`/actions/runs/(\d+)`)
	// PlanIDRE is the "Plan-id: `<64 hex>`" line; hand-run digests bold
	// the label, so asterisks around the colon are tolerated.
	PlanIDRE = regexp.MustCompile("Plan-id\\**:\\**\\s*`([0-9a-f]{64})`")
	// PlanLivePathRE names a stack through its live-tree path: stacks are
	// always exactly <env>/<stack>.
	PlanLivePathRE = regexp.MustCompile(`terraform/live/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)`)
	// PlanDestroysRE is the validator's destroy-count parser's shape; the
	// LAST summary line in a comment counts.
	PlanDestroysRE = regexp.MustCompile(`Plan: (?:\d+ to import, )?\d+ to add, \d+ to change, (\d+) to destroy\.`)
	// PlanPairRE pairs an "<env>/<stack>" token with a `<64hex>` on the
	// same line (a combined multi-stack hand-run digest). Only used
	// against stacks that already carry a "skipped by CI" notice.
	PlanPairRE = regexp.MustCompile("([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)[^\\n`]*`([0-9a-f]{64})`")
	// PlanSetMetaRE is the plan-set comment's machine-readable trailer:
	// <!-- plan-set: {"set_id", "head_sha", "members": [...], "skipped"} -->.
	PlanSetMetaRE = regexp.MustCompile(`(?s)<!--\s*plan-set:\s*(\{.*?\})\s*-->`)
	// HexIDRE is a plan, set or ceremony id.
	HexIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// The ceremony lane's shapes.
var (
	// CeremonyDigestRE is an approvable ceremony digest's exact head.
	CeremonyDigestRE = regexp.MustCompile("\\A### ceremony `(ceremonies/[^/`\\s]+\\.sh)`\\r?\\n\\r?\\n" +
		"Ceremony-id: `([0-9a-f]{64})`\\r?\\nHead: `([0-9a-f]{40})`\\r?\\n")
	// CeremonyFailedRE is a failed dry-run's digest (not approvable).
	CeremonyFailedRE = regexp.MustCompile("\\A### ceremony `(ceremonies/[^/`\\s]+\\.sh)`: dry-run FAILED")
	// CeremonyIDRE is the "Ceremony-id: `<64 hex>`" line.
	CeremonyIDRE = regexp.MustCompile("Ceremony-id: `([0-9a-f]{64})`")
	// CeremonyWindowRE is a ceremony digest's "Window: **disruptive**:" /
	// "Window: non-disruptive:" line.
	CeremonyWindowRE = regexp.MustCompile(`(?m)^Window: (?:\*\*)?([a-z-]+)(?:\*\*)?:`)
)

// Outcome is an apply report heading that is NOT a success, and the label
// it reads as. Re-posting the same /approve resumes a failed set.
type Outcome struct {
	Re    *regexp.Regexp
	Label string
}

// ApplyOutcomes are the apply workflow's non-success headings, in
// priority order; the first that matches a comment wins.
var ApplyOutcomes = []Outcome{
	{regexp.MustCompile(`(?m)^## Window apply FAILED verification .*AUTO-REVERTED`),
		"apply AUTO-REVERTED in the window — re-plan before approving again"},
	{regexp.MustCompile(`(?m)^## Window apply FAILED verification .*NOT reverted`),
		"window apply FAILED verification, NOT reverted — a human is paged"},
	{regexp.MustCompile(`(?m)^## Apply REFUSED — saved plan is stale`),
		"apply REFUSED: saved plan is stale — push to re-plan"},
	{regexp.MustCompile(`(?m)^## Apply FAILURE`),
		"apply FAILED — read the comment; re-post the same /approve to resume"},
	{regexp.MustCompile(`(?m)^## Apply (SKIPPED|CANCELLED)`),
		"apply did not run — read the comment; re-post the same /approve to resume"},
	{regexp.MustCompile(`(?m)^## Plan-set INCOMPLETE`),
		"plan-set INCOMPLETE — fix the cause, re-post the same /approve to resume"},
	{regexp.MustCompile(`(?m)^## Ceremony FAILED`),
		"ceremony FAILED — re-post the same /approve to re-run (idempotent)"},
	{regexp.MustCompile(`(?m)^## Ceremony NOT RUN`),
		"ceremony NOT RUN — a gate refused; see the comment"},
}

var (
	// ApplyRefusedRE is what turns an approval down after it was
	// dispatched or queued: the apply workflow's "**Apply refused — ...**"
	// / "**Apply refused (maintenance window):**", its ceremony
	// re-validation "**Ceremony refused: ...**", window mode's "**Window
	// item refused before apply (P0):**" and the cutoff freeze's
	// "**Dropped from the maintenance-window queue (night of ...):**".
	// They name no id, so they attribute to the newest /approve before them.
	ApplyRefusedRE = regexp.MustCompile(`(?m)^\*\*(Apply refused[ (—-]|Ceremony refused[ :]|Window item refused before apply|Dropped from the maintenance-window queue)`)
	// ApprovedIDRE is the "Approved: `<id>`" line an outcome report carries.
	ApprovedIDRE = regexp.MustCompile("Approved: `([0-9a-f]{64})`")
	// NotADigestRE marks the apply workflow's own reports, which carry
	// Plan-id lines but are never digests.
	NotADigestRE = regexp.MustCompile(`(?m)^(## (Apply |Window apply |Plan-set |Ceremony )|\*\*(Apply|Ceremony) refused|Auto-generated apply-log entry)`)
	// WindowVerdictRE is the "Window (...): **verdict**" line in a digest.
	WindowVerdictRE = regexp.MustCompile(`(?m)^Window \(([^)]*)\):\s*\*\*([^*\n]+)\*\*`)
)

// cmdRegexps are the config-shaped regexes, cached per command/flag set.
type cmdRegexps struct {
	cmd     *regexp.Regexp
	refused *regexp.Regexp
}

var (
	cmdMu    sync.Mutex
	cmdCache = map[[3]string]cmdRegexps{}
)

func cmdRes(cfg *config.TerraformApproveConfig) cmdRegexps {
	key := [3]string{cfg.ApproveCommand, cfg.ConfirmDestroysFlag, cfg.OutsideWindowFlag}
	cmdMu.Lock()
	defer cmdMu.Unlock()
	if r, ok := cmdCache[key]; ok {
		return r
	}
	r := cmdRegexps{
		// the validator's grammar, matched against the raw body: only a
		// comment it would accept counts as "/approve posted". It starts
		// with the command and a literal space, because the dispatcher
		// only fires on startsWith(body, "<command> ").
		cmd: regexp.MustCompile(`^` + regexp.QuoteMeta(cfg.ApproveCommand) + ` [ \t\r\n\f\v]*([0-9a-f]{64})` +
			`((?:[ \t]+(?:` + regexp.QuoteMeta(cfg.ConfirmDestroysFlag) + `=[0-9]+|` +
			regexp.QuoteMeta(cfg.OutsideWindowFlag) + `="[^"\r\n]*"))*)` +
			`[ \t\r\n\f\v]*$`),
		// the validator's replies when it turns a command down:
		// "**/approve refused:** ...", "not routed", "not dispatched"
		refused: regexp.MustCompile(`^\*\*` + regexp.QuoteMeta(cfg.ApproveCommand) + ` (refused|not routed|not dispatched):\*\*`),
	}
	cmdCache[key] = r
	return r
}

// CommandRE is the validator's grammar for the approve command built
// from the config: "^/approve <64 hex>( --confirm-destroys=N |
// --outside-window="...")*$". Group 1 is the id, group 2 the flags.
func CommandRE(cfg *config.TerraformApproveConfig) *regexp.Regexp { return cmdRes(cfg).cmd }

// RefusedRE matches the validator's "**/approve refused:**" (or "not
// routed" / "not dispatched") reply, built from the config's command.
func RefusedRE(cfg *config.TerraformApproveConfig) *regexp.Regexp { return cmdRes(cfg).refused }
