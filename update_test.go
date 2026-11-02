package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/selfupdate"
	"github.com/solcyber/gh-dillodash/internal/ui"
)

type fakeRelease struct {
	rel  *ghapi.Release
	body []byte
}

func (f *fakeRelease) LatestRelease(context.Context, model.Repo, string) (*ghapi.Release, string, error) {
	return f.rel, `"e"`, nil
}

func (f *fakeRelease) DownloadAsset(_ context.Context, _ model.Repo, _ int64, w io.Writer, _ int64) error {
	_, err := w.Write(f.body)
	return err
}

// testUpdater is a v1.5.2 gh extension in a temp data dir with v1.5.4
// out; nothing on the real machine is read or run.
func testUpdater(t *testing.T, body, served []byte) (*selfupdate.Updater, string) {
	t.Helper()
	dir := t.TempDir()
	ext := filepath.Join(dir, "data", "extensions", "gh-dillodash", "gh-dillodash")
	if err := os.MkdirAll(filepath.Dir(ext), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(ext, []byte("old"), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(ext), "manifest.yml"), []byte("tag: v1.5.2\n"), 0o600)
	sum := sha256.Sum256(body)
	src := &fakeRelease{body: served, rel: &ghapi.Release{Tag: "v1.5.4", Body: "### Fixed\n\n- a thing\n",
		Assets: []ghapi.ReleaseAsset{{ID: 3, Name: selfupdate.AssetName("linux", "amd64"), Digest: "sha256:" + hex.EncodeToString(sum[:])}}}}
	find := selfupdate.FindOptions{Exe: ext, Running: "v1.5.2", DataDir: filepath.Join(dir, "data"), GOOS: "linux"}
	return &selfupdate.Updater{
		Checker:   &selfupdate.Checker{Src: src, Path: filepath.Join(dir, "cache.json"), Find: find},
		Installer: &selfupdate.Installer{DL: src, GOOS: "linux", GOARCH: "amd64", Find: find, Run: func(context.Context, []string) ([]byte, error) { return nil, nil }},
	}, ext
}

func TestRunUpdate(t *testing.T) {
	u, ext := testUpdater(t, []byte("new"), []byte("new"))
	var out bytes.Buffer
	if err := runUpdate(context.Background(), u, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s := out.String()
	for _, want := range []string{"dillodash v1.5.2; the latest release is v1.5.4", "installed v1.5.4 over " + ext, "what changed in v1.5.4:", "  - a thing", "restart dillodash"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if b, _ := os.ReadFile(ext); string(b) != "new" {
		t.Errorf("not installed: %q", b)
	}
	// rollback puts the .prev back
	res := u.Installer.Rollback()
	if b, _ := os.ReadFile(ext); res.Err != nil || string(b) != "old" {
		t.Errorf("rollback %+v %q", res, b)
	}
}

func TestRunUpdateRefusesATamperedAsset(t *testing.T) {
	u, ext := testUpdater(t, []byte("new"), []byte("evil"))
	var out bytes.Buffer
	err := runUpdate(context.Background(), u, &out)
	if err == nil || !strings.Contains(err.Error(), "v1.5.4 NOT installed: checksum mismatch") || !strings.Contains(err.Error(), "by hand: gh extension upgrade dillodash") {
		t.Errorf("err %v", err)
	}
	if b, _ := os.ReadFile(ext); string(b) != "old" {
		t.Errorf("a refusal changed the binary: %q", b)
	}
}

func TestAutoUpdateEnv(t *testing.T) {
	for v, want := range map[string]bool{"0": false, "off": false, "": true, "1": true, "junk": true} {
		cfg := config.Default()
		autoUpdateEnv(cfg, func(string) string { return v })
		if cfg.Update.Auto != want {
			t.Errorf("DILLODASH_AUTO_UPDATE=%q: auto %v", v, cfg.Update.Auto)
		}
	}
}

func TestRestartExecsTheStartBinaryWithThePlace(t *testing.T) {
	wasExe, wasExec, wasArgs := startExe, execSelf, os.Args
	t.Cleanup(func() { startExe, execSelf, os.Args = wasExe, wasExec, wasArgs })
	startExe = "/home/alice/.local/share/gh/extensions/gh-dillodash/gh-dillodash"
	os.Args = []string{"gh-dillodash", "--motion", "off"}
	t.Setenv(ui.RestoreEnv, "stale")
	var gotPath string
	var gotArgv, gotEnv []string
	execSelf = func(path string, argv, env []string) error { gotPath, gotArgv, gotEnv = path, argv, env; return nil }
	r := ui.Restore{Detail: &ui.RestoreRef{Repo: "acme/infra", Number: 7, Kind: "pr"}}
	if err := restart(r); err != nil {
		t.Fatal(err)
	}
	if gotPath != startExe || strings.Join(gotArgv, " ") != startExe+" --motion off" {
		t.Errorf("exec %q %q", gotPath, gotArgv)
	}
	n := 0
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, ui.RestoreEnv+"=") {
			n++
			if got := ui.DecodeRestore(strings.TrimPrefix(kv, ui.RestoreEnv+"=")); got == nil || got.Detail.Number != 7 {
				t.Errorf("restore %q", kv)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d restore entries in the env", n)
	}
}

// TestRunUpdateNothingReplaced: an update with no install it may replace
// (a pinned extension here) says so and fails, never a silent success.
func TestRunUpdateNothingReplaced(t *testing.T) {
	u, ext := testUpdater(t, []byte("new"), []byte("new"))
	os.WriteFile(filepath.Join(filepath.Dir(ext), "manifest.yml"), []byte("tag: v1.5.2\nispinned: true\n"), 0o600)
	var out bytes.Buffer
	err := runUpdate(context.Background(), u, &out)
	if err == nil || !strings.Contains(err.Error(), "v1.5.4 NOT installed: no install here can be replaced") {
		t.Errorf("err %v\n%s", err, out.String())
	}
	if b, _ := os.ReadFile(ext); string(b) != "old" {
		t.Errorf("a pinned install changed: %q", b)
	}
}

// TestFirstLinesStopsAtTheFirstSection: the excerpt is the notes'
// opening paragraph, never half a list.
func TestFirstLinesStopsAtTheFirstSection(t *testing.T) {
	notes := "A release night in dillodash alone,\nand installs that keep themselves current.\n\n### Fixed\n\n- one\n  wrapped\n- two\n"
	if got := firstLines(notes, 8); got != "  A release night in dillodash alone,\n  and installs that keep themselves current." {
		t.Errorf("got %q", got)
	}
	if got := firstLines("### Fixed\n\n- one\n- two\n\n### Added\n- three\n", 8); got != "  - one\n  - two" {
		t.Errorf("headed notes: %q", got)
	}
	if got := firstLines("a\nb\nc\n", 2); got != "  a\n  b …" {
		t.Errorf("capped: %q", got)
	}
}
