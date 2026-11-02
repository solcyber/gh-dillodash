package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.5.2", "v1.5.4", -1},
		{"v1.5.4", "v1.5.4", 0},
		{"v1.10.0", "v1.9.9", 1},
		{"1.6.0", "v1.5.9", 1},
		{"dev+abc1234", "v1.5.4", 0},
		{"v1.5.4 demo", "v1.5.4", 0},
		{"test", "v9.9.9", 0},
		{"v1.6.0-rc1", "v1.5.0", 0},
		{"v01.2.3", "v1.2.3", 0},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	for _, v := range []string{"dev", "dev+abc1234-dirty", "test", "v1.5.4 demo", ""} {
		if Newer("v9.9.9", v) {
			t.Errorf("Newer(v9.9.9, %q): a non-release build never counts as stale", v)
		}
	}
}

type fakeSrc struct {
	rel   *ghapi.Release
	etag  string
	err   error
	calls []string // the etag each call sent
}

func (f *fakeSrc) LatestRelease(_ context.Context, repo model.Repo, etag string) (*ghapi.Release, string, error) {
	f.calls = append(f.calls, etag)
	if repo != Repo {
		return nil, "", errors.New("wrong repo " + repo.String())
	}
	if f.err != nil {
		return nil, "", f.err
	}
	if etag != "" && etag == f.etag {
		return nil, etag, ghapi.ErrNotModified
	}
	return f.rel, f.etag, nil
}

func TestCheckerConditionalAndOffline(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
	src := &fakeSrc{rel: &ghapi.Release{Tag: "v1.5.4", Body: "### Fixed"}, etag: `"e1"`}
	c := &Checker{Src: src, Path: filepath.Join(dir, "latest-release.json"), Now: func() time.Time { return at },
		Find: FindOptions{Running: "v1.5.2"}}
	st := c.Check(context.Background(), false)
	if !st.Stale() || st.Latest != "v1.5.4" || st.Err != "" {
		t.Fatalf("first check: %+v", st)
	}
	if !strings.Contains(st.Banner(), "dillodash v1.5.2 — v1.5.4 is out: gh extension upgrade dillodash") {
		t.Errorf("banner %q", st.Banner())
	}
	// within MinInterval: the cache answers, no request
	c.Check(context.Background(), false)
	if len(src.calls) != 1 {
		t.Errorf("calls within the interval: %q", src.calls)
	}
	// later: conditional, 304 keeps the answer
	at = at.Add(30 * time.Minute)
	st = c.Check(context.Background(), false)
	if len(src.calls) != 2 || src.calls[1] != `"e1"` {
		t.Errorf("conditional request: %q", src.calls)
	}
	if st.Latest != "v1.5.4" || !st.CheckedAt.Equal(at) {
		t.Errorf("after 304: %+v", st)
	}
	// offline: quiet, keeps the last answer, records the failure
	at = at.Add(time.Hour)
	src.err = errors.New("network error: no route to host")
	st = c.Check(context.Background(), true)
	if st.Latest != "v1.5.4" || !strings.Contains(st.Err, "no route") || !st.Stale() {
		t.Errorf("offline: %+v", st)
	}
}

func TestCheckerOfflineFirstStartIsQuiet(t *testing.T) {
	c := &Checker{Src: &fakeSrc{err: errors.New("timed out")}, Path: filepath.Join(t.TempDir(), "c.json"),
		Find: FindOptions{Running: "v1.5.2"}}
	st := c.Check(context.Background(), false)
	if st.Stale() || st.Banner() != "" || st.Err == "" {
		t.Errorf("offline first start: %+v", st)
	}
}

func TestCheckerIgnoresPrerelease(t *testing.T) {
	c := &Checker{Src: &fakeSrc{rel: &ghapi.Release{Tag: "v1.6.0-rc1", Prerelease: true}}, Find: FindOptions{Running: "v1.5.4"}}
	if st := c.Check(context.Background(), true); st.Latest != "" || st.Stale() {
		t.Errorf("prerelease: %+v", st)
	}
}

