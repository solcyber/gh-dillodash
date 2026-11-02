package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Downloader fetches a release asset's bytes.
type Downloader interface {
	DownloadAsset(ctx context.Context, repo model.Repo, id int64, w io.Writer, limit int64) error
}

// Runner runs a program and returns its combined output; the build
// attestation is checked with gh. Tests script it.
type Runner func(ctx context.Context, argv []string) ([]byte, error)

// maxAsset bounds a download: a release binary is tens of megabytes.
const maxAsset = 256 << 20

// Installer brings every install it may replace to a release: download
// once, verify (GitHub's digest, then the build attestation), then a
// rename beside each install, the old binary kept as <name>.prev.
type Installer struct {
	DL     Downloader
	Run    Runner
	GOOS   string
	GOARCH string
	Find   FindOptions
}

// ErrDownload marks an install that failed to download (worth a retry),
// unlike a refusal (a digest or an attestation that did not verify).
var ErrDownload = errors.New("download failed")

// Result is what an install or a rollback did.
type Result struct {
	Version string
	Updated []string // the paths replaced
	Notes   []string // installs left alone, and why
	Err     error    // nothing was installed (or rolled back)
}

// AssetName is the release asset for a platform, as the release build
// names it.
func AssetName(goos, goarch string) string {
	n := goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// ManualHint is the one command that upgrades by hand, for when the
// automatic install refuses.
const ManualHint = "gh extension upgrade dillodash"

// Install installs rel over every install it may replace. It refuses a
// draft, a prerelease, a release that is not newer than running (never
// a downgrade) and any asset whose digest or attestation does not
// verify; a refusal leaves every binary on disk untouched.
func (in *Installer) Install(ctx context.Context, rel *ghapi.Release, running string) Result {
	switch {
	case rel == nil:
		return Result{Err: errors.New("no release known yet")}
	case rel.Draft || rel.Prerelease || !Comparable(rel.Tag):
		return Result{Err: fmt.Errorf("%s is not a published stable release — never installed", rel.Tag)}
	case !Comparable(running):
		return Result{Err: fmt.Errorf("%s is not a release build — only a release build is replaced", running)}
	case !Newer(rel.Tag, running):
		return Result{Version: rel.Tag, Err: fmt.Errorf("%s is not newer than %s — nothing to install", rel.Tag, running)}
	}
	res := Result{Version: rel.Tag}
	name := AssetName(in.GOOS, in.GOARCH)
	var asset *ghapi.ReleaseAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			asset = &rel.Assets[i]
		}
	}
	if asset == nil {
		res.Err = fmt.Errorf("%s has no %s binary", rel.Tag, name)
		return res
	}
	want, ok := strings.CutPrefix(asset.Digest, "sha256:")
	if !ok || len(want) != 64 {
		res.Err = fmt.Errorf("%s's %s has no sha256 digest — an unverifiable binary is never installed", rel.Tag, name)
		return res
	}
	targets, notes := in.targets(rel.Tag)
	res.Notes = notes
	if len(targets) == 0 {
		return res
	}
	src, err := in.fetch(ctx, rel.Tag, asset, want, targets)
	if err != nil {
		res.Err = err
		return res
	}
	defer os.Remove(src)
	for _, t := range targets {
		if err := place(src, want, t, rel.Tag); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s NOT replaced: %s; by hand: %s", t.Path, err, t.ManualCommand(rel.Tag, name)))
			continue
		}
		res.Updated = append(res.Updated, t.Path)
	}
	if len(res.Updated) == 0 {
		res.Err = fmt.Errorf("%s NOT installed anywhere", rel.Tag)
	}
	return res
}

// targets are the installs Install may replace: release installs older
// than tag. A launcher, a local or pinned extension and a binary of
// unknown version are left alone, each with a note.
func (in *Installer) targets(tag string) ([]Install, []string) {
	var out []Install
	var notes []string
	for _, i := range Find(in.Find) {
		switch {
		case i.Kind == KindLauncher:
			what := "a launcher"
			if i.Runs != "" {
				what += " (runs " + i.Runs + ")"
			}
			notes = append(notes, i.Path+": "+what+" — never replaced")
		case i.Kind == KindExtension && i.Note != "":
			notes = append(notes, i.Path+": "+i.Note+" — not replaced")
		case i.Pinned:
			notes = append(notes, fmt.Sprintf("%s: pinned to %s — not replaced", i.Path, i.Version))
		case !Comparable(i.Version):
			notes = append(notes, i.Path+": version unknown — not replaced")
		case Newer(tag, i.Version):
			out = append(out, i)
		}
	}
	return out, notes
}

