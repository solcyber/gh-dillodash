package selfupdate

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Kind is how an install is laid out, which decides how it is upgraded.
type Kind string

const (
	// KindExtension is the gh extension: a release binary in gh's data
	// dir beside the manifest.yml gh wrote (gh extension upgrade).
	KindExtension Kind = "gh extension"
	// KindBinary is a standalone release binary (on PATH, say).
	KindBinary Kind = "binary"
	// KindLauncher is a script that runs another install (a wrapper that
	// sets a token and runs gh dillodash): it has no version of its own
	// and is never replaced.
	KindLauncher Kind = "launcher"
)

// Install is one dillodash this machine can run.
type Install struct {
	Kind    Kind
	Path    string
	Version string // "" when unknown
	Running bool   // this process runs it
	Pinned  bool   // a gh extension pinned to its tag
	Runs    string // a launcher: what it runs, when that is recognisable
	Note    string
}

// ManualCommand is the one command that puts release tag over this
// install by hand: what to run when the install path is not writable
// to dillodash itself.
func (i Install) ManualCommand(tag, asset string) string {
	if i.Kind == KindBinary {
		return fmt.Sprintf("gh release download %s -R %s -p %s -O %s --clobber", tag, Repo, asset, i.Path)
	}
	return ManualHint
}

// UpgradeHint is the one command (or step) that brings this install to
// version to.
func (i Install) UpgradeHint(to string) string {
	switch i.Kind {
	case KindBinary:
		return i.Path + " update"
	case KindLauncher:
		return "upgrade what " + i.Path + " runs"
	}
	return "gh extension upgrade dillodash"
}

// FindOptions say where to look; every field is injected so tests never
// touch the developer's machine or start a program.
type FindOptions struct {
	Exe     string // the running binary, read at startup
	Running string // its version
	DataDir string // gh's data dir; extensions live under it
	Path    string // $PATH
	GOOS    string
	// Version runs `<path> version` on a binary that is not the running
	// one; nil leaves its version unknown.
	Version func(path string) (string, error)
}

func (o FindOptions) exeName(name string) string {
	if o.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ExtensionDir is where gh keeps the dillodash extension.
func (o FindOptions) ExtensionDir() string {
	if o.DataDir == "" {
		return ""
	}
	return filepath.Join(o.DataDir, "extensions", "gh-dillodash")
}

// ExtensionBinary is the gh extension's binary path ("" without a data
// dir).
func (o FindOptions) ExtensionBinary() string {
	if d := o.ExtensionDir(); d != "" {
		return filepath.Join(d, o.exeName("gh-dillodash"))
	}
	return ""
}

// Self is the running install: the gh extension when the running binary
// is the extension's, else a standalone binary.
func Self(o FindOptions) Install {
	if o.Exe == "" { // unknown: hint the usual install
		return Install{Kind: KindExtension, Version: o.Running, Running: true}
	}
	in := Install{Kind: KindBinary, Path: o.Exe, Version: o.Running, Running: true}
	if ext := o.ExtensionBinary(); ext != "" && sameFile(ext, o.Exe) {
		in.Kind, in.Path = KindExtension, ext
		if m, err := ReadManifest(filepath.Join(o.ExtensionDir(), "manifest.yml")); err == nil {
			in.Pinned = m.Pinned
		}
	}
	return in
}

// Find lists every dillodash this machine can run: the gh extension,
// then each dillodash or gh-dillodash on PATH (a launcher script is
// listed as one and never asked its version), then the running binary
// when it is none of those. The same file reached twice is listed once.
func Find(o FindOptions) []Install {
	var out []Install
	var seen []string
	add := func(in Install) {
		for _, s := range seen {
			if sameFile(s, in.Path) {
				return
			}
		}
		seen = append(seen, in.Path)
		out = append(out, in)
	}
	if ext := o.ExtensionBinary(); ext != "" {
		if _, err := os.Stat(ext); err == nil {
			in := Install{Kind: KindExtension, Path: ext}
			if m, err := ReadManifest(filepath.Join(o.ExtensionDir(), "manifest.yml")); err == nil {
				in.Version, in.Pinned = m.Tag, m.Pinned
			} else {
				in.Note = "no manifest.yml: a local install, not from a release"
			}
			if sameFile(ext, o.Exe) {
				in.Running, in.Version = true, o.Running
			}
			add(in)
		}
	}
	for _, dir := range filepath.SplitList(o.Path) {
		if dir == "" {
			continue
		}
		for _, name := range []string{"dillodash", "gh-dillodash"} {
			p := filepath.Join(dir, o.exeName(name))
			st, err := os.Stat(p)
			if err != nil || st.IsDir() {
				continue
			}
			add(o.classify(p))
		}
	}
	if o.Exe != "" {
		add(Install{Kind: KindBinary, Path: o.Exe, Version: o.Running, Running: true})
	}
	return out
}

// classify reads the head of p: a script is a launcher, anything else a
// binary whose version is asked (unless it is this process).
func (o FindOptions) classify(p string) Install {
	head := readHead(p, 4096)
	if bytes.HasPrefix(head, []byte("#!")) {
		in := Install{Kind: KindLauncher, Path: p}
		if bytes.Contains(head, []byte("gh dillodash")) {
			in.Runs = "gh dillodash"
		}
		return in
	}
	in := Install{Kind: KindBinary, Path: p}
	switch {
	case sameFile(p, o.Exe):
		in.Running, in.Version = true, o.Running
	case o.Version != nil:
		out, err := o.Version(p)
		if err != nil {
			in.Note = "version unknown: " + err.Error()
			break
		}
		// "dillodash v1.2.3"
		if f := strings.Fields(out); len(f) >= 2 && f[0] == "dillodash" {
			in.Version = f[1]
		} else {
			in.Note = "not a dillodash binary?"
		}
	}
	return in
}

func readHead(p string, n int) []byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	b := make([]byte, n)
	k, _ := io.ReadFull(f, b)
	return b[:k]
}

// sameFile reports whether a and b are the same file on disk (through
// symlinks too); false when either is missing.
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

// Manifest is what dillodash reads of the manifest.yml gh writes beside
// an extension installed from a release.
type Manifest struct {
	Tag    string
	Pinned bool
}

// ReadManifest reads the tag and the pin from a gh extension manifest.
func ReadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	for _, l := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "tag":
			m.Tag = v
		case "ispinned":
			m.Pinned = v == "true"
		}
	}
	if m.Tag == "" {
		return m, fmt.Errorf("%s has no tag", path)
	}
	return m, nil
}
