package markdown

import (
	"strings"
	"testing"
)

// TestNestedFence: a ```go inside a ````md block is content; the block
// closes on a fence at least as long as its opener.
func TestNestedFence(t *testing.T) {
	body := "````md\nexample:\n```go\nx := 1\n```\n````\nafter **bold**\n"
	lines := Render(body, 80, false, nil)
	var all []string
	for _, l := range lines {
		s := ""
		for _, sg := range l.Segs {
			s += sg.Text
		}
		all = append(all, s)
	}
	joined := strings.Join(all, "\n")
	if !strings.Contains(joined, "```go") || !strings.Contains(joined, "x := 1") {
		t.Errorf("the inner fence is content:\n%s", joined)
	}
	if strings.Contains(joined, "**bold**") {
		t.Errorf("the text after the block renders as markdown:\n%s", joined)
	}
}
