package glyphs

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"unicode"

	"github.com/solcyber/gh-dillodash/internal/text"
)

func TestEverySetDefinesTheSameNames(t *testing.T) {
	for _, set := range []Set{Unicode, ASCII} {
		for n := range nerd {
			if _, ok := sets[set][n]; !ok {
				t.Errorf("%s set lacks %q", set, n)
			}
		}
		for n := range sets[set] {
			if _, ok := nerd[n]; !ok {
				t.Errorf("%s set has %q, nerd does not", set, n)
			}
		}
	}
	if len(Names()) != len(nerd) {
		t.Errorf("Names() = %d names, want %d", len(Names()), len(nerd))
	}
}

func TestASCIISetIsPureASCII(t *testing.T) {
	for n, s := range ascii {
		for _, r := range s {
			if r >= 128 {
				t.Errorf("ascii %q has %U", n, r)
			}
		}
	}
}

// drawn is every character the nerd and unicode sets draw.
func drawn() map[rune]bool {
	out := map[rune]bool{}
	for _, set := range []Set{Nerd, Unicode} {
		for _, s := range sets[set] {
			for _, r := range s {
				out[r] = true
			}
		}
	}
	return out
}

func TestNerdIconsAreBMPPrivateUseOnly(t *testing.T) {
	// never U+F0000+: the nf-md range is unprintable to some wcwidth tables
	n := 0
	for name, s := range nerd {
		for _, r := range s {
			if r >= 0xE000 {
				n++
				if r > 0xF8FF {
					t.Errorf("nerd %q uses %U outside the BMP private-use area", name, r)
				}
			}
		}
	}
	if n == 0 {
		t.Error("no private-use glyphs in the nerd set")
	}
}

func TestEveryGlyphIsPrintableOrPrivateUse(t *testing.T) {
	for r := range drawn() {
		pua := r >= 0xE000 && r <= 0xF8FF
		if !pua && !unicode.IsPrint(r) {
			t.Errorf("%U is neither printable nor BMP private-use", r)
		}
	}
}

func TestEveryGlyphIsOneCell(t *testing.T) {
	for r := range drawn() {
		if w := text.RuneWidth(r); w != 1 {
			t.Errorf("%U is %d cells wide", r, w)
		}
	}
}

func TestNoGlyphUnicode16MadeWide(t *testing.T) {
	// Unicode 16.0 made the I Ching trigrams and monograms/digrams
	// East_Asian_Width=W: libc wcwidth says 2 there and 1 on an older
	// libc, so a drawn one passes on one host and fails on the next.
	for r := range drawn() {
		if (r >= 0x2630 && r < 0x2638) || (r >= 0x268A && r < 0x2690) {
			t.Errorf("%U is wide under Unicode 16", r)
		}
	}
}

func TestUseSwitchesSetAndEllipsis(t *testing.T) {
	defer Use(Nerd)
	Use(ASCII)
	if Active() != ASCII || text.Ellipsis != ".." || G("pass") != "+" {
		t.Errorf("ascii: active=%s ellipsis=%q pass=%q", Active(), text.Ellipsis, G("pass"))
	}
	Use(Unicode)
	if Active() != Unicode || text.Ellipsis != "…" || G("pass") != "√" {
		t.Errorf("unicode: active=%s ellipsis=%q", Active(), text.Ellipsis)
	}
	Use(Set("bogus"))
	if Active() != Nerd || G("pass") != "" {
		t.Errorf("unknown set should select nerd, got %s", Active())
	}
}

func TestIcoAppendsNoBreakSpace(t *testing.T) {
	defer Use(Nerd)
	Use(Nerd)
	if got := Ico("pass"); got != " " {
		t.Errorf("nerd Ico = %q", got)
	}
	Use(ASCII)
	if got := Ico("pass"); got != "+ " {
		t.Errorf("ascii Ico = %q", got)
	}
	if got := Ico("window"); got != "" {
		t.Errorf("ascii Ico(window) = %q, want empty (no invented letters)", got)
	}
	if got := Ico("no.such.glyph"); got != "" {
		t.Errorf("unknown Ico = %q", got)
	}
}

