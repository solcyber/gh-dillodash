package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
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
	if err := pullConfig(ctx, gh, "acme/dash-config:bad.toml", path, true, &out); err == nil || !strings.Contains(err.Error(), "does not validate") {
		t.Errorf("bad config: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a refused pull must not touch the installed file")
	}
	// switching to another spec needs --force, and keeps the old file
	if err := pullConfig(ctx, gh, "acme/dash-config:team/config.toml", path, false, &out); err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "pass --force to switch") {
		t.Errorf("switch without --force: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a refused switch must not touch the installed file")
	}
	if err := pullConfig(ctx, gh, "acme/dash-config:team/config.toml", path, true, &out); err != nil {
		t.Errorf("subpath with --force: %v", err)
	}
	if bak, err := os.ReadFile(path + ".bak"); err != nil || !bytes.Equal(bak, before) {
		t.Errorf("the switch kept the previous file as .bak: %v", err)
	}
	if err := pullConfig(ctx, gh, "acme/dash-config:nope.toml", path, true, &out); err == nil || !strings.Contains(err.Error(), "could not read") {
		t.Errorf("missing file: %v", err)
	}
	// a bad spec is a usage error
	if err := pullConfig(ctx, gh, "acme", path, false, &out); err == nil || exitCode(err) != 2 {
		t.Errorf("bad spec: %v", err)
	}
}

// TestPullRefusesUnstampedFile: a config someone wrote by hand is never
// replaced silently; --force replaces it and keeps a .bak.
func TestPullRefusesUnstampedFile(t *testing.T) {
	gh := fake.New("alice")
	gh.FileData = map[string][]byte{"acme/dash-config:config.toml": []byte("[reviews]\nrepos = [\"acme/infra\"]\n")}
	path := filepath.Join(t.TempDir(), "config.toml")
	hand := []byte("# mine\n[reviews]\nrepos = [\"acme/web-portal\"]\n")
	if err := os.WriteFile(path, hand, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := pullConfig(context.Background(), gh, "acme/dash-config", path, false, &out)
	if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "was not pulled from a repo; pass --force") {
		t.Fatalf("refusal: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, hand) {
		t.Fatal("the hand-written file was touched")
	}
	if err := pullConfig(context.Background(), gh, "acme/dash-config", path, true, &out); err != nil {
		t.Fatal(err)
	}
	if bak, err := os.ReadFile(path + ".bak"); err != nil || !bytes.Equal(bak, hand) {
		t.Errorf(".bak holds the hand-written file: %v", err)
	}
	if c, err := config.Load(path, config.Overrides{}); err != nil || c.Reviews.Repos[0].Name != "infra" {
		t.Errorf("installed: %v %+v", err, c)
	}
	// no leftover temp files from the atomic writes
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

// TestPullHonoursDillodashConfig: a bare config pull writes where the
// dashboard will read, $DILLODASH_CONFIG included.
func TestPullHonoursDillodashConfig(t *testing.T) {
	gh := fake.New("alice")
	gh.FileData = map[string][]byte{"acme/dash-config:config.toml": []byte("[reviews]\nrepos = [\"acme/infra\"]\n")}
	was := newClient
	newClient = func(ghapi.Options) (client, error) { return gh, nil }
	t.Cleanup(func() { newClient = was })
	path := filepath.Join(t.TempDir(), "elsewhere", "dash.toml")
	t.Setenv("DILLODASH_CONFIG", path)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	out := capture(t, func() {
		if err := run([]string{"config", "pull", "acme/dash-config"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(path); err != nil || !strings.Contains(out, "installed "+path) {
		t.Errorf("pull wrote elsewhere: %v %q", err, out)
	}
}
