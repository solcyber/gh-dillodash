package widgets

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// frozen pins the effect clock so shakes and blinks are at their first
// frame and ants stay put.
func frozen(t *testing.T) {
	t.Helper()
	old, oldMotion, oldAmbient := fx.Now, fx.Motion, fx.Ambient
	fx.Now = func() float64 { return 1000 }
	fx.Motion, fx.Ambient = "full", false
	t.Cleanup(func() { fx.Now, fx.Motion, fx.Ambient = old, oldMotion, oldAmbient })
}

// keys turns a compact script into key presses: a named key in angle
// brackets ("<enter>", "<ctrl+u>"), anything else a rune.
func keys(script ...string) []Key {
	var out []Key
	for _, s := range script {
		if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
			out = append(out, K(s[1:len(s)-1]))
			continue
		}
		for _, r := range s {
			out = append(out, R(r))
		}
	}
	return out
}

// col is the cell column where sub starts in row s, or -1.
func col(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return text.Width(s[:i])
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestReasonProgress(t *testing.T) {
	ok, rem, hint := ReasonProgress("", 20, "")
	if ok || rem != 20 || hint != "20 more characters" {
		t.Errorf("empty: %v %d %q", ok, rem, hint)
	}
	ok, rem, hint = ReasonProgress(strings.Repeat("x", 19), 20, "")
	if ok || rem != 1 || hint != "1 more character" {
		t.Errorf("19: %v %d %q", ok, rem, hint)
	}
	if ok, rem, _ := ReasonProgress(strings.Repeat("x", 20), 20, ""); !ok || rem != 0 {
		t.Errorf("20: %v %d", ok, rem)
	}
	if ok, _, _ := ReasonProgress("  "+strings.Repeat("x", 19)+"   ", 20, ""); ok {
		t.Error("counted stripped, as the validator does")
	}
	ok, _, hint = ReasonProgress(`"`+strings.Repeat("x", 30), 20, `"`)
	if ok || !strings.Contains(hint, "validator") {
		t.Errorf("a double quote is refused whatever the length: %v %q", ok, hint)
	}
	if _, _, hint := ReasonProgress("ok", 2, ""); hint != "enough — enter continues" {
		t.Errorf("hint = %q", hint)
	}
	// characters, not bytes
	if ok, rem, _ := ReasonProgress("日本語", 3, ""); !ok || rem != 0 {
		t.Errorf("wide: %v %d", ok, rem)
	}
}

func TestPopHeight(t *testing.T) {
	peak := 0
	for i := 0; i <= 20; i++ {
		peak = max(peak, PopHeight(12, float64(i)/20, true))
	}
	if peak > 13 {
		t.Errorf("overshoots by more than a row: %d", peak)
	}
	if PopHeight(12, 0, false) != 12 {
		t.Error("done: full height")
	}
	if PopHeight(12, 0, true) != 2 {
		t.Errorf("start: %d", PopHeight(12, 0, true))
	}
}

func TestHazardSegsWidth(t *testing.T) {
	frozen(t)
	for _, set := range []glyphs.Set{glyphs.Nerd, glyphs.ASCII} {
		glyphs.Use(set)
		if w := text.SegsWidth(HazardSegs(33, true)); w != 33 {
			t.Errorf("%s: width %d", set, w)
		}
		if w := text.SegsWidth(HazardSegs(0, false)); w != 0 {
			t.Errorf("%s: zero width %d", set, w)
		}
	}
	glyphs.Use(glyphs.Nerd)
	segs := HazardSegs(6, false)
	if len(segs) != 2 || segs[0].Style != "hazard" || segs[1].Style != "hazard.dim" || segs[0].Text != "▚▞▚" {
		t.Errorf("segs = %+v", segs)
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  []string
	}{
		{"", 10, nil},
		{"hello world", 20, []string{"hello world"}},
		{"hello world", 5, []string{"hello", "world"}},
		{"a  b", 10, []string{"a  b"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"x abcdefgh", 4, []string{"x ab", "cdef", "gh"}},
		{"  lead", 10, []string{"lead"}},
		{"日本語 text", 4, []string{"日本", "語", "text"}},
	}
	for _, c := range cases {
		got := Wrap(c.in, c.width)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("Wrap(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
		for _, l := range got {
			if text.Width(l) > c.width {
				t.Errorf("Wrap(%q, %d): %q too wide", c.in, c.width, l)
			}
		}
	}
}

func TestDrawBlock(t *testing.T) {
	c := canvas.New(80, 24)
	top := DrawBlock(c, "post?\n/approve "+hex64+" --confirm-destroys=2", 23, "prompt", "approve.cmd")
	if top != 21 {
		t.Errorf("top = %d", top)
	}
	if got := strings.TrimRight(c.Text(21), " "); got != " post?" {
		t.Errorf("row 21 = %q", got)
	}
	// the command is wider than the row: both its halves keep the style
	if c.StyleAt(21, 1) != "prompt" || c.StyleAt(22, 1) != "approve.cmd" || c.StyleAt(23, 1) != "approve.cmd" {
		t.Errorf("styles: %q %q %q", c.StyleAt(21, 1), c.StyleAt(22, 1), c.StyleAt(23, 1))
	}
	if !strings.HasPrefix(c.Text(22), " /approve "+hex64) || !strings.Contains(c.Text(23), "--confirm-destroys=2") {
		t.Errorf("command rows:\n%q\n%q", c.Text(22), c.Text(23))
	}
	// a long block keeps its last rows, never the header rows
	many := strings.Repeat("line\n", 40)
	if top := DrawBlock(c, strings.TrimSuffix(many, "\n"), 23, "prompt", "approve.cmd"); top != 2 {
		t.Errorf("clipped top = %d", top)
	}
}

func TestPromptKeys(t *testing.T) {
	frozen(t)
	type step struct {
		key  Key
		text string // expected text after the key
		pos  int
	}
	cases := []struct {
		name    string
		initial string
		steps   []step
		done    bool
		result  string
		ok      bool
	}{
		{"backspace", "", []step{
			{R('a'), "a", 1}, {R('b'), "ab", 2}, {K("backspace"), "a", 1},
			{R('c'), "ac", 2}, {K("enter"), "ac", 2},
		}, true, "ac", true},
		{"esc cancels", "", []step{
			{R('h'), "h", 1}, {R('i'), "hi", 2}, {K("esc"), "hi", 2},
		}, true, "", false},
		{"ctrl+c cancels", "", []step{{R('h'), "h", 1}, {K("ctrl+c"), "h", 1}}, true, "", false},
		{"multibyte input", "", []step{
			{R('é'), "é", 1}, {R('中'), "é中", 2}, {K("enter"), "é中", 2},
		}, true, "é中", true},
		{"empty enter", "", []step{{K("enter"), "", 0}}, true, "", true},
		{"enter strips", "", []step{
			{R(' '), " ", 1}, {R('a'), " a", 2}, {R(' '), " a ", 3}, {K("enter"), " a ", 3},
		}, true, "a", true},
		{"initial text, cursor at the end", "seed", []step{
			{K("backspace"), "see", 3}, {R('n'), "seen", 4},
		}, false, "", false},
		{"left/right/home/end and delete", "abc", []step{
			{K("left"), "abc", 2}, {K("left"), "abc", 1}, {R('X'), "aXbc", 2},
			{K("home"), "aXbc", 0}, {K("delete"), "Xbc", 0}, {K("end"), "Xbc", 3},
			{K("delete"), "Xbc", 3}, {K("right"), "Xbc", 3}, {K("left"), "Xbc", 2},
			{K("backspace"), "Xc", 1}, {K("left"), "Xc", 0}, {K("left"), "Xc", 0},
		}, false, "", false},
		{"ctrl+a / ctrl+e / ctrl+k", "hello", []step{
			{K("ctrl+a"), "hello", 0}, {K("right"), "hello", 1}, {K("ctrl+k"), "h", 1},
			{R('i'), "hi", 2}, {K("ctrl+a"), "hi", 0}, {K("ctrl+e"), "hi", 2},
		}, false, "", false},
		{"ctrl+u kills to the start", "abc", []step{
			{K("left"), "abc", 2}, {K("ctrl+u"), "c", 0}, {K("ctrl+u"), "c", 0},
		}, false, "", false},
		{"ctrl+w kills the previous word", "foo bar  ", []step{
			{K("ctrl+w"), "foo ", 4}, {K("ctrl+w"), "", 0}, {K("ctrl+w"), "", 0},
		}, false, "", false},
		{"ctrl+w in the middle", "one two three", []step{
			{K("left"), "one two three", 12}, {K("left"), "one two three", 11},
			{K("ctrl+w"), "one two ee", 8},
		}, false, "", false},
		{"wide chars delete whole", "日本語", []step{
			{K("backspace"), "日本", 2}, {K("left"), "日本", 1}, {K("delete"), "日", 1},
		}, false, "", false},
		{"unprintable and tab ignored", "a", []step{
			{K("tab"), "a", 1}, {R(0x07), "a", 1}, {K("up"), "a", 1},
		}, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewPrompt("> ", c.initial)
			done := false
			for i, s := range c.steps {
				done = p.Handle(s.key)
				if p.Text() != s.text || p.Pos() != s.pos {
					t.Fatalf("step %d (%+v): text %q pos %d, want %q %d", i, s.key, p.Text(), p.Pos(), s.text, s.pos)
				}
			}
			if done != c.done || p.Done() != c.done {
				t.Errorf("done = %v, want %v", done, c.done)
			}
			if got, ok := p.Result(); got != c.result || ok != c.ok {
				t.Errorf("result = %q, %v; want %q, %v", got, ok, c.result, c.ok)
			}
		})
	}
}

func TestPromptOnChange(t *testing.T) {
	frozen(t)
	var seen []string
	p := NewPrompt("/", "")
	p.OnChange = func(s string) { seen = append(seen, s) }
	for _, k := range keys("ab", "<left>", "<home>", "<delete>", "<ctrl+u>", "c", "<backspace>") {
		p.Handle(k)
	}
	want := "a|ab|b|cb|b"
	if got := strings.Join(seen, "|"); got != want {
		t.Errorf("on_change saw %q, want %q (cursor moves and no-op edits never fire)", got, want)
	}
}

func TestPromptMinLenRefusal(t *testing.T) {
	frozen(t)
	p := NewPrompt("why?", "")
	p.MinLen = 5
	for _, k := range keys("abc") {
		p.Handle(k)
	}
	if p.Handle(K("enter")) || p.Done() {
		t.Fatal("a short reason must be refused, not returned")
	}
	if !p.Refused() {
		t.Error("refused state after a short enter")
	}
	if !p.Active() {
		t.Error("the refusal shakes")
	}
	p.Handle(R('d'))
	if p.Refused() {
		t.Error("an edit clears the refusal")
	}
	// stripped length counts: "abcd " is still 4
	p.Handle(R(' '))
	if p.Handle(K("enter")) {
		t.Fatal("trailing spaces do not count")
	}
	p.Handle(R('e'))
	if !p.Handle(K("enter")) {
		t.Fatal("five characters: enter continues")
	}
	if got, ok := p.Result(); got != "abcd e" || !ok {
		t.Errorf("result = %q %v", got, ok)
	}
	// esc still cancels a short reason
	q := NewPrompt("why?", "ab")
	q.MinLen = 5
	if !q.Handle(K("esc")) {
		t.Error("esc cancels")
	}
	if _, ok := q.Result(); ok {
		t.Error("cancelled: ok false")
	}
}

func TestPromptForbid(t *testing.T) {
	frozen(t)
	p := NewPrompt("why?", "")
	p.MinLen = 3
	p.Forbid = `"`
	changes := 0
	p.OnChange = func(string) { changes++ }
	p.Handle(R('a'))
	p.Handle(R('"'))
	if p.Text() != "a" {
		t.Errorf("forbidden char typed: %q", p.Text())
	}
	if !strings.Contains(p.Note(), "validator") || !strings.Contains(p.Note(), `"`) {
		t.Errorf("note = %q", p.Note())
	}
	if changes != 1 {
		t.Errorf("a refused key is not a change: %d", changes)
	}
	p.Handle(R('b'))
	if p.Note() != "" {
		t.Error("an edit clears the note")
	}
	// a forbidden character in the initial text blocks enter whatever the length
	q := NewPrompt("why?", `say "hi" please`)
	q.MinLen = 3
	q.Forbid = `"`
	if q.Handle(K("enter")) {
		t.Error("forbidden char in the text: enter refused")
	}
}

func TestPromptDraw(t *testing.T) {
	frozen(t)
	glyphs.Use(glyphs.Nerd)
	c := canvas.New(80, 24)
	p := NewPrompt("comment> ", "")
	for _, k := range keys("hi!") {
		p.Handle(k)
	}
	p.Draw(c, nil)
	if got := strings.TrimRight(c.Text(22), " "); got != " comment>" {
		t.Errorf("question row = %q", got)
	}
	if c.StyleAt(22, 1) != "prompt" {
		t.Errorf("question style = %q", c.StyleAt(22, 1))
	}
	if got := strings.TrimRight(c.Text(23), " "); got != "› hi!" {
		t.Errorf("input row = %q", got)
	}
	if c.StyleAt(23, 0) != "logo" || c.StyleAt(23, 2) != "input" {
		t.Errorf("input styles: %q %q", c.StyleAt(23, 0), c.StyleAt(23, 2))
	}
	if p.CursorY != 23 || p.CursorX != 5 {
		t.Errorf("cursor at %d,%d", p.CursorY, p.CursorX)
	}
	// wide characters: the cursor sits after both cells
	w := NewPrompt("> ", "中a")
	w.Draw(c, nil)
	if w.CursorX != 2+3 {
		t.Errorf("wide cursor x = %d", w.CursorX)
	}
	w.Handle(K("home"))
	w.Handle(K("right"))
	w.Draw(c, nil)
	if w.CursorX != 4 {
		t.Errorf("cursor after one wide char x = %d", w.CursorX)
	}
	// a line longer than the row scrolls so the cursor stays shown
	long := NewPrompt("> ", strings.Repeat("x", 100))
	long.Draw(c, nil)
	if long.CursorX > 78 {
		t.Errorf("cursor off screen: %d", long.CursorX)
	}
	if !strings.HasPrefix(c.Text(23), "› xxx") {
		t.Errorf("scrolled input = %q", c.Text(23))
	}
	// the rows are never wider than the canvas (a long question wraps)
	lq := NewPrompt("No live plan to /approve on #3543 (acme/15-window-canary: superseded, "+
		"acme/05-net: no changes). Submit a GitHub review approval as alice?", "")
	c = canvas.New(80, 24)
	lq.Draw(c, nil)
	if !strings.Contains(c.Text(21), "No live plan") || !strings.Contains(c.Text(22), "alice?") {
		t.Errorf("wrapped question:\n%q\n%q", c.Text(21), c.Text(22))
	}
}

func TestPromptDrawReasonFooter(t *testing.T) {
	frozen(t)
	glyphs.Use(glyphs.Nerd)
	c := canvas.New(80, 24)
	p := NewPrompt("why outside the window?", "")
	p.MinLen = 20
	p.Forbid = `"`
	p.Draw(c, nil)
	// question above the hint row, input on h-2, meter on h-1
	if got := strings.TrimRight(c.Text(20), " "); got != " why outside the window?" {
		t.Errorf("question row = %q", got)
	}
	if !strings.Contains(c.Text(21), "20 more characters") || c.StyleAt(21, 1) != "hint.short" {
		t.Errorf("hint row = %q (%s)", c.Text(21), c.StyleAt(21, 1))
	}
	if !strings.HasSuffix(strings.TrimRight(c.Text(21), " "), `no " · esc cancels`) {
		t.Errorf("keys = %q", c.Text(21))
	}
	if !strings.HasPrefix(c.Text(22), "› ") || p.CursorY != 22 {
		t.Errorf("input row = %q cursor %d", c.Text(22), p.CursorY)
	}
	if !strings.Contains(c.Text(23), " 0/20 ") || c.StyleAt(23, 1) != "meter.track" {
		t.Errorf("meter row = %q (%s)", c.Text(23), c.StyleAt(23, 1))
	}
	label := col(c.Text(23), " 0/20 ")
	if c.StyleAt(23, label+1) != "meter.label" {
		t.Errorf("label style = %q", c.StyleAt(23, label+1))
	}
	// twelve characters: the countdown and a gold partial fill
	for _, k := range keys("twelve chars") {
		p.Handle(k)
	}
	p.Draw(c, nil)
	if !strings.Contains(c.Text(21), "8 more characters") {
		t.Errorf("hint = %q", c.Text(21))
	}
	if !strings.Contains(c.Text(23), " 12/20 ") || c.StyleAt(23, 1) != "meter.short" {
		t.Errorf("meter = %q (%s)", c.Text(23), c.StyleAt(23, 1))
	}
	label = col(c.Text(23), " 12/20 ")
	if c.StyleAt(23, label+1) != "meter.label.short" {
		t.Errorf("label style = %q", c.StyleAt(23, label+1))
	}
	// a refused enter: red hint, the track blinks bad
	p.Handle(K("enter"))
	p.Draw(c, nil)
	if !strings.Contains(c.Text(21), "8 more characters before this can go on") || c.StyleAt(21, 1) != "hint.bad" {
		t.Errorf("refused hint = %q (%s)", c.Text(21), c.StyleAt(21, 1))
	}
	if c.StyleAt(23, 70) != "meter.track.bad" {
		t.Errorf("refused track = %q", c.StyleAt(23, 70))
	}
	// a forbidden key: its note replaces the hint
	p.Handle(R('"'))
	p.Draw(c, nil)
	if !strings.Contains(c.Text(21), `no " — the validator refuses it`) || c.StyleAt(21, 1) != "hint.bad" {
		t.Errorf("note row = %q", c.Text(21))
	}
	// enough: blue, "enter continues", and the shine sweeps the bar
	for _, k := range keys(" and more") {
		p.Handle(k)
	}
	p.Draw(c, nil)
	if !strings.Contains(c.Text(21), "enough — enter continues") || c.StyleAt(21, 1) != "hint.ok" {
		t.Errorf("ok hint = %q (%s)", c.Text(21), c.StyleAt(21, 1))
	}
	if c.StyleAt(23, 1) != "meter.ok" && c.StyleAt(23, 1) != "meter.shine" {
		t.Errorf("ok fill = %q", c.StyleAt(23, 1))
	}
	if !p.Active() {
		t.Error("the shine plays once the reason fills")
	}
	label = col(c.Text(23), " 21/20")
	if label < 0 || c.StyleAt(23, label+1) != "meter.label.ok" {
		t.Errorf("ok label: %q", c.Text(23))
	}
	if !p.Animated() {
		t.Error("a min-length prompt is animated")
	}
	if !p.Handle(K("enter")) {
		t.Error("enter continues")
	}
}

func TestPromptDrawHazard(t *testing.T) {
	frozen(t)
	glyphs.Use(glyphs.Nerd)
	c := canvas.New(80, 24)
	p := NewPrompt("how many destroys?", "")
	p.Hazard = true
	p.Draw(c, nil)
	if c.StyleAt(21, 1) != "hazard" || !strings.HasPrefix(c.Text(21), " ▚▞▚▞▚▞") {
		t.Errorf("stripe row = %q (%s)", c.Text(21), c.StyleAt(21, 1))
	}
	if strings.TrimSpace(c.Text(22)) != "how many destroys?" {
		t.Errorf("question = %q", c.Text(22))
	}
	if !p.Animated() {
		t.Error("the stripe marches")
	}
	// the cursor cell is marked on the input row
	if got := c.Text(23); !strings.HasPrefix(got, "› ") {
		t.Errorf("input = %q", got)
	}
}

func TestPromptApproveLineStyle(t *testing.T) {
	frozen(t)
	c := canvas.New(80, 24)
	p := NewPrompt("post this?\n/approve "+hex64, "")
	p.Draw(c, nil)
	if c.StyleAt(21, 1) != "prompt" || c.StyleAt(22, 1) != "approve.cmd" {
		t.Errorf("styles: %q %q", c.StyleAt(21, 1), c.StyleAt(22, 1))
	}
}

func TestConfirm(t *testing.T) {
	frozen(t)
	cases := []struct {
		key  Key
		want bool
	}{
		{R('y'), true}, {R('Y'), true}, {R('n'), false}, {R('N'), false},
		{K("enter"), false}, {K("esc"), false}, {R(' '), false}, {K("up"), false},
	}
	for _, c := range cases {
		cf := NewConfirm("post?")
		if !cf.Handle(c.key) || cf.Done() != true {
			t.Errorf("%+v: any key answers", c.key)
		}
		if cf.Result() != c.want {
			t.Errorf("%+v: result %v, want %v", c.key, cf.Result(), c.want)
		}
	}
	cv := canvas.New(80, 24)
	NewConfirm("post?").Draw(cv, nil)
	if got := strings.TrimRight(cv.Text(23), " "); got != " post? [y/N]" {
		t.Errorf("row 23 = %q", got)
	}
	longQ := "No live plan to /approve on #3543 (acme/15-window-canary: superseded, " +
		"acme/05-net: no changes). Submit a GitHub review approval as alice?"
	cv = canvas.New(80, 24)
	NewConfirm(longQ).Draw(cv, nil)
	if !strings.Contains(cv.Text(23), "[y/N]") || !strings.Contains(cv.Text(22), "No live plan") {
		t.Errorf("a long question wraps and keeps its [y/N]:\n%q\n%q", cv.Text(22), cv.Text(23))
	}
	// an /approve line is drawn in the command style; the theme is mono in
	// tests, so it does not breathe
	cv = canvas.New(80, 24)
	a := NewConfirm("/approve " + hex64 + "\nPost this?")
	a.Draw(cv, nil)
	if a.Animated() {
		t.Error("mono: no breathing")
	}
	if cv.StyleAt(22, 1) != "approve.cmd" || cv.StyleAt(23, 1) != "prompt" {
		t.Errorf("styles: %q %q", cv.StyleAt(22, 1), cv.StyleAt(23, 1))
	}
	if got := strings.TrimRight(cv.Text(23), " "); got != " Post this? [y/N]" {
		t.Errorf("the [y/N] sits after the question: %q", got)
	}
}

func TestPickKeys(t *testing.T) {
	frozen(t)
	thirty := make([]string, 30)
	for i := range thirty {
		thirty[i] = strings.Repeat("x", i%3+1)
	}
	cases := []struct {
		name   string
		labels []string
		keys   []Key
		result int
		done   bool
	}{
		{"j j enter -> third", []string{"a", "b", "c", "d"}, keys("jj", "<enter>"), 2, true},
		{"q cancels", []string{"a"}, keys("q"), -1, true},
		{"esc cancels", []string{"a"}, keys("<esc>"), -1, true},
		{"digit picks", []string{"a", "b", "c"}, keys("3"), 2, true},
		{"digit past the end is ignored", []string{"a", "b"}, keys("3"), -1, false},
		{"k wraps to the last (all reachable)", thirty, keys("k", "<enter>"), 29, true},
		{"j wraps to the first", []string{"a", "b"}, keys("jj", "<enter>"), 0, true},
		{"arrows", []string{"a", "b", "c"}, keys("<down>", "<down>", "<up>", "<enter>"), 1, true},
		{"g/G", thirty, keys("G", "<enter>"), 29, true},
		{"home/end", thirty, keys("<end>", "<home>", "<enter>"), 0, true},
		{"pgdn clamps", thirty, keys("<pgdn>", "<pgdn>", "<pgdn>", "<pgdn>", "<enter>"), 29, true},
		{"pgup clamps", thirty, keys("G", "<pgup>", "<pgup>", "<pgup>", "<pgup>", "<enter>"), 0, true},
		{"other keys ignored", []string{"a", "b"}, keys("zx", "<tab>", "<left>"), -1, false},
		{"empty menu: any key ends it", nil, keys("j"), -1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewPick("t", c.labels, "")
			done := false
			for _, k := range c.keys {
				done = p.Handle(k)
			}
			if done != c.done || p.Result() != c.result {
				t.Errorf("done %v result %d, want %v %d", done, p.Result(), c.done, c.result)
			}
		})
	}
	// pgdn moves by the rows the last draw showed
	p := NewPick("t", thirty, "")
	p.Draw(canvas.New(80, 24), nil) // room = 24 - 8 = 16
	p.Handle(K("pgdn"))
	if p.Selected() != 16 {
		t.Errorf("pgdn by a page: %d", p.Selected())
	}
}

func TestPickDraw(t *testing.T) {
	frozen(t)
	glyphs.Use(glyphs.Nerd)
	c := canvas.New(80, 24)
	p := NewPick("choose", []string{"alpha", "beta", "gamma"}, "")
	p.Handle(R('j'))
	p.Draw(c, nil)
	// the box at row 2, column 1, 48 wide, with the title in its border
	if !strings.HasPrefix(c.Text(2), " ╭─ choose ─") || c.StyleAt(2, 4) != "menu.title" || c.StyleAt(2, 1) != "logo" {
		t.Errorf("border = %q (%s %s)", c.Text(2), c.StyleAt(2, 1), c.StyleAt(2, 4))
	}
	if c.Text(2)[len(c.Text(2))-1] != ' ' || !strings.Contains(c.Text(2), "╮ ") {
		t.Errorf("border end = %q", c.Text(2))
	}
	if got, want := strings.TrimRight(c.Text(3), " "), " │ 1 alpha"+strings.Repeat(" ", 38)+"│"; got != want {
		t.Errorf("row 3 = %q, want %q", got, want)
	}
	if got, want := strings.TrimRight(c.Text(4), " "), " │▌2 beta"+strings.Repeat(" ", 39)+"│"; got != want {
		t.Errorf("row 4 = %q, want %q", got, want)
	}
	if c.StyleAt(4, 2) != "gutter" || c.StyleAt(4, 5) != "bright" || c.StyleAt(3, 5) != "menu.item" || c.StyleAt(3, 3) != "faint" {
		t.Errorf("item styles: %q %q %q %q", c.StyleAt(4, 2), c.StyleAt(4, 5), c.StyleAt(3, 5), c.StyleAt(3, 3))
	}
	// the hint is clipped at the interior's edge
	if !strings.Contains(c.Text(7), "j/k move · enter choose · 1-9 pick · esc can │") || c.StyleAt(7, 3) != "menu.hint" {
		t.Errorf("hint row = %q", c.Text(7))
	}
	if !strings.HasPrefix(c.Text(8), " ╰") || !strings.Contains(c.Text(8), "╯") {
		t.Errorf("bottom = %q", c.Text(8))
	}
	if strings.TrimSpace(c.Text(9)) != "" {
		t.Errorf("nothing under the box: %q", c.Text(9))
	}
	// a long list scrolls and says where it is
	thirty := make([]string, 30)
	for i := range thirty {
		thirty[i] = "item " + strings.Repeat("-", 53) // wide enough for the whole hint
	}
	c = canvas.New(80, 24)
	q := NewPick("t", thirty, "")
	q.Handle(K("end"))
	q.Draw(c, nil)
	found := false
	for y := 0; y < 24; y++ {
		if strings.Contains(c.Text(y), "(15-30 of 30)") {
			found = true
		}
	}
	if !found {
		t.Errorf("scrolled hint missing:\n%s", strings.Join(c.Lines(), "\n"))
	}
	if !strings.Contains(c.Text(3), "   item") || strings.Contains(c.Text(3), "1 item") {
		t.Errorf("first shown row = %q", c.Text(3))
	}
	if !strings.Contains(c.Text(18), "▌  item") {
		t.Errorf("selected last = %q", c.Text(18))
	}
	// a title too long for the border wraps inside the box
	c = canvas.New(80, 24)
	long := strings.Repeat("word ", 12)
	r := NewPick(strings.TrimSpace(long), []string{"a"}, "")
	r.Draw(c, nil)
	if strings.Contains(c.Text(2), "word") {
		t.Errorf("long title in the border: %q", c.Text(2))
	}
	if !strings.Contains(c.Text(3), "word word") || c.StyleAt(3, 3) != "menu.title" {
		t.Errorf("title inside = %q", c.Text(3))
	}
	// the box widens for long labels, up to the screen
	c = canvas.New(80, 24)
	NewPick("t", []string{strings.Repeat("z", 60)}, "").Draw(c, nil)
	if !strings.HasSuffix(strings.TrimRight(c.Text(2), " "), "╮") || text.Width(strings.TrimRight(c.Text(2), " ")) != 71 {
		t.Errorf("wide box = %q (%d)", c.Text(2), text.Width(strings.TrimRight(c.Text(2), " ")))
	}
}

func TestPickPopAndAnts(t *testing.T) {
	frozen(t)
	glyphs.Use(glyphs.Nerd)
	f := fx.New()
	p := NewPick("t", []string{"a", "b"}, "menu")
	p.Open(f)
	if _, live := f.T("menu"); !live {
		t.Fatal("Open starts the pop timer")
	}
	c := canvas.New(80, 24)
	p.Draw(c, f)
	// at the start of the pop the box is two rows tall
	if !strings.HasPrefix(c.Text(2), " ╭") || !strings.HasPrefix(c.Text(3), " ╰") || strings.TrimSpace(c.Text(4)) != "" {
		t.Errorf("popping box:\n%q\n%q\n%q", c.Text(2), c.Text(3), c.Text(4))
	}
	// once over, full height
	f.Stop("menu")
	c = canvas.New(80, 24)
	p.Draw(c, f)
	if !strings.HasPrefix(c.Text(7), " ╰") {
		t.Errorf("settled box bottom = %q", c.Text(7))
	}
	// ambient: a band of light runs round the border
	fx.Ambient = true
	c = canvas.New(80, 24)
	p.Draw(c, f)
	lit := 0
	for _, pos := range [][2]int{{2, 1}, {2, 48}, {7, 1}, {7, 48}, {4, 1}, {4, 48}} {
		if st := c.StyleAt(pos[0], pos[1]); strings.HasPrefix(st, "fx.b") {
			lit++
		}
	}
	if lit == 0 {
		t.Error("no ants on the border")
	}
	// motion off: no pop, no ants
	fx.Motion = "off"
	fx.Ambient = true
	c = canvas.New(80, 24)
	p.Draw(c, f)
	if p.Animated() || !strings.HasPrefix(c.Text(7), " ╰") {
		t.Error("motion off: static, full box")
	}
}

func TestDrawBox(t *testing.T) {
	glyphs.Use(glyphs.Nerd)
	c := canvas.New(20, 6)
	c.Fill(2, 0, 20, "x", false, false)
	c.PutStr(2, 5, "junk", "x")
	top, left, iw := DrawBox(c, 1, 2, 4, 12, "hi", "card.title", "card", -1)
	if top != 2 || left != 4 || iw != 8 {
		t.Errorf("interior = %d,%d,%d", top, left, iw)
	}
	want := []string{
		strings.Repeat(" ", 20),
		"  ╭─ hi ─────╮      ",
		"  │          │      ",
		"  │          │      ",
		"  ╰──────────╯      ",
		strings.Repeat(" ", 20),
	}
	for y, w := range want {
		if got := c.Text(y); got != w {
			t.Errorf("row %d = %q, want %q", y, got, w)
		}
	}
	if c.StyleAt(1, 2) != "card" || c.StyleAt(1, 4) != "card.title" || c.StyleAt(2, 6) != "text" {
		t.Errorf("styles: %q %q %q", c.StyleAt(1, 2), c.StyleAt(1, 4), c.StyleAt(2, 6))
	}
	// ants at phase 0: the band starts at the top-left corner
	DrawBox(c, 1, 2, 4, 12, "", "card.title", "card", 0)
	if c.StyleAt(1, 2) != "fx.b7" || c.StyleAt(1, 3) != "fx.b7" || c.StyleAt(1, 4) != "fx.b6" || c.StyleAt(1, 10) != "fx.b5" || c.StyleAt(1, 12) != "card" {
		t.Errorf("ants: %q %q %q %q %q", c.StyleAt(1, 2), c.StyleAt(1, 3), c.StyleAt(1, 4), c.StyleAt(1, 10), c.StyleAt(1, 12))
	}
	// a two-row box: top and bottom only
	c = canvas.New(10, 3)
	DrawBox(c, 0, 0, 2, 4, "", "card.title", "card", -1)
	if c.Text(0) != "╭──╮      " || c.Text(1) != "╰──╯      " {
		t.Errorf("tiny box:\n%q\n%q", c.Text(0), c.Text(1))
	}
	// ascii glyphs
	glyphs.Use(glyphs.ASCII)
	defer glyphs.Use(glyphs.Nerd)
	c = canvas.New(10, 3)
	DrawBox(c, 0, 0, 3, 5, "", "card.title", "card", -1)
	if c.Text(0) != "+---+     " || c.Text(1) != "|   |     " || c.Text(2) != "+---+     " {
		t.Errorf("ascii box:\n%q\n%q\n%q", c.Text(0), c.Text(1), c.Text(2))
	}
}

var _ = []Modal{(*Prompt)(nil), (*Confirm)(nil), (*Pick)(nil)}
