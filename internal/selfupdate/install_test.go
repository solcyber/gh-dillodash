package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

type fakeDL struct {
	body []byte
	ids  []int64
}

func (f *fakeDL) DownloadAsset(_ context.Context, repo model.Repo, id int64, w io.Writer, _ int64) error {
	if repo != Repo {
		return errors.New("wrong repo")
	}
	f.ids = append(f.ids, id)
	_, err := w.Write(f.body)
	return err
}

// machine lays out a v1.5.2 gh extension (running), a v1.5.2 binary on
// PATH and a launcher script.
type machine struct {
	ext, bin, launcher string
	opts               FindOptions
	ran                [][]string
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	extDir := filepath.Join(data, "extensions", "gh-dillodash")
	m := &machine{
		ext:      filepath.Join(extDir, "gh-dillodash"),
		bin:      filepath.Join(root, "bin", "dillodash"),
		launcher: filepath.Join(root, "lbin", "dillodash"),
	}
	write(t, m.ext, "old extension binary", 0o755)
	write(t, filepath.Join(extDir, "manifest.yml"), "owner: acme\nname: gh-dillodash\ntag: v1.5.2\nispinned: false\n", 0o600)
	write(t, m.bin, "old standalone binary", 0o755)
	write(t, m.launcher, "#!/bin/sh\nexec gh dillodash \"$@\"\n", 0o755)
	m.opts = FindOptions{
		Exe: m.ext, Running: "v1.5.2", DataDir: data, GOOS: "linux",
		Path:    filepath.Dir(m.launcher) + string(os.PathListSeparator) + filepath.Dir(m.bin),
		Version: func(string) (string, error) { return "dillodash v1.5.2\n", nil },
	}
	return m
}

func (m *machine) installer(dl Downloader, attestErr error) *Installer {
	return &Installer{DL: dl, GOOS: "linux", GOARCH: "amd64", Find: m.opts,
		Run: func(_ context.Context, argv []string) ([]byte, error) {
			m.ran = append(m.ran, argv)
			if attestErr != nil {
				return []byte("Error: failed to fetch attestations: HTTP 404\n"), attestErr
			}
			return nil, nil
		}}
}

func release(tag string, body []byte) *ghapi.Release {
	sum := sha256.Sum256(body)
	return &ghapi.Release{Tag: tag, Assets: []ghapi.ReleaseAsset{
		{ID: 1, Name: "darwin-arm64", Digest: "sha256:" + strings.Repeat("0", 64)},
		{ID: 2, Name: "linux-amd64", Digest: "sha256:" + hex.EncodeToString(sum[:])},
	}}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noTemps(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		es, _ := os.ReadDir(d)
		for _, e := range es {
			if strings.HasPrefix(e.Name(), ".dillodash-") {
				t.Errorf("temp file left behind: %s", filepath.Join(d, e.Name()))
			}
		}
	}
}

func TestInstallUpdatesEveryInstall(t *testing.T) {
	m := newMachine(t)
	body := []byte("new binary v1.5.4")
	dl := &fakeDL{body: body}
	res := m.installer(dl, nil).Install(context.Background(), release("v1.5.4", body), "v1.5.2")
	if res.Err != nil {
		t.Fatalf("install: %v (%q)", res.Err, res.Notes)
	}
	if len(res.Updated) != 2 || res.Updated[0] != m.ext || res.Updated[1] != m.bin {
		t.Errorf("updated %q", res.Updated)
	}
	if len(dl.ids) != 1 || dl.ids[0] != 2 {
		t.Errorf("downloads: %v (once, this platform's asset)", dl.ids)
	}
	for _, p := range []string{m.ext, m.bin} {
		if read(t, p) != string(body) {
			t.Errorf("%s not replaced", p)
		}
	}
	if read(t, m.ext+".prev") != "old extension binary" || read(t, m.bin+".prev") != "old standalone binary" {
		t.Error("the old binaries are kept as .prev")
	}
	manifest := read(t, filepath.Join(filepath.Dir(m.ext), "manifest.yml"))
	if !strings.Contains(manifest, "tag: v1.5.4") || !strings.Contains(manifest, "owner: acme") {
		t.Errorf("manifest %q", manifest)
	}
	if !strings.Contains(read(t, m.launcher), "exec gh dillodash") {
		t.Error("the launcher was touched")
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "a launcher (runs gh dillodash) — never replaced") {
		t.Errorf("notes %q", res.Notes)
	}
	if len(m.ran) != 1 || strings.Join(m.ran[0][:3], " ") != "gh attestation verify" || m.ran[0][4] != "--repo" || m.ran[0][5] != "solcyber/gh-dillodash" {
		t.Errorf("attestation: %q", m.ran)
	}
	noTemps(t, filepath.Dir(m.ext), filepath.Dir(m.bin))

	// rollback swaps back; a second rollback undoes it
	in := m.installer(dl, nil)
	in.Find.Running = "v1.5.4"
	if r := in.Rollback(); r.Err != nil || len(r.Updated) != 2 {
		t.Fatalf("rollback: %+v", r)
	}
	if read(t, m.ext) != "old extension binary" || read(t, m.bin) != "old standalone binary" || read(t, m.ext+".prev") != string(body) {
		t.Error("rollback did not restore the .prev")
	}
	if !strings.Contains(read(t, filepath.Join(filepath.Dir(m.ext), "manifest.yml")), "tag: v1.5.2") {
		t.Error("rollback did not restore the manifest's tag")
	}
	in.Rollback()
	if read(t, m.ext) != string(body) {
		t.Error("a second rollback undoes the first")
	}
}

