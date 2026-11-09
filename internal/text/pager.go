package text

import "strings"

// ForPager makes untrusted text (a PR's diff, a CI job's log) safe to
// hand to $PAGER, which may pass escapes straight to the terminal (less
// -R, more on Windows, cat). It keeps tabs, newlines and colour (SGR:
// ESC [ digits/semicolons m, which logs use and less -R renders) and
// drops every other escape sequence (OSC 52 clipboard writes, OSC 8
// links, title changes, cursor moves, DCS) and every other C0/C1
// control. A carriage return not before a newline becomes a newline, so
// a line can't be overwritten by what follows it.
func ForPager(s string) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\t' || r == '\n':
			b.WriteRune(r)
		case r == '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				continue
			}
			b.WriteByte('\n')
		case r == 0x1b:
			i = skipEscape(rs, i, &b)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// a control, C1 included (0x9b is a one-byte CSI): dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// skipEscape consumes the escape sequence starting at rs[i] (ESC),
// writing it to b only when it is SGR, and returns the index of its last
// rune.
func skipEscape(rs []rune, i int, b *strings.Builder) int {
	if i+1 >= len(rs) {
		return i
	}
	switch rs[i+1] {
	case '[': // CSI: parameters, intermediates, one final byte
		j := i + 2
		for j < len(rs) && rs[j] >= 0x30 && rs[j] <= 0x3f {
			j++
		}
		params := string(rs[i+2 : j])
		for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
			j++
		}
		if j >= len(rs) {
			return len(rs) - 1
		}
		if rs[j] == 'm' && strings.Trim(params, "0123456789;") == "" {
			b.WriteString("\x1b[" + params + "m")
		}
		return j
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: up to BEL or ST
		for j := i + 2; j < len(rs); j++ {
			if rs[j] == 0x07 {
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
		return len(rs) - 1
	default: // a two-byte escape (ESC c resets the terminal, ESC 7/8...)
		return i + 1
	}
}
