package glyphs

import "strings"

// Armadillo is the mascot, facing right.
var Armadillo = []string{
	`            __.------.__`,
	`        .-'' |  |  |  | ''-.__`,
	"       /  |  |  |  |  |  |  ' o`.",
	`      (___|__|__|__|__|__|___.--'`,
	`         \\  \\         //  //`,
}

// ArmadilloStep is the other pair of legs forward: two frames make a walk.
var ArmadilloStep = append(append([]string{}, Armadillo[:len(Armadillo)-1]...),
	`         //  //         \\  \\`)

var mirrorRunes = map[rune]rune{
	'(': ')', ')': '(', '/': '\\', '\\': '/', '`': '\'', '\'': '`',
}

// Mirror is ASCII art facing the other way: every row padded to the
// widest, reversed, with the slanted characters swapped.
func Mirror(art []string) []string {
	w := 0
	for _, ln := range art {
		if n := len([]rune(ln)); n > w {
			w = n
		}
	}
	out := make([]string, len(art))
	for i, ln := range art {
		r := []rune(ln + strings.Repeat(" ", w-len([]rune(ln))))
		for a, b := 0, len(r)-1; a < b; a, b = a+1, b-1 {
			r[a], r[b] = r[b], r[a]
		}
		for j, c := range r {
			if m, ok := mirrorRunes[c]; ok {
				r[j] = m
			}
		}
		out[i] = string(r)
	}
	return out
}

// Logotype is the startup splash's wordmark: full blocks lit by a vertical
// gradient, box-drawing shadows in steel blue (the "px" glyph lists every
// character it uses, so the font test covers it). ASCII terminals get
// LogotypeASCII.
var Logotype = []string{
	`██████╗ ██╗ ██╗     ██╗      ██████╗ ██████╗  █████╗ ███████╗██╗  ██╗`,
	`██╔══██╗██║ ██║     ██║     ██╔═══██╗██╔══██╗██╔══██╗██╔════╝██║  ██║`,
	`██║  ██║██║ ██║     ██║     ██║   ██║██║  ██║███████║███████╗███████║`,
	`██║  ██║██║ ██║     ██║     ██║   ██║██║  ██║██╔══██║╚════██║██╔══██║`,
	`██████╔╝██║ ███████╗███████╗╚██████╔╝██████╔╝██║  ██║███████║██║  ██║`,
	`╚═════╝ ╚═╝ ╚══════╝╚══════╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝`,
}

// LogotypeASCII is the small wordmark for terminals without box drawing.
var LogotypeASCII = []string{
	` ___   ___  _     _      ___   ___     _    ___  _  _ `,
	`|   \ |_ _|| |   | |    / _ \ |   \   /_\  / __|| || |`,
	`| |) | | | | |__ | |__ | (_) || |) | / _ \ \__ \| __ |`,
	`|___/ |___||____||____| \___/ |___/ /_/ \_\|___/|_||_|`,
}
