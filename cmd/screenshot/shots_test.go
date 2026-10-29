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

var imgRE = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]*shots/([a-z0-9-]+)\.png)\)`)

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
// manifest, by a path that resolves from that doc to the committed PNG,
// with the shot's caption as its alt text; every shot has a caption and
// is embedded somewhere (no orphans); no two shots share a name.
func TestGalleryMatchesTheDocs(t *testing.T) {
	names := map[string]bool{}
	for _, s := range shots {
		if names[s.Name] {
			t.Errorf("shot %q twice", s.Name)
		}
		names[s.Name] = true
		if ui.ShotCaptions[s.Name] == "" {
			t.Errorf("shot %q has no caption in ui.ShotCaptions", s.Name)
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
			if _, err := os.Stat(filepath.Join(filepath.Dir(f), filepath.FromSlash(m[2]))); err != nil {
				t.Errorf("%s: %s does not resolve from the doc", filepath.Base(f), m[2])
			}
			if want := ui.ShotCaptions[m[3]]; m[1] != want {
				t.Errorf("%s: %s alt text\n got %q\nwant %q (ui.ShotCaptions)", filepath.Base(f), m[2], m[1], want)
			}
			used[m[3]] = true
		}
	}
	for n := range names {
		if !used[n] {
			t.Errorf("shot %q is embedded by no doc", n)
		}
	}
	for n := range ui.ShotCaptions {
		if !names[n] {
			t.Errorf("caption %q names no shot", n)
		}
	}
}

// TestHeroOpensTheRelease: the README's second frame is the release's
// detail, not whatever row the fixture now puts there.
func TestHeroOpensTheRelease(t *testing.T) {
	glyphs.Use(glyphs.Nerd)
	theme.Setup(colorprofile.Ascii, false)
	fx.Motion = "off"
	frames, err := render(shot{Name: "hero"}, heroScripts)
	if err != nil || len(frames) != 2 {
		t.Fatalf("%v (%d frames)", err, len(frames))
	}
	if top := frames[1].Text(0); !strings.Contains(top, heroExpect) {
		t.Errorf("the hero's detail header is %q, want %s", top, heroExpect)
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
			continue
		}
		screen := strings.Join(frames[0].Lines(), "\n")
		for _, want := range s.Expect {
			if !strings.Contains(screen, want) {
				t.Errorf("%s: the frame lacks %q: the script landed elsewhere\n%s", s.Name, want, screen)
			}
		}
	}
}
