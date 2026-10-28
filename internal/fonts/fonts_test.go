package fonts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	winLocal = `C:\Users\t\AppData\Local`
	winKey   = `HKCU\Software\Microsoft\Windows NT\CurrentVersion\Fonts`
)

// box is a throwaway HOME plus a fake OS, environment and command runner
// that records every argv, so each platform branch runs on any host and
// nothing touches the real font directories or a real Windows registry.
type box struct {
	t    *testing.T
	root string // stands in for / and C:\ (C:\x -> root/c/x)
	home string
	env  map[string]string
	ran  [][]string
	out  bytes.Buffer
	// fail names commands that are "not on PATH"
	fail map[string]bool
	// regQuery is what reg.exe query answers; "" = the value is absent
	regQuery string
}

func newBox(t *testing.T) *box {
	t.Helper()
	root := t.TempDir()
	b := &box{t: t, root: root, home: filepath.Join(root, "home"), env: map[string]string{"USER": "tester"}, fail: map[string]bool{}}
	if err := os.MkdirAll(b.home, 0o755); err != nil {
		t.Fatal(err)
	}
	return b
}

// winFonts is where C:\Users\t\AppData\Local\Microsoft\Windows\Fonts
// lands on this host.
func (b *box) winFonts() string {
	return filepath.Join(b.root, "c", "Users", "t", "AppData", "Local", "Microsoft", "Windows", "Fonts")
}

func (b *box) linuxDir() string {
	return filepath.Join(b.home, ".local", "share", "fonts", Subdir)
}

