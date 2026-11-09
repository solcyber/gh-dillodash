package text

import "testing"

func TestForPager(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain\tline\n", "plain\tline\n"},
		{"\x1b[32mok\x1b[0m", "\x1b[32mok\x1b[0m"},                           // colour stays
		{"a\x1b]52;c;ZXZpbA==\x07b", "ab"},                                   // clipboard write gone
		{"a\x1b]8;;https://evil.example\x1b\\link\x1b]8;;\x1b\\b", "alinkb"}, // hyperlink gone
		{"a\x1b[2Jb", "ab"},                                                  // clear screen gone
		{"a\x1b[?1049hb", "ab"},                                              // private mode gone
		{"a\x1bcb", "ab"},                                                    // reset gone
		{"a\x9b2Jb", "a\ufffd2Jb"},                                           // a raw C1 byte is invalid UTF-8: inert
		{"a\u009b2Jb", "a2Jb"},                                               // C1 CSI as a rune: dropped
		{"secret\rdecoy\n", "secret\ndecoy\n"},                               // no overwrite
		{"crlf\r\n", "crlf\n"},
		{"bell\x07\x00", "bell"},
		{"tail\x1b[", "tail"},
	} {
		if got := ForPager(c.in); got != c.want {
			t.Errorf("ForPager(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
