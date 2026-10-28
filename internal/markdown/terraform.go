package markdown

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/text"
)

// Terraform plan-digest highlighting: generic terraform coloring the
// terraform workflow registers through TerraformHooks.
var (
	planSummaryRE    = regexp.MustCompile(`Plan: (?:\d+ to import, )?\d+ to add, \d+ to change, \d+ to destroy\.`)
	approveMentionRE = regexp.MustCompile(`/approve\b(?:[ \t]+[0-9a-f]{64})?`)
	planNumsRE       = regexp.MustCompile(`(\d+)( to (?:import|add|change|destroy))`)
	// planDestroysRE is the dispatcher's destroy-count parser's shape.
	planDestroysRE = regexp.MustCompile(`Plan: (?:\d+ to import, )?\d+ to add, \d+ to change, (\d+) to destroy\.`)
	tfSymbolRE     = regexp.MustCompile(`^(\s*)(-/\+|\+/-|<=|[+~-])(\s)`)
	tfNoteRE       = regexp.MustCompile(`^\s*# .* (will be created|will be destroyed|` +
		`must be replaced|will be updated in-place|will be read during apply|has moved to)`)
	windowVerdictLineRE = regexp.MustCompile(`^(Window \([^)]*\):\s*)\*\*([^*]+)\*\*(.*)$`)
	// approveCmdRE is ApproveCmdRE: only a comment the dispatcher would
	// accept counts as the operative approval.
	approveCmdRE = ApproveCmdRE
	// approveRefusedRE is apply-dispatch's reply when it turns an /approve down.
	approveRefusedRE = regexp.MustCompile(`^\*\*/approve (refused|not routed|not dispatched):\*\*`)
)

// ApproveCmdRE is the validator's shape of an /approve command line: the
// 64-hex plan id and its optional --confirm-destroys / --outside-window
// flags. The widgets package highlights the same shape.
var ApproveCmdRE = regexp.MustCompile(`^/approve [ \t\r\n\f\v]*([0-9a-f]{64})` +
	`((?:[ \t]+--(?:confirm-destroys=[0-9]+|outside-window="[^"\r\n]*"))*)` +
	`[ \t\r\n\f\v]*$`)

// VerdictStyle is the style for a window verdict: "verdict.notrev" for a
// not-revertible one, "verdict.disruptive" for a disruptive one, else
// "verdict.ok".
func VerdictStyle(verdict string) string {
	v := strings.ToLower(verdict)
	if strings.Contains(v, "not revertible") {
		return "verdict.notrev"
	}
	if strings.Contains(v, "disruptive") && !strings.HasPrefix(v, "non") {
		return "verdict.disruptive"
	}
	return "verdict.ok"
}

// PlanSummarySegs colors "Plan: 1 to add, 0 to change, 2 to destroy."
// with each count in its own style: plan.add, plan.change, plan.destroy,
// or plan.zero for a zero count; the words are muted.
func PlanSummarySegs(s string) []text.Seg {
	var out []text.Seg
	add := func(t, style string) {
		if t != "" {
			out = append(out, text.S(t, style))
		}
	}
	pos := 0
	for _, l := range planNumsRE.FindAllStringSubmatchIndex(s, -1) {
		add(s[pos:l[0]], "muted")
		num, what := s[l[2]:l[3]], s[l[4]:l[5]]
		n, _ := strconv.Atoi(num)
		style := "plan.change"
		switch {
		case n == 0:
			style = "plan.zero"
		case strings.Contains(what, "destroy"):
			style = "plan.destroy"
		case strings.Contains(what, "add") || strings.Contains(what, "import"):
			style = "plan.add"
		}
		add(num, style)
		add(what, "muted")
		pos = l[1]
	}
	add(s[pos:], "muted")
	return out
}

// terraformCodeLine colors a plan line inside a fence: symbols (+ create,
// - destroy, -/+ replace, ~ update, <= read), "# ... will be created"
// notes, and the plan summary. nil for anything else, and for diff fences.
func terraformCodeLine(line, lang string) []text.Seg {
	if lang == "diff" || lang == "patch" {
		return nil
	}
	if l := planDestroysRE.FindStringIndex(line); l != nil {
		start := strings.Index(line, "Plan:")
		segs := []text.Seg{text.S(line[:start], "md.code")}
		segs = append(segs, PlanSummarySegs(line[start:l[1]])...)
		return append(segs, text.S(line[l[1]:], "md.code"))
	}
	if m := tfSymbolRE.FindStringSubmatch(line); m != nil {
		style := "tf.replace"
		switch m[2] {
		case "+":
			style = "tf.create"
		case "-":
			style = "tf.destroy"
		case "~":
			style = "tf.update"
		case "<=":
			style = "tf.read"
		}
		return []text.Seg{text.S(line, style)}
	}
	if m := tfNoteRE.FindStringSubmatch(line); m != nil {
		verb := m[1]
		style := "tf.note"
		switch {
		case strings.Contains(verb, "created"):
			style = "tf.create"
		case strings.Contains(verb, "destroyed"):
			style = "tf.destroy"
		case strings.Contains(verb, "replaced"):
			style = "tf.replace"
		case strings.Contains(verb, "updated"):
			style = "tf.update"
		}
		return []text.Seg{text.S(line, style)}
	}
	return nil
}

// TerraformHooks is the terraform workflow's renderer extension: plan
// summaries and /approve mentions inline, plan symbols and notes in code
// fences, and whole lines for the operative "/approve <id>" command
// (approve.cmd), the dispatcher's refusals (approve.refused) and
// "Window (...): **verdict**" lines (verdict.*).
func TerraformHooks() *Hooks {
	h := &Hooks{
		Inline: []InlinePattern{
			{Re: planSummaryRE, Segs: func(m []string, _ string) []text.Seg {
				return PlanSummarySegs(m[0])
			}},
			{Re: approveMentionRE, Segs: func(m []string, _ string) []text.Seg {
				return []text.Seg{text.S(m[0], "approve.mention")}
			}},
		},
		CodeLine: terraformCodeLine,
	}
	h.Line = func(line string, width int) []text.Line {
		if approveCmdRE.MatchString(line) {
			// the operative approval: the loudest thing on the screen
			s := " " + line + " "
			if Glyphs.Shield != "" {
				s = " " + Glyphs.Shield + " " + line + " "
			}
			return Wrap([]text.Seg{text.S(s, "approve.cmd")}, width, nil, nil, true)
		}
		if approveRefusedRE.MatchString(line) {
			segs := append([]text.Seg{text.S(ico(Glyphs.Warn), "approve.refused")},
				Inline(line, "approve.refused", h)...)
			return Wrap(segs, width, nil, nil, false)
		}
		if m := windowVerdictLineRE.FindStringSubmatch(line); m != nil {
			style := VerdictStyle(m[2])
			segs := []text.Seg{
				text.S(ico(Glyphs.Window), style),
				text.S(m[1], "muted"),
				text.S(strings.TrimSpace(m[2]), style),
			}
			segs = append(segs, Inline(m[3], "muted", h)...)
			return Wrap(segs, width, nil, nil, false)
		}
		return nil
	}
	return h
}
