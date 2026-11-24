package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestUSSpelling: what dillodash says is in US English (see CONTRIBUTING's
// style notes). It reads every string literal in the non-test Go files,
// so status text, prompts, notices and help, and fails on a British
// spelling from the list below. Comments and docs follow the same rule
// by review; this test guards what a person reads on screen.
//
// Lowercase and capitalized forms only: an all-caps word is GitHub's or a
// bot's own value (CANCELLED in a check rollup or an apply digest).
var britishSpelling = regexp.MustCompile(`\b(?i:` +
	`cancell(?:ed|ing)|labell(?:ed|ing)|modell(?:ed|ing)|travell(?:ed|ing|er)|` +
	`colour(?:s|ed|ing|ful)?|behaviours?|favour(?:s|ed|ite)?|honour(?:s|ed|ing)?|neighbours?|` +
	`centres?|centred|metres?|licences?|defence|offence|catalogue|` +
	`(?:organ|author|recogn|priorit|custom|summar|categor|normal|initial|serial|sanit|minim|maxim|optim|util|real|special|standard|synchron|memo|apolog)is(?:e|ed|es|ing|ation|ations)|` +
	`analys(?:e|ed|ing)|enrol(?:s|ment)?|acknowledgements?|judgements?|` +
	`grey|greyed|artefacts?|whilst|amongst|learnt|spelt)\b`)

// usSpellingExempt are exact literal values that are not dillodash's own
// words: values GitHub, a bot or the config file defines, which the code
// must match as they are.
var usSpellingExempt = map[string]string{
	"cancelled":      "GitHub's workflow-run conclusion, and the release phase it maps to",
	"run cancelled":  "the prefix of the release bot's outcome line",
	"cancelled by @": "the window manifest's item status, as the conductor writes it",
	"labelled":       "the fake client's failure key for IssuesWithLabel",
	"licence":        "the accepted alias of `font license`",
}

func TestUSSpelling(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, f := range files(t, root) {
		if filepath.Ext(f) != ".go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root, f), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			if _, ok := usSpellingExempt[s]; ok {
				return true
			}
			for _, w := range britishSpelling.FindAllString(s, -1) {
				if strings.ToUpper(w) == w {
					continue
				}
				t.Errorf("%s: %q spells %q the British way; write US English (canceled, labeled, color, license)", fset.Position(lit.Pos()), s, w)
			}
			return true
		})
	}
}

// TestUSSpellingCatches keeps the pattern honest: it must flag the
// British forms and pass their US spellings and the usual lookalikes.
func TestUSSpellingCatches(t *testing.T) {
	for _, s := range []string{"approve cancelled", "Labelled", "colour", "behaviour", "the licence", "organisation", "summarised", "grey", "enrolment"} {
		if !britishSpelling.MatchString(s) {
			t.Errorf("%q passed", s)
		}
	}
	for _, s := range []string{"approve canceled", "labeled", "color", "behavior", "the license", "licensed", "organization", "summarized", "gray", "enrollment", "enrolled", "promise", "expertise", "otherwise", "analyses", "colorize", "CANCELLED"} {
		if w := britishSpelling.FindString(s); w != "" && strings.ToUpper(w) != w {
			t.Errorf("%q flagged %q", s, w)
		}
	}
}
