package hygiene

// TestNoPrivateTerms walks the repository and fails on anything that
// belongs to a private deployment rather than to the public tool: a
// structural pattern (a decision-record number, an account id, a secret
// reference, a token shape), or a term on the maintainers' keyed
// denylist (denylist.txt, HMAC-SHA256 under $DILLODASH_HYGIENE_KEY). The
// plaintext list and the key are not in this repository; a hit prints
// the file, the line and a hash prefix. Text files are read line by
// line; PNGs by their text chunks, PDFs by their readable text.
//
// Without the key the denylist part is skipped (logged); with
// DILLODASH_HYGIENE_REQUIRED=1, as in CI, a missing key fails.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// exempt are hits allowed in one file, each with the reason: the hash of
// the term (as in denylist.txt) and why it must stay.
var exempt = map[string]map[string]string{
	// the predecessor tool's config directory: `config migrate` must find
	// the file there, so the name stays, in this one constant
	"internal/config/find.go": {
		"84b3798835d54b9e215f4d1244f8b67912816a226901927a5f58c2ea01867569": "the legacy config directory config migrate reads",
	},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; this test reads the tree through git")
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git checkout (a module zip or a tarball); nothing to scan")
	}
	return strings.TrimSpace(string(out))
}

func files(t *testing.T, root string) []string {
	out, err := exec.Command("git", "-C", root, "ls-files", "-co", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var list []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f == "" {
			continue
		}
		switch strings.ToLower(filepath.Ext(f)) {
		case ".ttf", ".otf", ".woff", ".woff2", ".ico", ".sum":
			continue
		}
		// the hash list (hex runs read as account ids) and this file's
		// own placeholder shapes
		if strings.HasSuffix(f, "internal/hygiene/denylist.txt") || strings.HasSuffix(f, "private_terms_test.go") || filepath.Base(f) == ".gitleaks.toml" {
			continue
		}
		list = append(list, f)
	}
	return list
}

// textOf is what a file says: a text file as it is, a PNG's text chunks,
// a PDF's readable text; nil for other binaries.
func textOf(data []byte) (text []byte, binary bool) {
	if t := PNGText(data); t != nil || bytes.HasPrefix(data, []byte("\x89PNG")) {
		return t, true
	}
	if t := PDFText(data); t != nil {
		return t, true
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, true
	}
	return data, false
}

// matcher is the denylist matcher, or nil when the key is not set.
func matcher(t *testing.T) *Matcher {
	key := os.Getenv(KeyEnv)
	if strings.TrimSpace(key) == "" {
		if os.Getenv(RequiredEnv) == "1" {
			t.Fatalf("%s is not set and %s=1: the denylist can't be checked", KeyEnv, RequiredEnv)
		}
		t.Logf("%s is not set: the denylist part is skipped (the structural checks still run)", KeyEnv)
		return nil
	}
	return NewMatcher(Key(key), Denylist())
}

func TestNoPrivateTerms(t *testing.T) {
	root := repoRoot(t)
	m := matcher(t)
	for _, rel := range files(t, root) {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		text, binary := textOf(data)
		if text == nil {
			continue
		}
		where := rel
		if binary {
			where += " (embedded text)"
		}
		for n, line := range strings.Split(string(text), "\n") {
			for _, re := range Structural {
				if re.MatchString(line) {
					t.Errorf("%s:%d: matches %s", where, n+1, re)
				}
			}
			if !binary {
				for _, id := range AccountID.FindAllString(line, -1) {
					if id != "123456789012" {
						t.Errorf("%s:%d: 12-digit number %s (an account id?)", where, n+1, id)
					}
				}
			}
		}
		if m == nil {
			continue
		}
		for _, h := range m.Text(text) {
			if why, ok := exempt[filepath.ToSlash(rel)][h.Hash]; ok && why != "" {
				continue
			}
			t.Errorf("%s:%d: private term (hash %s…)", where, h.Line, h.Hash[:12])
		}
	}
}

// TestMatcher proves the matcher on a test key and list: whole tokens,
// words inside paths, logins and domains, short terms as whole words
// only, phrases, embedded long terms, and every hit reported.
func TestMatcher(t *testing.T) {
	key := Key("00112233445566778899aabbccddeeff")
	terms := []string{"acme-internal", "ops", "Release Board", "zebra.example", "kangaroo"}
	var list []string
	for _, term := range terms {
		list = append(list, Hash(key, term))
	}
	m := NewMatcher(key, list)
	hash := func(s string) string { return Hash(key, s) }
	cases := []struct {
		line string
		want []string
	}{
		{"see acme-internal for it", []string{hash("acme-internal")}},
		{"path brain/acme-internal/x and acme-internal again", []string{hash("acme-internal"), hash("acme-internal")}},
		{"the ops team", []string{hash("ops")}},
		{"stops and opsy are fine", nil}, // a short term matches only a whole word
		{"run by org/ops@host", []string{hash("ops")}},
		{"the release   board met", []string{hash("release board")}},
		{"mail bob@zebra.example.", []string{hash("zebra.example")}},
		{"superkangaroos", []string{hash("kangaroo")}}, // a long term inside a word
		{"KANGAROO", []string{hash("kangaroo")}},
		{"nothing to see", nil},
	}
	for _, c := range cases {
		got := m.Line(c.line)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q: got %d hits %v, want %d", c.line, len(got), got, len(c.want))
		}
	}
	other := NewMatcher(Key("another key"), list)
	if hits := other.Line("acme-internal"); len(hits) != 0 {
		t.Errorf("another key matched: %v", hits)
	}
	if Hash(key, "  Release\tBoard ") != hash("release board") {
		t.Error("terms are normalized: case and spaces")
	}
	if got := m.Text([]byte("a\nkangaroo\nb ops\n")); len(got) != 2 || got[0].Line != 2 || got[1].Line != 3 {
		t.Errorf("Text lines: %+v", got)
	}
}

// TestEmbeddedText reads the text a PNG and a PDF carry.
func TestEmbeddedText(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, body []byte) []byte {
		var b bytes.Buffer
		n := len(body)
		b.Write([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
		b.WriteString(typ)
		b.Write(body)
		b.Write([]byte{0, 0, 0, 0}) // crc, not checked
		return b.Bytes()
	}
	png = append(png, chunk("tEXt", []byte("Author\x00alice at kangaroo works"))...)
	png = append(png, chunk("iTXt", []byte("Comment\x00\x00\x00en\x00\x00made for zebra.example"))...)
	png = append(png, chunk("IEND", nil)...)
	text := string(PNGText(png))
	if !strings.Contains(text, "kangaroo works") || !strings.Contains(text, "made for zebra.example") {
		t.Errorf("PNG text: %q", text)
	}
	if PNGText([]byte("not a png")) != nil {
		t.Error("not a PNG: no text")
	}
	pdf := []byte("%PDF-1.4\n1 0 obj << /Author (Kangaroo Person) >> endobj\n%%EOF")
	if !strings.Contains(string(PDFText(pdf)), "Kangaroo Person") {
		t.Errorf("PDF text: %q", PDFText(pdf))
	}
}

func TestGitignoreCoversConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"config.toml", "*.token"} {
		if !strings.Contains(string(data), want) {
			t.Errorf(".gitignore lacks %s", want)
		}
	}
}
