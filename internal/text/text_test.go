package text

import "testing"

func TestWidthAndClip(t *testing.T) {
	cases := []struct {
		s    string
		w    int
		clip int
		want string
	}{
		{"hello", 5, 3, "hel"},
		{"日本語", 6, 3, "日"},
		{"日本語", 6, 4, "日本"},
		{"áb", 2, 1, "á"},
		{" ok", 4, 2, " "}, // nerd glyph, one cell
		{"🙂x", 3, 2, "🙂"},
	}
	for _, c := range cases {
		if got := Width(c.s); got != c.w {
			t.Errorf("Width(%q) = %d, want %d", c.s, got, c.w)
		}
		if got := Clip(c.s, c.clip); got != c.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.s, c.clip, got, c.want)
		}
	}
}

func TestEllipPad(t *testing.T) {
	if got := Ellip("abcdefgh", 5); got != "abcd…" {
		t.Errorf("Ellip = %q", got)
	}
	if got := Ellip("abc", 5); got != "abc" {
		t.Errorf("Ellip short = %q", got)
	}
	if got := Pad("日本", 5); got != "日本 " || Width(got) != 5 {
		t.Errorf("Pad = %q", got)
	}
	if got := LPad("ab", 4); got != "  ab" {
		t.Errorf("LPad = %q", got)
	}
}

func TestClean(t *testing.T) {
	if got := Clean("a\tb"); got != "a       b" {
		t.Errorf("tabs: %q", got)
	}
	if got := Clean("x\x00y\x1bz\r\nw"); got != "x·y·z w" {
		t.Errorf("ctrl: %q", got)
	}
	ASCIIOnly = true
	defer func() { ASCIIOnly = false }()
	if got := Clean("a — b … é"); got != "a - b ... ?" {
		t.Errorf("ascii: %q", got)
	}
}

func TestSegs(t *testing.T) {
	segs := []Seg{S("abc", "a"), S("defgh", "b")}
	if w := SegsWidth(segs); w != 8 {
		t.Errorf("SegsWidth = %d", w)
	}
	p := PadSegs(segs, 10)
	if SegsWidth(p) != 10 || p[len(p)-1].Text != "  " {
		t.Errorf("PadSegs = %+v", p)
	}
	c := ClipSegs(segs, 4)
	if SegsText(c) != "abcd" || len(c) != 2 {
		t.Errorf("ClipSegs = %+v", c)
	}
	hits := OverlayHits([]Seg{S("find me here", "t")}, []Span{{5, 7, true}})
	if len(hits) != 3 || hits[1].Text != "me" || hits[1].Extra&Reverse == 0 || hits[0].Extra != 0 {
		t.Errorf("OverlayHits = %+v", hits)
	}
}
