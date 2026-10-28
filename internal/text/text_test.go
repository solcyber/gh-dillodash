package text

import (
	"strings"
	"testing"
)

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

func TestEllipLeft(t *testing.T) {
	if got := EllipLeft("short", 10); got != "short" {
		t.Errorf("fits: %q", got)
	}
	got := EllipLeft("a/very/long/path/to/name.tf", 12)
	if Width(got) != 12 || !strings.HasSuffix(got, "to/name.tf") || !strings.HasPrefix(got, Ellipsis) {
		t.Errorf("ascii: %q (width %d)", got, Width(got))
	}
	got = EllipLeft("日本語のファイル名.txt", 9)
	if Width(got) > 9 || !strings.HasSuffix(got, ".txt") {
		t.Errorf("cjk: %q (width %d)", got, Width(got))
	}
	if got := EllipLeft("abcdef", 1); Width(got) > 1 {
		t.Errorf("one cell: %q", got)
	}
	if got := EllipLeft("abcdef", 0); got != "" {
		t.Errorf("zero: %q", got)
	}
}

func TestEllipSegs(t *testing.T) {
	segs := []Seg{S("hello ", "a"), S("world", "b")}
	if got := EllipSegs(segs, 20); len(got) != 2 {
		t.Errorf("fits: %+v", got)
	}
	got := EllipSegs(segs, 8)
	if SegsWidth(got) != 8 || !strings.HasSuffix(SegsText(got), Ellipsis) || got[len(got)-1].Style != "b" {
		t.Errorf("cut: %+v", got)
	}
	if got := EllipSegs(segs, 1); SegsWidth(got) > 1 {
		t.Errorf("one cell: %+v", got)
	}
}