func TestInstallRefusesAndTouchesNothing(t *testing.T) {
	body := []byte("new binary v1.5.4")
	cases := []struct {
		name    string
		rel     *ghapi.Release
		running string
		dl      []byte
		attest  error
		want    string
	}{
		{"tampered asset", release("v1.5.4", body), "v1.5.2", []byte("tampered bytes"), nil, "checksum mismatch"},
		{"no attestation", release("v1.5.4", body), "v1.5.2", body, errors.New("exit status 1"), "build attestation not verified (Error: failed to fetch attestations: HTTP 404)"},
		{"downgrade", release("v1.5.1", body), "v1.5.2", body, nil, "is not newer than v1.5.2"},
		{"same", release("v1.5.2", body), "v1.5.2", body, nil, "is not newer"},
		{"prerelease", func() *ghapi.Release { r := release("v1.5.4", body); r.Prerelease = true; return r }(), "v1.5.2", body, nil, "not a published stable release"},
		{"draft", func() *ghapi.Release { r := release("v1.5.4", body); r.Draft = true; return r }(), "v1.5.2", body, nil, "not a published stable release"},
		{"dev build", release("v1.5.4", body), "dev+abc1234", body, nil, "not a release build"},
		{"no digest", func() *ghapi.Release { r := release("v1.5.4", body); r.Assets[1].Digest = ""; return r }(), "v1.5.2", body, nil, "no sha256 digest"},
		{"no asset", func() *ghapi.Release { r := release("v1.5.4", body); r.Assets = r.Assets[:1]; return r }(), "v1.5.2", body, nil, "has no linux-amd64 binary"},
	}
	for _, tc := range cases {
		m := newMachine(t)
		res := m.installer(&fakeDL{body: tc.dl}, tc.attest).Install(context.Background(), tc.rel, tc.running)
		if res.Err == nil || !strings.Contains(res.Err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", tc.name, res.Err, tc.want)
		}
		if len(res.Updated) != 0 || read(t, m.ext) != "old extension binary" || read(t, m.bin) != "old standalone binary" {
			t.Errorf("%s: a refusal changed a binary", tc.name)
		}
		if _, err := os.Stat(m.ext + ".prev"); err == nil {
			t.Errorf("%s: a refusal wrote a .prev", tc.name)
		}
		if !strings.Contains(read(t, filepath.Join(filepath.Dir(m.ext), "manifest.yml")), "tag: v1.5.2") {
			t.Errorf("%s: a refusal changed the manifest", tc.name)
		}
		noTemps(t, filepath.Dir(m.ext), filepath.Dir(m.bin))
	}
}

func TestInstallSkipsWhatItMayNotReplace(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("root writes anywhere, and Windows ignores a read-only directory mode")
	}
	m := newMachine(t)
	// a pinned extension stays; the binary's dir is read-only
	write(t, filepath.Join(filepath.Dir(m.ext), "manifest.yml"), "tag: v1.5.2\nispinned: true\n", 0o600)
	dir := filepath.Dir(m.bin)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	body := []byte("new")
	res := m.installer(&fakeDL{body: body}, nil).Install(context.Background(), release("v1.5.4", body), "v1.5.2")
	if res.Err == nil || len(res.Updated) != 0 {
		t.Fatalf("res %+v", res)
	}
	if !strings.Contains(res.Err.Error(), "by hand: gh release download v1.5.4 -R solcyber/gh-dillodash -p linux-amd64 -O "+m.bin+" --clobber") {
		t.Errorf("err %v", res.Err)
	}
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "pinned to v1.5.2 — not replaced") {
		t.Errorf("notes %q", res.Notes)
	}
	if !bytes.Equal([]byte(read(t, m.bin)), []byte("old standalone binary")) {
		t.Error("read-only binary changed")
	}
}