func TestGate(t *testing.T) {
	ext := Install{Kind: KindExtension}
	st := Status{Running: "v1.5.2", Latest: "v1.5.4", Self: ext}
	if g := st.Gate("approve"); g != "release NOW on an approved release needs ≥ v1.5.3 — you run v1.5.2: gh extension upgrade dillodash" {
		t.Errorf("approve gate %q", g)
	}
	st = Status{Running: "v1.5.4", Latest: "v1.6.0", Self: ext}
	if g := st.Gate("approve"); !strings.HasPrefix(g, "you run dillodash v1.5.4 and v1.6.0 is out") {
		t.Errorf("stale gate %q", g)
	}
	st = Status{Running: "v1.6.0", Latest: "v1.6.0", Self: ext}
	if g := st.Gate("approve"); g != "" {
		t.Errorf("current build gated: %q", g)
	}
	// no check answered yet: a known gap still gates
	st = Status{Running: "v1.5.2", Self: ext}
	if g := st.Gate("window"); !strings.Contains(g, "needs ≥ v1.5.4") {
		t.Errorf("unchecked gate %q", g)
	}
	bin := Status{Running: "v1.5.2", Latest: "v1.5.4", Self: Install{Kind: KindBinary, Path: "/opt/bin/dillodash"}}
	if !strings.Contains(bin.Banner(), "replace /opt/bin/dillodash with the v1.5.4 binary from https://github.com/solcyber/gh-dillodash/releases") {
		t.Errorf("binary banner %q", bin.Banner())
	}
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	extDir := filepath.Join(data, "extensions", "gh-dillodash")
	write(t, filepath.Join(extDir, "gh-dillodash"), "\x7fELF...", 0o755)
	write(t, filepath.Join(extDir, "manifest.yml"), "owner: acme\nname: gh-dillodash\ntag: v1.5.2\nispinned: false\n", 0o600)
	bin1 := filepath.Join(root, "bin1")
	write(t, filepath.Join(bin1, "dillodash"), "#!/bin/sh\nexport GH_TOKEN=x\nexec gh dillodash \"$@\"\n", 0o755)
	bin2 := filepath.Join(root, "bin2")
	write(t, filepath.Join(bin2, "dillodash"), "\x7fELF...", 0o755)
	// the extension's binary linked onto PATH: listed once
	bin3 := filepath.Join(root, "bin3")
	if err := os.MkdirAll(bin3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(extDir, "gh-dillodash"), filepath.Join(bin3, "gh-dillodash")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	var asked []string
	o := FindOptions{
		Exe: filepath.Join(extDir, "gh-dillodash"), Running: "v1.5.2", DataDir: data, GOOS: "linux",
		Path: strings.Join([]string{bin1, bin2, bin3, filepath.Join(root, "missing")}, string(os.PathListSeparator)),
		Version: func(p string) (string, error) {
			asked = append(asked, p)
			return "dillodash v1.4.1\n", nil
		},
	}
	got := Find(o)
	if len(got) != 3 {
		t.Fatalf("installs: %+v", got)
	}
	if g := got[0]; g.Kind != KindExtension || g.Version != "v1.5.2" || !g.Running {
		t.Errorf("extension: %+v", g)
	}
	if g := got[1]; g.Kind != KindLauncher || g.Runs != "gh dillodash" || g.Version != "" {
		t.Errorf("launcher: %+v", g)
	}
	if g := got[2]; g.Kind != KindBinary || g.Version != "v1.4.1" || g.Running {
		t.Errorf("binary: %+v", g)
	}
	if len(asked) != 1 || asked[0] != filepath.Join(bin2, "dillodash") {
		t.Errorf("version asked of %q: a launcher and the running binary are never run", asked)
	}
	if s := Self(o); s.Kind != KindExtension {
		t.Errorf("self: %+v", s)
	}
}