func (b *box) run(argv []string) (string, error) {
	b.ran = append(b.ran, argv)
	if b.fail[argv[0]] {
		return "", errors.New("exec: \"" + argv[0] + "\": executable file not found in $PATH")
	}
	switch argv[0] {
	case "cmd.exe":
		return winLocal + "\r\n", nil
	case "wslpath":
		p := strings.Replace(argv[2], "C:", filepath.Join(b.root, "c"), 1)
		return strings.ReplaceAll(p, `\`, "/") + "\n", nil
	case "reg.exe":
		if argv[1] == "query" {
			if b.regQuery == "" {
				return "", errors.New("ERROR: The system was unable to find the specified registry key or value.")
			}
			return b.regQuery, nil
		}
		return "The operation completed successfully.\r\n", nil
	}
	return "", nil
}

func (b *box) opts(goos string, wsl bool) Options {
	proc := "Linux version 6.1.0-generic"
	if wsl {
		proc = "Linux version 5.15.167.4-microsoft-standard-WSL2"
	}
	return Options{
		OS:          goos,
		Env:         func(k string) string { return b.env[k] },
		Home:        b.home,
		ProcVersion: func() string { return proc },
		Hostname:    "box.example",
		Run:         b.run,
		Out:         &b.out,
	}
}

func (b *box) count(cmd string) int {
	n := 0
	for _, a := range b.ran {
		if a[0] == cmd {
			n++
		}
	}
	return n
}

func (b *box) argv(cmd string) [][]string {
	var out [][]string
	for _, a := range b.ran {
		if a[0] == cmd {
			out = append(out, a)
		}
	}
	return out
}

// hasFonts: both styles in dir, byte-identical to the embedded ones.
func hasFonts(t *testing.T, dir string) bool {
	t.Helper()
	for _, f := range Files() {
		got, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil || !bytes.Equal(got, f.Data) {
			return false
		}
	}
	return true
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func damage(t *testing.T, p string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

// registered: both styles registered under HKCU Fonts at their Windows
// paths, with /f, in the order install.sh used.
func registered(t *testing.T, b *box) {
	t.Helper()
	adds := b.argv("reg.exe")
	var seen []string
	for _, a := range adds {
		if a[1] == "add" {
			seen = append(seen, strings.Join(a, "\x00"))
		}
	}
	for _, f := range Files() {
		want := strings.Join([]string{"reg.exe", "add", winKey,
			"/v", strings.TrimSuffix(f.Name, ".ttf") + " (TrueType)",
			"/t", "REG_SZ",
			"/d", winLocal + `\Microsoft\Windows\Fonts\` + f.Name,
			"/f"}, "\x00")
		found := false
		for _, s := range seen {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s not registered; reg.exe calls: %q", f.Name, adds)
		}
	}
}

// ---- the embedded files are the checksummed ones ------------------------

func TestEmbeddedChecksums(t *testing.T) {
	for _, f := range Files() {
		if got := sum(f.Data); got != f.SHA256 {
			t.Errorf("%s: sha256 %s, want %s", f.Name, got, f.SHA256)
		}
		if len(f.Data) < 1<<20 {
			t.Errorf("%s: only %d bytes, not a full font", f.Name, len(f.Data))
		}
	}
	if err := verifyEmbedded(); err != nil {
		t.Fatal(err)
	}
	if len(Files()) != 2 || Files()[0].Name != RegularName || Files()[1].Name != BoldName {
		t.Errorf("Files() = %v, want Regular then Bold", Files())
	}
}

func TestLicensesShipWithTheFont(t *testing.T) {
	for _, name := range []string{"LICENSE.md", "NERD-FONTS-LICENSE.md", "NERD-FONTS-README.md", "README.md"} {
		if !exists(name) {
			t.Errorf("%s missing from the package directory", name)
		}
	}
}

func TestBadEmbeddedChecksumRefusesToInstall(t *testing.T) {
	old := regular
	regular = append([]byte(nil), regular...)
	regular[0] ^= 1
	defer func() { regular = old }()
	b := newBox(t)
	_, err := Install(b.opts("linux", false))
	if err == nil || !strings.Contains(err.Error(), "doesn't match its checksum") {
		t.Fatalf("err = %v, want a checksum refusal", err)
	}
	if exists(b.linuxDir()) {
		t.Error("a font that fails its checksum must not be installed")
	}
}

// ---- Linux: fresh, rerun, repair ---------------------------------------

func TestLinuxInstallRerunRepair(t *testing.T) {
	b := newBox(t)
	o := b.opts("linux", false)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.linuxDir()) {
		t.Fatal("font not in ~/.local/share/fonts/dillodash")
	}
	if len(res.Installed) != 2 || len(res.Skipped) != 0 {
		t.Errorf("Installed=%v Skipped=%v", res.Installed, res.Skipped)
	}
	if !strings.Contains(b.out.String(), "font: installed "+Family+" to "+b.linuxDir()) {
		t.Errorf("out = %q", b.out.String())
	}
	if fc := b.argv("fc-cache"); len(fc) != 1 || strings.Join(fc[0], " ") != "fc-cache -f "+b.linuxDir() {
		t.Errorf("fc-cache calls = %q", fc)
	}
	if b.count("reg.exe") != 0 || b.count("cmd.exe") != 0 {
		t.Errorf("no Windows registration on Linux; ran %q", b.ran)
	}
	if !contains(res.Notes, `set your terminal's font to "Hack Nerd Font Mono"`) {
		t.Errorf("Notes = %q", res.Notes)
	}
	if contains(res.Notes, "SSH") {
		t.Errorf("no SSH hint outside SSH; Notes = %q", res.Notes)
	}

	// rerun: no change, no cache refresh
	b.out.Reset()
	res, err = Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 0 || len(res.Skipped) != 2 {
		t.Errorf("rerun: Installed=%v Skipped=%v", res.Installed, res.Skipped)
	}
	if !strings.Contains(b.out.String(), "font: no change") {
		t.Errorf("rerun out = %q", b.out.String())
	}
	if b.count("fc-cache") != 1 {
		t.Errorf("rerun refreshed the font cache again: %q", b.ran)
	}

	// damaged: upgraded back
	b.out.Reset()
	damage(t, filepath.Join(b.linuxDir(), RegularName))
	res, err = Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.linuxDir()) || len(res.Installed) != 1 || len(res.Skipped) != 1 {
		t.Errorf("damaged: Installed=%v Skipped=%v", res.Installed, res.Skipped)
	}
	if !strings.Contains(b.out.String(), "font: upgraded") {
		t.Errorf("damaged out = %q", b.out.String())
	}
	if b.count("fc-cache") != 2 {
		t.Errorf("upgrade must refresh the cache: %q", b.ran)
	}
}

func TestForceRewritesCurrentFiles(t *testing.T) {
	b := newBox(t)
	o := b.opts("linux", false)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	o.Force = true
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 2 || len(res.Skipped) != 0 {
		t.Errorf("force: Installed=%v Skipped=%v", res.Installed, res.Skipped)
	}
	if !hasFonts(t, b.linuxDir()) {
		t.Error("force broke the files")
	}
}

func TestFilesAreWritten0644(t *testing.T) {
	b := newBox(t)
	if _, err := Install(b.opts("linux", false)); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(b.linuxDir(), BoldName))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode %o, want 644", st.Mode().Perm())
	}
}

