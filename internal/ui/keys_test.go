package ui

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

var update = flag.Bool("update", false, "rewrite docs/keys.md from the key table")

func repoFile(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

// TestKeysDocMatchesTable: docs/keys.md is generated from the table.
func TestKeysDocMatchesTable(t *testing.T) {
	path := repoFile("docs", "keys.md")
	want := KeysDoc()
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("docs/keys.md is stale; run: go test ./internal/ui -update")
	}
}

// TestReadmeKeysExist: every key the README's short table names is in
// the table.
func TestReadmeKeysExist(t *testing.T) {
	src, err := os.ReadFile(repoFile("README.md"))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{"enter": true, "esc": true, "space": true, "tab": true}
	for i := range bindings {
		for _, k := range bindings[i].Keys {
			if k.Rune != 0 {
				known[string(k.Rune)] = true
			} else {
				known[k.Name] = true
			}
		}
	}
	text := string(src)
	start := strings.Index(text, "## Keys")
	if start < 0 {
		t.Skip("README has no Keys section")
	}
	section := text[start:]
	if end := strings.Index(section[1:], "\n## "); end > 0 {
		section = section[:end+1]
	}
	re := regexp.MustCompile("`([^`]+)`")
	for _, m := range re.FindAllStringSubmatch(section, -1) {
		k := m[1]
		if strings.HasPrefix(k, "$") || strings.HasPrefix(k, "#") || strings.HasPrefix(k, "/") || strings.Contains(k, " ") || strings.Contains(k, "_") || len(k) > 8 {
			continue // $EDITOR, #N, /approve, a command, a config key
		}
		if !known[strings.ToLower(k)] && !known[k] {
			t.Errorf("README names key %q, which the table does not bind", k)
		}
	}
}

// TestCoreKeyGuardMatchesTable: the workflow package's reserved keys
// equal the table's list/detail/issue/any keys.
func TestCoreKeyGuardMatchesTable(t *testing.T) {
	got := workflow.CoreKeyIDs()
	want := coreKeyIDs()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("workflow.coreKeys and the ui key table differ:\n guard %v\n table %v", got, want)
	}
}

// TestKeyBarTrimKeepsQuitAndHelp: narrow bars drop by priority and keep
// q and ? to the last.
func TestKeyBarTrimKeepsQuitAndHelp(t *testing.T) {
	h := newHarness(t)
	h.load()
	for _, w := range []int{120, 60, 40, 24} {
		h.m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		s := h.screen()
		lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
		bar := strings.TrimRight(lines[len(lines)-1], " ")
		if !strings.HasSuffix(bar, "?  help") {
			t.Errorf("%d cols: bar does not end with the help chip: %q", w, bar)
		}
		if w >= 40 && !strings.Contains(bar, " q  quit ") {
			t.Errorf("%d cols: q dropped: %q", w, bar)
		}
		if w >= 120 && !strings.Contains(bar, " j/k  move ") {
			t.Errorf("%d cols: wide bar lacks j/k: %q", w, bar)
		}
	}
	items := []barItem{{"j/k", "move", 80}, {"a", "approve", 10}, {"q", "quit", 0}}
	cells := func(it barItem) int { return len(it.key) + len(it.label) + 4 }
	if got := trimBar(items, 30, cells); len(got) != 2 || got[0].key != "a" || got[1].key != "q" {
		t.Errorf("trim: %+v", got)
	}
	if got := trimBar(items, 1, cells); len(got) != 1 || got[0].key != "q" {
		t.Errorf("trim to nothing keeps q: %+v", got)
	}
}

