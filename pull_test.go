package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
)

func TestPullConfig(t *testing.T) {
	gh := fake.New("alice")
	gh.FileData = map[string][]byte{
		"acme/dash-config:config.toml":      []byte("[reviews]\nrepos = [\"acme/infra\"]\n"),
		"acme/dash-config:bad.toml":         []byte("[reviews]\nrepos = []\n"),
		"acme/dash-config:team/config.toml": []byte("[reviews]\nrepos = [\"acme/web-portal\"]\n"),
	}
	path := filepath.Join(t.TempDir(), "cfg", "config.toml")
	var out bytes.Buffer
	ctx := context.Background()

	// a bare pull with nothing installed needs a spec
	if err := pullConfig(ctx, gh, "", path, false, &out); err == nil || !strings.Contains(err.Error(), "say where") {
		t.Fatalf("bare pull without a source: %v", err)
	}
	// first install
	if err := pullConfig(ctx, gh, "acme/dash-config", path, false, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src, ok := config.ParseSource(data)
	if !ok || src.Spec() != "acme/dash-config:config.toml" || src.SHA == "" {
		t.Fatalf("source header: %q", data)
	}
	if st, _ := os.Stat(path); st != nil && st.Mode().Perm() != 0o600 && os.Getenv("OS") != "Windows_NT" {
		t.Errorf("mode %o", st.Mode().Perm())
	}
	if !strings.Contains(out.String(), "installed") {
		t.Errorf("output: %s", out.String())
	}
	// the installed file still parses and the header is not a warning
	if c, err := config.Load(path, config.Overrides{}); err != nil || len(c.Warnings) != 0 || c.Reviews.Repos[0].Name != "infra" {
		t.Errorf("installed config: %v %+v", err, c)
	}
	// a bare pull refreshes from the recorded source and sees it current
	out.Reset()
	if err := pullConfig(ctx, gh, "", path, false, &out); err != nil || !strings.Contains(out.String(), "is current") {
		t.Errorf("refresh: %v %s", err, out.String())
	}
	// the repo moves: the bare pull installs the new content
	gh.FileData["acme/dash-config:config.toml"] = []byte("[reviews]\nrepos = [\"acme/infra\", \"acme/etl-jobs\"]\n")
	out.Reset()
	if err := pullConfig(ctx, gh, "", path, false, &out); err != nil || !strings.Contains(out.String(), "installed") {
		t.Errorf("refresh after change: %v %s", err, out.String())
	}
	if c, _ := config.Load(path, config.Overrides{}); len(c.Reviews.Repos) != 2 {
		t.Errorf("new content not installed: %+v", c.Reviews.Repos)
	}
	// an invalid remote config is refused and the installed one survives
	before, _ := os.ReadFile(path)
	if err := pullConfig(ctx, gh, "acme/dash-config:bad.toml", path, false, &out); err == nil || !strings.Contains(err.Error(), "does not validate") {
		t.Errorf("bad config: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a refused pull must not touch the installed file")
	}
	// a path inside the repo, and a missing file
	if err := pullConfig(ctx, gh, "acme/dash-config:team/config.toml", path, false, &out); err != nil {
		t.Errorf("subpath: %v", err)
	}
	if err := pullConfig(ctx, gh, "acme/dash-config:nope.toml", path, false, &out); err == nil || !strings.Contains(err.Error(), "could not read") {
		t.Errorf("missing file: %v", err)
	}
	// a bad spec is a usage error
	if err := pullConfig(ctx, gh, "acme", path, false, &out); err == nil || exitCode(err) != 2 {
		t.Errorf("bad spec: %v", err)
	}
}