func TestNeverDeletesAnything(t *testing.T) {
	b := newBox(t)
	if err := os.MkdirAll(b.linuxDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(b.linuxDir(), "SomeOtherFont.ttf")
	if err := os.WriteFile(stray, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(b.opts("linux", false)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(stray); string(got) != "keep" {
		t.Error("a file the installer did not write was touched")
	}
}

func TestXDGDataHomeIsHonoured(t *testing.T) {
	b := newBox(t)
	data := filepath.Join(b.root, "data")
	b.env["XDG_DATA_HOME"] = data
	if _, err := Install(b.opts("linux", false)); err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, filepath.Join(data, "fonts", Subdir)) {
		t.Error("font did not land under XDG_DATA_HOME")
	}
	if exists(b.linuxDir()) {
		t.Error("font also landed in ~/.local/share")
	}
}

func TestFcCacheFailureIsANoteNotAnError(t *testing.T) {
	b := newBox(t)
	b.fail["fc-cache"] = true
	res, err := Install(b.opts("linux", false))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Notes, "fc-cache") {
		t.Errorf("Notes = %q", res.Notes)
	}
}

// ---- macOS and the BSDs -------------------------------------------------

func TestMacOS(t *testing.T) {
	b := newBox(t)
	res, err := Install(b.opts("darwin", false))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(b.home, "Library", "Fonts")
	if !hasFonts(t, dir) {
		t.Error("font not in ~/Library/Fonts")
	}
	if exists(filepath.Join(b.home, ".local", "share")) {
		t.Error("font also in the XDG dir")
	}
	if len(b.ran) != 0 {
		t.Errorf("macOS runs nothing; ran %q", b.ran)
	}
	if len(res.Installed) != 2 {
		t.Errorf("Installed = %v", res.Installed)
	}
}

func TestFreeBSDTakesTheLinuxBranch(t *testing.T) {
	b := newBox(t)
	if _, err := Install(b.opts("freebsd", false)); err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.linuxDir()) {
		t.Error("font not in the XDG dir")
	}
	if b.count("reg.exe") != 0 {
		t.Error("Windows registration on FreeBSD")
	}
}

// ---- WSL: the Linux side AND the Windows side --------------------------

func TestWSLInstallsBothSides(t *testing.T) {
	b := newBox(t)
	res, err := Install(b.opts("linux", true))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.winFonts()) {
		t.Error("font not in the Windows per-user fonts")
	}
	if !hasFonts(t, b.linuxDir()) {
		t.Error("font not on the Linux side")
	}
	registered(t, b)
	if cmd := b.argv("cmd.exe"); len(cmd) != 1 || strings.Join(cmd[0], " ") != "cmd.exe /d /c echo %LOCALAPPDATA%" {
		t.Errorf("cmd.exe calls = %q", cmd)
	}
	if wp := b.argv("wslpath"); len(wp) != 1 || strings.Join(wp[0], " ") != "wslpath -u "+winLocal {
		t.Errorf("wslpath calls = %q", wp)
	}
	if !strings.Contains(b.out.String(), "Windows ("+winLocal+`\Microsoft\Windows\Fonts)`) {
		t.Errorf("out = %q", b.out.String())
	}
	if !contains(res.Notes, "restart Windows Terminal") {
		t.Errorf("Notes = %q", res.Notes)
	}
	if len(res.Installed) != 4 {
		t.Errorf("Installed = %v", res.Installed)
	}
	if b.count("fc-cache") != 1 {
		t.Errorf("fc-cache calls: %q", b.ran)
	}
	// Windows first: the paths are listed in install order
	if p := Paths(b.opts("linux", true)); len(p) != 4 || p[0] != filepath.Join(b.winFonts(), RegularName) || p[3] != filepath.Join(b.linuxDir(), BoldName) {
		t.Errorf("Paths = %v", p)
	}
}

func TestWSLByDistroName(t *testing.T) {
	b := newBox(t)
	b.env["WSL_DISTRO_NAME"] = "Ubuntu"
	if _, err := Install(b.opts("linux", false)); err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.winFonts()) {
		t.Error("WSL by WSL_DISTRO_NAME (custom kernel) skipped the Windows side")
	}
}

func TestWSLWithoutInterop(t *testing.T) {
	b := newBox(t)
	b.fail["cmd.exe"] = true
	_, err := Install(b.opts("linux", true))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"cmd.exe", "right-click, Install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want %q in it", err, want)
		}
	}
	if exists(b.linuxDir()) || exists(b.winFonts()) {
		t.Error("nothing should be written when Windows is unreachable")
	}
}