func TestUnknownNameDrawsQuestionMarkAndIsRecorded(t *testing.T) {
	if got := G("no.such.glyph"); got != "?" {
		t.Errorf("G(unknown) = %q", got)
	}
	found := false
	for _, n := range Missed() {
		if n == "no.such.glyph" {
			found = true
		}
	}
	if !found {
		t.Errorf("Missed() = %v, want the unknown name recorded", Missed())
	}
}

func TestEveryCIStateHasItsOwnShape(t *testing.T) {
	defer Use(Nerd)
	for _, set := range []Set{Nerd, Unicode, ASCII} {
		Use(set)
		shapes := map[string]bool{}
		for _, n := range []string{"pass", "fail", "wait", "spinner", "skip", "unknown"} {
			shapes[string([]rune(G(n))[0])] = true
		}
		if len(shapes) != 6 {
			t.Errorf("%s: CI states share a shape: %v", set, shapes)
		}
	}
}

func TestSpinnerFrameIsOneFrame(t *testing.T) {
	defer Use(Nerd)
	Use(Nerd)
	f := SpinnerFrame()
	if !strings.Contains(G("spinner"), f) || len([]rune(f)) != 1 {
		t.Errorf("SpinnerFrame = %q", f)
	}
	Use(ASCII)
	if SpinnerFrame() != "~" {
		t.Errorf("ascii spinner = %q", SpinnerFrame())
	}
}

func TestAllCopiesTheSet(t *testing.T) {
	m := All(Nerd)
	m["pass"] = "changed"
	if nerd["pass"] == "changed" {
		t.Error("All returned the live map")
	}
	if All(Set("bogus")) != nil {
		t.Error("All(bogus) should be nil")
	}
}

func TestMirrorRoundTrips(t *testing.T) {
	m := Mirror(Mirror(Armadillo))
	for i := range Armadillo {
		if strings.TrimRight(m[i], " ") != strings.TrimRight(Armadillo[i], " ") {
			t.Errorf("row %d: %q != %q", i, m[i], Armadillo[i])
		}
	}
	once := Mirror(Armadillo)
	if !strings.HasPrefix(strings.TrimLeft(once[3], " "), "<   o  )") {
		t.Errorf("mirrored snout = %q", once[3])
	}
	if len(ArmadilloStep) != len(Armadillo) || ArmadilloStep[0] != Armadillo[0] ||
		ArmadilloStep[4] == Armadillo[4] {
		t.Errorf("ArmadilloStep = %q", ArmadilloStep)
	}
}

func TestLogotypeRowsLineUp(t *testing.T) {
	for name, art := range map[string][]string{"Logotype": Logotype, "LogotypeASCII": LogotypeASCII} {
		widths := map[int]bool{}
		for _, r := range art {
			widths[text.Width(r)] = true
		}
		if len(widths) != 1 {
			t.Errorf("%s rows have widths %v", name, widths)
		}
	}
	if text.Width(Logotype[0]) >= 80 {
		t.Errorf("Logotype is %d cells, too wide for an 80-column terminal", text.Width(Logotype[0]))
	}
	for _, r := range LogotypeASCII {
		for _, c := range r {
			if c >= 128 {
				t.Errorf("LogotypeASCII has %U", c)
			}
		}
	}
}

func TestEveryLogotypeGlyphIsInPx(t *testing.T) {
	// so the font test covers them
	for _, r := range Logotype {
		for _, c := range r {
			if c != ' ' && !strings.ContainsRune(nerd["px"], c) {
				t.Errorf("logotype glyph %q (%U) not in px", string(c), c)
			}
		}
	}
}

func TestReportListsEverySet(t *testing.T) {
	var buf bytes.Buffer
	called := false
	Report(&buf, func(w io.Writer) { called = true; w.Write([]byte("palette")) })
	out := buf.String()
	if !strings.Contains(out, "Hack Nerd Font Mono") || !strings.Contains(out, nerd["pr"]) ||
		!strings.Contains(out, unicodeSet["pr"]) || !strings.Contains(out, "(none)") {
		t.Errorf("report:\n%s", out)
	}
	if !called || !strings.HasSuffix(out, "palette") {
		t.Error("palette func not written after the table")
	}
}
