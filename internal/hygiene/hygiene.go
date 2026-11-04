// Package hygiene keeps a private deployment's names out of this public
// repository. A Matcher holds keyed hashes (HMAC-SHA256) of the terms a
// maintainer must never see published: a repository, a login, a domain, a
// person's name. The plaintext list, and the key, live with the
// maintainers; this repository carries only the hashes, so the list can't
// be read back, and without the key a guessed name can't be checked
// against it either.
//
// The same matcher runs over the tree (the hygiene test), the text inside
// images and PDFs, and the title, body and commit messages of a pull
// request (cmd/scan in CI).
package hygiene

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"io"
	"regexp"
	"strings"
)

// KeyEnv is the environment variable holding the key (hex, or any
// string, used as it is when it is not hex).
const KeyEnv = "DILLODASH_HYGIENE_KEY"

// RequiredEnv, set to 1 (the CI hygiene job does), makes a missing key
// a failure instead of a skip.
const RequiredEnv = "DILLODASH_HYGIENE_REQUIRED"

//go:embed denylist.txt
var denylistText string

// Denylist is the embedded hash list: one lowercase hex HMAC per line;
// blank lines and lines starting with # are ignored.
func Denylist() []string { return ParseList(denylistText) }

// ParseList reads a hash list.
func ParseList(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, strings.ToLower(l))
	}
	return out
}

// Key decodes the key's text: hex when it is hex, else its bytes.
func Key(s string) []byte {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) > 0 {
		return b
	}
	return []byte(s)
}

// Normalize is how a term and a candidate are compared: lowercase, the
// spaces collapsed.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Hash is a term's entry in the list.
func Hash(key []byte, term string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(Normalize(term)))
	return hex.EncodeToString(m.Sum(nil))
}

// Matcher finds listed terms in text.
type Matcher struct {
	key   []byte
	deny  map[string]bool
	cache map[string][]string
}

// NewMatcher is a matcher for the hashes under key.
func NewMatcher(key []byte, hashes []string) *Matcher {
	m := &Matcher{key: key, deny: map[string]bool{}, cache: map[string][]string{}}
	for _, h := range hashes {
		m.deny[strings.ToLower(h)] = true
	}
	return m
}

var (
	// tokenRE is a run of word characters with the joiners a name is
	// written with: a path, a login, a domain, a hyphenated name.
	tokenRE = regexp.MustCompile(`[a-z0-9][a-z0-9_.@/-]*`)
	// wordRE is a word inside a token.
	wordRE = regexp.MustCompile(`[a-z0-9]+`)
)

const (
	minSub   = 6  // shorter terms match only as a whole word or token
	maxSub   = 48 // longest substring tried
	maxWords = 4  // longest phrase tried
)

// Line is the hashes of the listed terms in a line, one per hit, in the
// order found (a term twice is two hits).
func (m *Matcher) Line(line string) []string {
	lower := strings.ToLower(line)
	var hits []string
	for _, tok := range tokenRE.FindAllString(lower, -1) {
		hits = append(hits, m.token(strings.TrimRight(tok, "./-_@"))...)
	}
	// phrases: up to maxWords consecutive words, joined by one space
	words := wordRE.FindAllString(lower, -1)
	for i := range words {
		for n := 2; n <= maxWords && i+n <= len(words); n++ {
			if h := Hash(m.key, strings.Join(words[i:i+n], " ")); m.deny[h] {
				hits = append(hits, h)
			}
		}
	}
	return hits
}

// token is the hits in one token: the token itself, each word of it,
// each run of its words with their joiners, and every substring of
// minSub..maxSub runes. Each distinct hit counts once per token.
func (m *Matcher) token(tok string) []string {
	if tok == "" {
		return nil
	}
	if h, ok := m.cache[tok]; ok {
		return h
	}
	found := map[string]bool{}
	var hits []string
	try := func(s string) {
		if s == "" {
			return
		}
		if h := Hash(m.key, s); m.deny[h] && !found[h] {
			found[h] = true
			hits = append(hits, h)
		}
	}
	try(tok)
	for _, w := range wordRE.FindAllString(tok, -1) {
		try(w)
	}
	r := []rune(tok)
	for l := minSub; l <= maxSub && l < len(r); l++ {
		for i := 0; i+l <= len(r); i++ {
			try(string(r[i : i+l]))
		}
	}
	m.cache[tok] = hits
	return hits
}

