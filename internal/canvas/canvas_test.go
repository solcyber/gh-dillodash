package canvas

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/text"
)

func plain(s, style string, extra text.Attr, sel, band bool) string {
	if style == "text" && !sel && !band && extra == 0 {
		return s
	}
	return "<" + style + ">" + s + "</>"
}

func TestPutClipsAndRecords(t *testing.T) {
	c := New(10, 2)
	var rec []Put
	c.Record = &rec
	x := c.PutSegs(0, 2, []text.Seg{text.S("hello", "a"), text.S(" world!", "b")}, 0, false, false)
	if x != 10 {
		t.Errorf("x = %d", x)
	}
	if got := c.Text(0); got != "  hello wo" {
		t.Errorf("row = %q", got)
	}
	if len(rec) != 2 || rec[1].Text != " wo" || rec[1].X != 7 {
		t.Errorf("record = %+v", rec)
	}
	if c.StyleAt(0, 3) != "a" || c.StyleAt(0, 8) != "b" {
		t.Errorf("styles: %q %q", c.StyleAt(0, 3), c.StyleAt(0, 8))
	}
	c.PutSegs(5, 0, []text.Seg{text.S("off", "a")}, 0, false, false) // off-canvas: no panic
	c.PutSegs(1, 0, []text.Seg{text.S("abcdef", "a")}, 3, false, false)
	if got := c.Text(1); got != "abc       " {
		t.Errorf("right clip: %q", got)
	}
}

func TestWideGlyphs(t *testing.T) {
	c := New(6, 1)
	c.PutStr(0, 0, "日本語", "t")
	if got := c.Text(0); got != "日本語" {
		t.Errorf("row = %q", got)
	}
	// overwrite the second half of a wide glyph with a narrow one
	c.PutStr(0, 1, "x", "t")
	if got := c.Text(0); got != " x本語" {
		t.Errorf("row = %q", got)
	}
	// a wide glyph that would not fit is dropped
	c.PutStr(0, 5, "語", "t")
	if got := c.Text(0); got != " x本語" {
		t.Errorf("row = %q", got)
	}
	if out := c.Render(plain); strings.Count(out, "\n") != 0 || !strings.Contains(out, "本") {
		t.Errorf("render = %q", out)
	}
}

func TestRenderCoalesces(t *testing.T) {
	c := New(8, 2)
	c.PutSegs(0, 0, []text.Seg{text.S("ab", "a"), text.S("cd", "a"), text.S("ef", "b")}, 0, false, false)
	c.Fill(1, 0, 8, "sel", true, false)
	got := c.Render(plain)
	want := "<a>abcd</><b>ef</>  \n<sel>        </>"
	if got != want {
		t.Errorf("render:\n%q\nwant\n%q", got, want)
	}
}

func TestPutRaw(t *testing.T) {
	c := New(4, 1)
	c.PutStr(0, 0, "abcd", "a")
	c.PutRaw(0, 1, "▀", "\x1b[38;2;1;2;3m")
	got := c.Render(plain)
	want := "<a>a</>\x1b[0m\x1b[38;2;1;2;3m▀\x1b[0m<a>cd</>"
	if got != want {
		t.Errorf("render:\n%q\nwant\n%q", got, want)
	}
}

// TestRawSegs: a raw segment (text.Raw) paints its cells like PutRaw.
func TestRawSegs(t *testing.T) {
	c := New(4, 1)
	c.PutStr(0, 0, "abcd", "a")
	c.Put(0, 1, text.Raw("▀▄", "\x1b[38;2;1;2;3m"))
	got := c.Render(plain)
	want := "<a>a</>\x1b[0m\x1b[38;2;1;2;3m▀\x1b[0m\x1b[0m\x1b[38;2;1;2;3m▄\x1b[0m<a>d</>"
	if got != want {
		t.Errorf("render:\n%q\nwant\n%q", got, want)
	}
}
