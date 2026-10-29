package ui

import (
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/markdown"
)

// UseGlyphs picks the glyph set for the dashboard and the markdown it
// renders: the unicode set keeps the Nerd markdown glyphs that every
// font has and swaps the three that need the Nerd Font for its own.
// main and the screenshot tool both call it, so the gallery shows what
// users see.
func UseGlyphs(set glyphs.Set) {
	glyphs.Use(set)
	switch set {
	case glyphs.ASCII:
		markdown.Glyphs = markdown.ASCIIGlyphs
	case glyphs.Unicode:
		g := markdown.NerdGlyphs
		g.Shield, g.Window, g.Warn = glyphs.G("shield"), glyphs.G("window"), glyphs.G("warn")
		markdown.Glyphs = g
	default:
		markdown.Glyphs = markdown.NerdGlyphs
	}
}
