package widgets

import (
	"strings"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

// Confirm is a y/N question on the bottom rows. Only a y or Y answers
// yes; any other key (n, Enter, Esc, anything) answers no. With motion,
// an /approve line in the question breathes between blues.
type Confirm struct {
	Question string
	done     bool
	yes      bool
}

// NewConfirm makes a confirm for question (" [y/N]" is appended as drawn).
func NewConfirm(question string) *Confirm { return &Confirm{Question: question} }

// Result is the answer: true only for y/Y.
func (c *Confirm) Result() bool { return c.yes }

// Done says a key answered the question.
func (c *Confirm) Done() bool { return c.done }

// Handle answers the question with any key.
func (c *Confirm) Handle(k Key) bool {
	if c.done {
		return true
	}
	c.done = true
	c.yes = k.Name == "" && (k.Rune == 'y' || k.Rune == 'Y')
	return true
}

// Animated says an /approve line breathes (motion on, a color theme).
func (c *Confirm) Animated() bool {
	if fx.Motion != "full" || theme.Current() == theme.Mono {
		return false
	}
	for _, para := range strings.Split(c.Question, "\n") {
		if ApproveCmdRe.MatchString(strings.TrimSpace(para)) {
			return true
		}
	}
	return false
}

// Draw paints the question with its [y/N] on the bottom rows.
func (c *Confirm) Draw(cv *canvas.Canvas, f *fx.Fx) {
	cmd := "approve.cmd"
	if c.Animated() {
		cmd = []string{"approve.cmd", "approve.cmd.hi", "approve.cmd.hi2", "approve.cmd.hi"}[int(fx.Wave(1.6, 0)*4)%4]
	}
	DrawBlock(cv, c.Question+" [y/N]", cv.H-1, "prompt", cmd)
}