// fetch downloads the asset into a temp file beside the first target it
// can write to, and verifies it: the sha256 against GitHub's digest,
// then the build attestation. The verified file's path comes back.
func (in *Installer) fetch(ctx context.Context, tag string, asset *ghapi.ReleaseAsset, want string, targets []Install) (string, error) {
	if in.DL == nil {
		return "", errors.New("no download client")
	}
	var f *os.File
	var werr error
	for _, t := range targets {
		if f, werr = os.CreateTemp(filepath.Dir(t.Path), ".dillodash-"+tag+"-*"); werr == nil {
			break
		}
	}
	if f == nil {
		return "", fmt.Errorf("no install is writable (%v); by hand: %s", werr, targets[0].ManualCommand(tag, asset.Name))
	}
	path := f.Name()
	h := sha256.New()
	err := in.DL.DownloadAsset(ctx, Repo, asset.ID, io.MultiWriter(f, h), maxAsset)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("%w: %s: %v", ErrDownload, asset.Name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(path)
		return "", fmt.Errorf("checksum mismatch: %s's sha256 is %s, the release says %s — nothing installed", asset.Name, got[:12], want[:12])
	}
	if err := in.attest(ctx, path); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// attest checks the build attestation with gh: the binary was built by
// this repo's release workflow. No gh, or no attestation, refuses.
func (in *Installer) attest(ctx context.Context, path string) error {
	if in.Run == nil {
		return errors.New("build attestation not checked (no gh to run) — nothing installed")
	}
	out, err := in.Run(ctx, []string{"gh", "attestation", "verify", path, "--repo", Repo.String()})
	if err != nil {
		why := firstLine(string(out))
		if why == "" {
			why = err.Error()
		}
		return fmt.Errorf("build attestation not verified (%s) — nothing installed", why)
	}
	return nil
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// place copies the verified src beside t, checks it again, keeps t as
// <t>.prev and renames the copy over t; for the gh extension the
// manifest's tag follows (its old manifest kept as manifest.yml.prev).
// A failure leaves t as it was.
func place(src, want string, t Install, tag string) error {
	dir := filepath.Dir(t.Path)
	tmp, err := os.CreateTemp(dir, ".dillodash-new-*")
	if err != nil {
		return fmt.Errorf("not writable (%v)", err)
	}
	tmpPath := tmp.Name()
	fail := func(err error) error { os.Remove(tmpPath); return err }
	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		return fail(err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), in)
	in.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fail(errors.New("the copy does not match the verified download"))
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fail(err)
	}
	if err := swapIn(tmpPath, t.Path); err != nil {
		return fail(err)
	}
	if t.Kind == KindExtension {
		manifest := filepath.Join(dir, "manifest.yml")
		if err := setManifestTag(manifest, tag); err != nil {
			return fmt.Errorf("installed, but manifest.yml still names the old tag (%v): gh extension list shows it", err)
		}
	}
	return nil
}

// swapIn keeps path as path.prev and renames tmp over path. The old
// binary stays runnable throughout: on Unix a hard link (else a copy)
// makes the .prev and the rename replaces atomically; Windows renames a
// running binary aside instead, and puts it back on a failure.
func swapIn(tmp, path string) error {
	prev := path + ".prev"
	_ = os.Remove(prev)
	if goos() == "windows" {
		if err := os.Rename(path, prev); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Rename(prev, path)
			return err
		}
		return nil
	}
	if err := os.Link(path, prev); err != nil {
		if err := copyFile(path, prev); err != nil {
			return fmt.Errorf("couldn't keep %s: %v", filepath.Base(prev), err)
		}
	}
	return os.Rename(tmp, path)
}

// goos is runtime.GOOS; a var so the Windows branch is testable.
var goos = func() string { return runtimeGOOS }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// setManifestTag rewrites the tag line of a gh extension manifest,
// keeping the old manifest as manifest.yml.prev.
func setManifestTag(path, tag string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "tag" {
			lines[i] = "tag: " + tag
			found = true
		}
	}
	if !found {
		return errors.New("no tag line")
	}
	if err := config.WriteFileAtomic(path+".prev", raw, 0o600); err != nil {
		return err
	}
	return config.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")), 0o600)
}

// Rollback swaps every install with a <name>.prev back to it (and the
// gh extension's manifest with it); the replaced binary becomes the new
// .prev, so a second rollback undoes the first.
func (in *Installer) Rollback() Result {
	var res Result
	for _, i := range Find(in.Find) {
		if i.Kind == KindLauncher {
			continue
		}
		prev := i.Path + ".prev"
		if _, err := os.Stat(prev); err != nil {
			continue
		}
		if err := swapBack(i.Path, prev); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s NOT rolled back: %s", i.Path, err))
			continue
		}
		if i.Kind == KindExtension {
			m := filepath.Join(filepath.Dir(i.Path), "manifest.yml")
			if _, err := os.Stat(m + ".prev"); err == nil {
				if err := swapBack(m, m+".prev"); err != nil {
					res.Notes = append(res.Notes, fmt.Sprintf("%s rolled back, but its manifest.yml was not: %s", i.Path, err))
				}
			}
		}
		res.Updated = append(res.Updated, i.Path)
	}
	if len(res.Updated) == 0 && len(res.Notes) == 0 {
		res.Err = errors.New("no install has a .prev to roll back to")
	}
	return res
}

// swapBack exchanges path and prev through a third name.
func swapBack(path, prev string) error {
	mid := path + ".rollback"
	_ = os.Remove(mid)
	if err := os.Rename(path, mid); err != nil {
		return err
	}
	if err := os.Rename(prev, path); err != nil {
		_ = os.Rename(mid, path)
		return err
	}
	return os.Rename(mid, prev)
}

// Updater checks and installs: the dashboard's background updater and
// `dillodash update`.
type Updater struct {
	*Checker
	Installer *Installer
}

// Install installs the latest release the check read over every install
// it may replace.
func (u *Updater) Install(ctx context.Context, st Status) Result {
	if u.Installer == nil {
		return Result{Err: errors.New("no installer")}
	}
	return u.Installer.Install(ctx, u.Checker.Release(), st.Running)
}