// Hit is a listed term found at a place.
type Hit struct {
	Line int    // 1-based; 0 for a hit with no line (image metadata)
	Hash string // the list entry
}

// Text is every hit in a text, line by line.
func (m *Matcher) Text(data []byte) []Hit {
	var out []Hit
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		for _, h := range m.Line(sc.Text()) {
			out = append(out, Hit{Line: n, Hash: h})
		}
	}
	return out
}

// ---- text inside binary files ----

// PNGText is the text a PNG carries in its tEXt, zTXt and iTXt chunks
// (keywords and values), one entry per line. A file that is not a PNG
// gives nothing.
func PNGText(data []byte) []byte {
	const sig = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(data, []byte(sig)) {
		return nil
	}
	var out bytes.Buffer
	p := data[len(sig):]
	for len(p) >= 12 {
		n := int(binary.BigEndian.Uint32(p[:4]))
		typ := string(p[4:8])
		if n < 0 || 8+n+4 > len(p) {
			break
		}
		body := p[8 : 8+n]
		switch typ {
		case "tEXt":
			out.Write(bytes.ReplaceAll(body, []byte{0}, []byte{' '}))
			out.WriteByte('\n')
		case "zTXt":
			if k, rest, ok := bytes.Cut(body, []byte{0}); ok && len(rest) > 1 {
				out.Write(k)
				out.WriteByte(' ')
				out.Write(inflate(rest[1:]))
				out.WriteByte('\n')
			}
		case "iTXt":
			// keyword 0 flag method lang 0 translated 0 text
			parts := bytes.SplitN(body, []byte{0}, 2)
			if len(parts) == 2 && len(parts[1]) >= 2 {
				compressed := parts[1][0] == 1
				rest := bytes.SplitN(parts[1][2:], []byte{0}, 3)
				out.Write(parts[0])
				out.WriteByte(' ')
				for i, r := range rest {
					if i == 2 && compressed {
						r = inflate(r)
					}
					out.Write(r)
					out.WriteByte(' ')
				}
				out.WriteByte('\n')
			}
		}
		p = p[8+n+4:]
	}
	return out.Bytes()
}

var (
	streamRE    = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	printableRE = regexp.MustCompile(`[\x20-\x7e]{4,}`)
)

// PDFText is the readable text of a PDF: its printable runs (the
// metadata, uncompressed content) and those of every stream that
// inflates. Glyph-encoded text may still hide; this is a net, not a
// parser.
func PDFText(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return nil
	}
	var out bytes.Buffer
	runs := func(b []byte) {
		for _, r := range printableRE.FindAll(b, -1) {
			out.Write(r)
			out.WriteByte('\n')
		}
	}
	runs(data)
	for _, m := range streamRE.FindAllSubmatch(data, -1) {
		runs(inflate(m[1]))
	}
	return out.Bytes()
}

func inflate(b []byte) []byte {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	defer r.Close()
	out, _ := io.ReadAll(io.LimitReader(r, 8<<20))
	return out
}

// ---- structural shapes (no key needed) ----

// Structural are the shapes that never belong in the public repo: a
// decision-record number, a secret reference, a token.
var Structural = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\badr[ _-]?[0-9]{3,4}\b`),
	regexp.MustCompile(`op:/{2}`), // written so this line does not match itself
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b`),
}

// AccountID is a 12-digit number (an account id?); 123456789012 is the
// documentation placeholder.
var AccountID = regexp.MustCompile(`\b[0-9]{12}\b`)