// TestEveryTableKeyHasAHandlerCase: the handlers in app.go and
// fortress.go are plain switches; this reads their literals and checks
// them against the table both ways, so neither can drift.
func TestEveryTableKeyHasAHandlerCase(t *testing.T) {
	handled := map[string]map[widgets.Key]bool{} // view -> keys the handler names
	views := map[string][]string{
		"listKey": {ViewList}, "detailKey": {ViewDetail, "issue"}, "filesKey": {ViewFiles}, "patchKey": {ViewPatch},
		"fortressKey": {ViewFortress}, "handleKey": {anyView},
	}
	for v := range views {
		for _, view := range views[v] {
			handled[view] = map[widgets.Key]bool{}
		}
	}
	fset := token.NewFileSet()
	for _, file := range []string{"app.go", "fortress.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			vs, ok := views[fn.Name.Name]
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					var k widgets.Key
					switch x.Kind {
					case token.CHAR:
						r := []rune(strings.Trim(x.Value, "'"))
						if len(r) == 1 {
							k = widgets.R(r[0])
						}
					case token.STRING:
						name := strings.Trim(x.Value, "\"")
						if isKeyName(name) {
							k = widgets.K(name)
						}
					}
					if k != (widgets.Key{}) {
						for _, v := range vs {
							handled[v][k] = true
						}
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok {
						switch id.Name {
						case "isEnter":
							for _, v := range vs {
								handled[v][widgets.K("enter")] = true
							}
						case "isEsc":
							for _, v := range vs {
								handled[v][widgets.K("esc")] = true
							}
						}
					}
				}
				return true
			})
		}
	}
	// every table key has a case
	for i := range bindings {
		b := &bindings[i]
		for _, v := range b.Views {
			for _, k := range b.Keys {
				if !handled[v][k] && !handled[anyView][k] {
					t.Errorf("table binds %v in %s, but no handler has a case for it", k, v)
				}
			}
		}
	}
	// every handled key is in the table (the detail handler also serves
	// the issue view, whose table is a subset)
	for v, keys := range handled {
		for k := range keys {
			if v == "issue" || (v == anyView && k == widgets.R('q')) { // handleKey routes q past the action guard
				continue
			}
			if !tableHas(v, k) && !tableHas(anyView, k) {
				t.Errorf("%s handles %v, which the table does not list", v, k)
			}
		}
	}
}

func tableHas(view string, k widgets.Key) bool {
	for i := range bindings {
		b := &bindings[i]
		if !b.has(view) {
			continue
		}
		for _, bk := range b.Keys {
			if bk == k {
				return true
			}
		}
	}
	return false
}

func isKeyName(s string) bool {
	switch s {
	case "enter", "esc", "up", "down", "left", "right", "home", "end", "pgup", "pgdn", "tab", "shift+tab", "backtab", "ctrl+c":
		return true
	}
	return false
}

// TestHelpGroupsCoverTheTable: every help entry appears once, under its
// group, in a stable order.
func TestHelpGroupsCoverTheTable(t *testing.T) {
	groups := helpGroups()
	var labels []string
	all := ""
	for _, g := range groups {
		labels = append(labels, g.label)
		all += strings.Join(g.entries, "\n") + "\n"
	}
	if lines := packEntries([]string{"aa", "bbb", "cccc"}, 8); strings.Join(lines, "|") != "aa   bbb|cccc" {
		t.Errorf("packEntries: %q", lines)
	}
	if lines := packEntries([]string{"a long entry wider than the line"}, 10); len(lines) < 2 {
		t.Errorf("an entry wider than the line wraps: %q", lines)
	}
	sort.Strings(nil)
	if strings.Join(labels, ",") != "list,detail,files,diff,fortress,anywhere,prompts" {
		t.Errorf("groups %v", labels)
	}
	for _, want := range []string{"D  approve a merged row's deployments", "D  approve its waiting deployments", "ctrl-c  quit", "HJKL  look by 8"} {
		if strings.Count(all, want) != 1 {
			t.Errorf("help has %q %d times", want, strings.Count(all, want))
		}
	}
}

// TestNoSpacedHyphenSeparators: status text uses an em dash for an
// aside, never " - " (see CONTRIBUTING's style notes).
func TestNoSpacedHyphenSeparators(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, " - ") && !strings.Contains(lit.Value, "->") {
				t.Errorf("%s: %s uses \" - \" as a separator; use an em dash", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}
