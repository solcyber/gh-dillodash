package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/demo"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/ui"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

var imgRE = regexp.MustCompile(`!\[([^\]]*)\]\(((?:\.\./)*(?:docs/)?shots/([a-z0-9-]+)\.png)\)`)

// docFiles are the markdown files that may embed the gallery.
func docFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	files := []string{filepath.Join(root, "README.md")}
	for _, glob := range []string{"docs/*.md", "docs/workflows/*.md"} {
		m, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	return files
}

// TestGalleryMatchesTheDocs: every picture a doc embeds is a shot of the
// manifest with a committed PNG and non-empty alt text, and every shot is
// embedded somewhere (no orphans); no two shots share a name.
func TestGalleryMatchesTheDocs(t *testing.T) {
	names := map[string]bool{}
	for _, s := range shots {
		if names[s.Name] {
			t.Errorf("shot %q twice", s.Name)
		}
		names[s.Name] = true
		if s.Caption == "" {
			t.Errorf("shot %q has no caption", s.Name)
		}
		if _, err := os.Stat(filepath.Join("..", "..", "docs", "shots", s.Name+".png")); err != nil {
			t.Errorf("shot %q has no PNG: run go run ./cmd/screenshot", s.Name)
		}
	}
	used := map[string]bool{}
	for _, f := range docFiles(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range imgRE.FindAllStringSubmatch(string(src), -1) {
			if !names[m[3]] {
				t.Errorf("%s embeds %s, which is not a shot", filepath.Base(f), m[2])
			}
			if strings.TrimSpace(m[1]) == "" {
				t.Errorf("%s: %s has no alt text", filepath.Base(f), m[2])
			}
			used[m[3]] = true
		}
	}
	for n := range names {
		if !used[n] {
			t.Errorf("shot %q is embedded by no doc", n)
		}
	}
}

// TestEveryShotSettles: every script runs to a frame on the fixture (a
// key-table change that breaks one fails here, not at render time).
func TestEveryShotSettles(t *testing.T) {
	glyphs.Use(glyphs.Nerd)
	theme.Setup(colorprofile.Ascii, false)
	fx.Motion = "off"
	cfg := demo.Config()
	for _, s := range shots {
		wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
		if err != nil {
			t.Fatal(err)
		}
		cols, rows := s.size()
		frames, err := ui.SnapshotsWith(context.Background(), ui.Options{
			Config: cfg, GH: demo.Client(), Workflows: wf, Me: demo.Me, Host: "github.com", Version: "test",
		}, s.Opts, cols, rows, [][]string{s.Steps})
		if err != nil || len(frames) != 1 {
			t.Errorf("%s: %v (%d frames)", s.Name, err, len(frames))
		}
	}
}