func TestWSLUnexpandedLocalAppData(t *testing.T) {
	b := newBox(t)
	o := b.opts("linux", true)
	o.Run = func(argv []string) (string, error) {
		if argv[0] == "cmd.exe" {
			return "%LOCALAPPDATA%\r\n", nil
		}
		return b.run(argv)
	}
	if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "couldn't read %LOCALAPPDATA%") {
		t.Errorf("err = %v", err)
	}
}

// ---- Windows: per-user fonts plus the registry ---------------------------

func TestWindows(t *testing.T) {
	b := newBox(t)
	// a path this host can write, standing in for C:\Users\t\AppData\Local
	local := filepath.Join(b.root, "c", "Users", "t", "AppData", "Local")
	b.env["LOCALAPPDATA"] = local
	o := b.opts("windows", false)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.winFonts()) {
		t.Error("font not in the Windows per-user fonts")
	}
	if exists(filepath.Join(b.home, ".local")) || exists(filepath.Join(b.home, "Library")) {
		t.Error("something landed in a Unix font dir")
	}
	adds := b.argv("reg.exe")
	if len(adds) != 2 {
		t.Fatalf("reg.exe calls = %q", adds)
	}
	for i, f := range Files() {
		want := []string{"reg.exe", "add", winKey, "/v", strings.TrimSuffix(f.Name, ".ttf") + " (TrueType)",
			"/t", "REG_SZ", "/d", local + `\Microsoft\Windows\Fonts\` + f.Name, "/f"}
		if strings.Join(adds[i], "\x00") != strings.Join(want, "\x00") {
			t.Errorf("reg.exe call %d = %q, want %q", i, adds[i], want)
		}
	}
	if b.count("cmd.exe") != 0 || b.count("wslpath") != 0 || b.count("fc-cache") != 0 {
		t.Errorf("native Windows needs no cmd.exe/wslpath/fc-cache: %q", b.ran)
	}
	if !contains(res.Notes, "restart Windows Terminal") {
		t.Errorf("Notes = %q", res.Notes)
	}

	// rerun: no change, registration re-asserted
	b.out.Reset()
	res, err = Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 2 || !strings.Contains(b.out.String(), "font: no change") {
		t.Errorf("rerun: Skipped=%v out=%q", res.Skipped, b.out.String())
	}
	if b.count("reg.exe") != 4 {
		t.Errorf("registration must be re-asserted every run: %q", b.argv("reg.exe"))
	}
	if contains(res.Notes, "restart") {
		t.Errorf("no restart note when nothing changed: %q", res.Notes)
	}
}

func TestWindowsWithoutLocalAppData(t *testing.T) {
	b := newBox(t)
	_, err := Install(b.opts("windows", false))
	if err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA is unset") {
		t.Errorf("err = %v", err)
	}
	if Paths(b.opts("windows", false)) != nil {
		t.Error("Paths should be nil when the directory is unknown")
	}
}

func TestWindowsRegistrationFailureIsAnError(t *testing.T) {
	b := newBox(t)
	b.env["LOCALAPPDATA"] = filepath.Join(b.root, "c", "Users", "t", "AppData", "Local")
	b.fail["reg.exe"] = true
	_, err := Install(b.opts("windows", false))
	if err == nil || !strings.Contains(err.Error(), "reg.exe couldn't register") {
		t.Errorf("err = %v", err)
	}
}

// ---- over SSH: still installs here, and says to install where you type --

func TestSSH(t *testing.T) {
	b := newBox(t)
	b.env["SSH_CONNECTION"] = "10.0.0.1 50000 10.0.0.2 22"
	res, err := Install(b.opts("linux", false))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFonts(t, b.linuxDir()) {
		t.Error("font must still be installed here")
	}
	src := "'tester@box.example:" + filepath.ToSlash(b.linuxDir()) + "/HackNerdFontMono-*.ttf'"
	for _, want := range []string{
		"this is an SSH session",
		"gh dillodash font install",
		"scp " + src + " ~/Library/Fonts/",
		"scp " + src + " ~/.local/share/fonts/ && fc-cache -f",
		"right-click, Install",
		`icons = "unicode"`,
	} {
		if !contains(res.Notes, want) {
			t.Errorf("Notes lack %q:\n%s", want, strings.Join(res.Notes, "\n"))
		}
	}
	hint := SSHHint(b.opts("linux", false))
	if len(hint) != 5 {
		t.Errorf("SSHHint = %q", hint)
	}
	delete(b.env, "SSH_CONNECTION")
	if SSHHint(b.opts("linux", false)) != nil {
		t.Error("SSHHint outside SSH must be nil")
	}
	b.env["SSH_TTY"] = "/dev/pts/3"
	if SSHHint(b.opts("linux", false)) == nil {
		t.Error("SSH_TTY alone must trigger the hint")
	}
}

// ---- Check: reads, never writes -------------------------------------------

func TestCheck(t *testing.T) {
	b := newBox(t)
	o := b.opts("linux", false)
	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 0 || len(res.Missing) != 2 {
		t.Errorf("before install: Installed=%v Missing=%v", res.Installed, res.Missing)
	}
	if !contains(res.Notes, "install would write "+filepath.Join(b.linuxDir(), RegularName)) {
		t.Errorf("Notes = %q", res.Notes)
	}
	if exists(b.linuxDir()) || len(b.ran) != 0 {
		t.Errorf("Check wrote or ran something: ran %q", b.ran)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	b.ran = nil
	res, err = Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 2 || len(res.Missing) != 0 {
		t.Errorf("after install: Installed=%v Missing=%v", res.Installed, res.Missing)
	}
	if !contains(res.Notes, "set your terminal's font") {
		t.Errorf("Notes = %q", res.Notes)
	}
	damage(t, filepath.Join(b.linuxDir(), BoldName))
	res, err = Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 1 || len(res.Missing) != 1 || !contains(res.Notes, "install would replace") {
		t.Errorf("damaged: Installed=%v Missing=%v Notes=%q", res.Installed, res.Missing, res.Notes)
	}
	if len(b.ran) != 0 {
		t.Errorf("Check on Linux runs nothing: %q", b.ran)
	}
}

func TestCheckWindowsRegistry(t *testing.T) {
	b := newBox(t)
	local := filepath.Join(b.root, "c", "Users", "t", "AppData", "Local")
	b.env["LOCALAPPDATA"] = local
	o := b.opts("windows", false)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	b.ran = nil
	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Notes, "install would register") {
		t.Errorf("absent registry value must be noted: %q", res.Notes)
	}
	queries := b.argv("reg.exe")
	if len(queries) != 2 || queries[0][1] != "query" || queries[0][3] != "/v" || queries[0][4] != "HackNerdFontMono-Regular (TrueType)" {
		t.Errorf("reg.exe query calls = %q", queries)
	}
	b.regQuery = "\r\nHKEY_CURRENT_USER\\Software\\Microsoft\\Windows NT\\CurrentVersion\\Fonts\r\n    HackNerdFontMono-Regular (TrueType)    REG_SZ    " +
		local + `\Microsoft\Windows\Fonts\HackNerdFontMono-Regular.ttf` + "\r\n\r\n"
	res, err = Check(o)
	if err != nil {
		t.Fatal(err)
	}
	// the stub answers the same for both values, so Bold's path is "wrong"
	if n := strings.Count(strings.Join(res.Notes, "\n"), "install would register"); n != 1 {
		t.Errorf("want exactly the Bold value noted, got %d: %q", n, res.Notes)
	}
	if !contains(res.Notes, "HackNerdFontMono-Bold (TrueType)") {
		t.Errorf("Notes = %q", res.Notes)
	}
}

// ---- Paths per OS ------------------------------------------------------

func TestPathsPerOS(t *testing.T) {
	b := newBox(t)
	b.env["LOCALAPPDATA"] = `C:\Users\t\AppData\Local`
	cases := map[string]string{
		"darwin":  filepath.Join(b.home, "Library", "Fonts", RegularName),
		"linux":   filepath.Join(b.linuxDir(), RegularName),
		"freebsd": filepath.Join(b.linuxDir(), RegularName),
		"windows": filepath.Join(`C:\Users\t\AppData\Local`, "Microsoft", "Windows", "Fonts", RegularName),
	}
	for goos, want := range cases {
		p := Paths(b.opts(goos, false))
		if len(p) != 2 || p[0] != want || !strings.HasSuffix(p[1], BoldName) {
			t.Errorf("%s: Paths = %v, want [%s ...]", goos, p, want)
		}
	}
}

func TestDefaultsFillTheRealMachine(t *testing.T) {
	var o Options
	if err := o.defaults(); err != nil {
		t.Fatal(err)
	}
	if o.OS == "" || o.Home == "" || o.Env == nil || o.Run == nil || o.Out == nil || o.ProcVersion == nil || o.Hostname == "" {
		t.Errorf("defaults left a field empty: %+v", o)
	}
	if _, err := run([]string{"definitely-not-a-command-dillodash"}); err == nil {
		t.Error("a missing command must be an error")
	}
}