func TestSwapInWindows(t *testing.T) {
	old := goos
	goos = func() string { return "windows" }
	t.Cleanup(func() { goos = old })
	dir := t.TempDir()
	p, tmp := filepath.Join(dir, "gh-dillodash.exe"), filepath.Join(dir, ".new")
	write(t, p, "old", 0o755)
	write(t, tmp, "new", 0o755)
	if err := swapIn(tmp, p); err != nil {
		t.Fatal(err)
	}
	if read(t, p) != "new" || read(t, p+".prev") != "old" {
		t.Error("windows swap")
	}
}

func TestPendingRestart(t *testing.T) {
	st := Status{Running: "v1.5.2", Latest: "v1.5.4", Installed: "v1.5.4", Self: Install{Kind: KindExtension}}
	if st.Banner() != "dillodash v1.5.4 installed — R: restart now (keeps your place)" {
		t.Errorf("banner %q", st.Banner())
	}
	if g := st.Gate("approve"); !strings.Contains(g, "R restarts into it") {
		t.Errorf("gate %q", g)
	}
}

// TestSecondDashboardKeepsPrev: two dashboards run v1.5.2; one installs
// v1.5.4. The other must see the extension as v1.5.4 now (its manifest),
// so it does not install again and overwrite the .prev a rollback needs.
func TestSecondDashboardKeepsPrev(t *testing.T) {
	m := newMachine(t)
	body := []byte("new binary v1.5.4")
	if res := m.installer(&fakeDL{body: body}, nil).Install(context.Background(), release("v1.5.4", body), "v1.5.2"); res.Err != nil {
		t.Fatal(res.Err)
	}
	// the second dashboard: same exe path, still running v1.5.2
	res := m.installer(&fakeDL{body: body}, nil).Install(context.Background(), release("v1.5.4", body), "v1.5.2")
	for _, p := range res.Updated {
		if p == m.ext {
			t.Errorf("the extension was replaced twice: %q", res.Updated)
		}
	}
	if read(t, m.ext+".prev") != "old extension binary" {
		t.Error("the extension's .prev no longer holds the old build")
	}
}

// TestRollbackFailsLoudAndSticks: a rollback that rolled nothing back is
// an error; one that worked records the release it left, so the
// background updater does not put it back, and an explicit update can.
func TestRollbackFailsLoudAndSticks(t *testing.T) {
	m := newMachine(t)
	in := m.installer(nil, nil)
	in.SkipPath = filepath.Join(t.TempDir(), "rolled-back-from")
	if res := in.Rollback(); res.Err == nil {
		t.Error("no .prev anywhere: want an error")
	}
	body := []byte("new binary v1.5.4")
	if res := m.installer(&fakeDL{body: body}, nil).Install(context.Background(), release("v1.5.4", body), "v1.5.2"); res.Err != nil {
		t.Fatal(res.Err)
	}
	in.Find.Running = "v1.5.4"
	res := in.Rollback()
	if res.Err != nil || len(res.Updated) == 0 {
		t.Fatalf("rollback: %v %q", res.Err, res.Notes)
	}
	if in.Skipped() != "v1.5.4" {
		t.Errorf("skipped = %q, want v1.5.4", in.Skipped())
	}
	u := &Updater{Checker: &Checker{}, Installer: in}
	u.Checker.last = release("v1.5.4", body)
	if r := u.Install(context.Background(), Status{Running: "v1.5.2"}); r.Err == nil || !strings.Contains(r.Err.Error(), "rolled back from v1.5.4") {
		t.Errorf("auto-update after a rollback: %v", r.Err)
	}
	in.ClearSkip()
	if in.Skipped() != "" {
		t.Error("ClearSkip leaves the skip")
	}
}

// TestInstallElsewhereIsNotSelf: when the running binary is not among
// the installs replaced, the result says so (R would exec the old one).
func TestInstallElsewhereIsNotSelf(t *testing.T) {
	m := newMachine(t)
	m.opts.Exe = filepath.Join(t.TempDir(), "root-owned", "dillodash") // not on disk: nothing to replace
	body := []byte("new binary v1.5.4")
	res := m.installer(&fakeDL{body: body}, nil).Install(context.Background(), release("v1.5.4", body), "v1.5.2")
	if res.Err != nil || len(res.Updated) == 0 || !res.NotSelf {
		t.Errorf("updated %q NotSelf=%v err=%v", res.Updated, res.NotSelf, res.Err)
	}
}
