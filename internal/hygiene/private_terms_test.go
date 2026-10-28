package hygiene

// TestNoPrivateTerms walks the repository and fails on anything that
// belongs to a private deployment rather than to the public tool: a
// structural pattern (an internal decision-record number, an account id, a
// secret reference, a token shape, an internal path), or a token whose
// salted hash is on a denylist the maintainers hold. The plaintext list is
// not in this repository; a hit prints the file, the line and a hash prefix.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const salt = "dillodash-private-terms-v1"

var deny = map[string]bool{
	"612160b5be88ca47ff78ab7e67f58cafa856ba31d3ea76448ae0301d26f50fe9": true,
	"8ca3745698c44201d628217a759ce609361cbfab42c80d0f13807efc6bbc2697": true,
	"92ceaea76f72cb3ccba0e4c14ab80114725a7417905806615fb88c7ad8e0966c": true,
	"5c0c05a221fa99c2625d4310991aa842255e9fe613c095e8a24ede5c53cc1eb3": true,
	"5bcc96896f974899e1ca272a49c0ffc3668cda1c1bacdb05b6654ad39aacbd2f": true,
	"7d9a3c08fc59b916bb0f68ec5273d90ab56d1a5c499c67fa41d8133d2171b066": true,
	"478c49b25ae74762b42bc1c13b7cd4e09762e81fcbdc0aa24fd9ad831b254146": true,
	"db94351348345a92be56526f2d4c4d13db2f5a6fb74e3f7441f0a7b2e3105c8d": true,
	"ad7a934b682f807c02ee789fe679272e1d8d8c7419723d63603203b76dcc395e": true,
	"77db3aeab261dafc85e67a6bcf213b9d7761c0636a2fd117989c40adf8c706c2": true,
	"5fa0bed169ab56796bcd8495deb7afb01a59e33cc791a87bdd5e3f44a9ed2f45": true,
	"78449b1a6c24ccb61369c9e7b4aebdaac24cc66274bb5c7c7a87bff3ed5be47f": true,
	"95baad9161b10b7c0486f464e3c61953ccf90c279a065a3cdf22de43504a2875": true,
	"121be5c148ed16e1fcd0c9780f11ab1f0c2ef35c1b8a663893b89a9d65417f19": true,
	"57fb926e7536121a90f51619db3d357340ca6d06fe3e1dddea3a859729edf46a": true,
	"4f143b59ad0d7e53785070a9f895cdb8aa75f9f299a48464420d73505a1beb0c": true,
	"2943d72ae4a9415f849edae6b3c96a4a7e5b88826ec4ce3424d602ddce46269a": true,
	"cc9023b349f37fbdc251767d9ca3df6cc3b7163170e0208ca5525e30ee631297": true,
	"02aec9074339c304c0bdf7bf8445f20505c47675203cba6b845cd6304e493f4d": true,
	"19330809d67e92a9bb918ffd6195b74db41aaa2d50c267c675d651b36665e1c7": true,
	"99a414f78e0c5bcf448af29a8c3db4ddf4fb7f3ff1780fe7be9a1dbb108975a4": true,
	"ed2275fb3e0d4066df3a08de8829b0598c3aded963998e87e8f56eb28de54ec0": true,
	"76e33b3e3944e2697bce6cbd15ca82f2784ea4b3945261a971e884b974a2a3ca": true,
	"abc63f8c95ff18356ec8168dc51248c6602eb937c01259cf86cf1d286fd947b3": true,
	"642b33450798235c33d077b7687c38d4c7b0c86f0a52204d24895339daf1f750": true,
	"adb3c972f850c9baea2e1dfe88e1edf665bd4b7d3f2e17b6e16b3a039c63a98a": true,
	"50ea22a13467cbd9e250e94a794bca3b26649300aca821ed0759a9d8b024369f": true,
	"5f21fd34c1775b10fa6c2cd11a560c6594f80f4de0b11bcdf1c44f895857f35e": true,
}

var structural = []*regexp.Regexp{
	regexp.MustCompile(`\bADR ?[0-9]{3,4}\b`),
	regexp.MustCompile(`op://`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b`),
	regexp.MustCompile(`tools/pr-dash`),
	regexp.MustCompile(`brain/RUNBOOKS`),
}

var accountID = regexp.MustCompile(`\b[0-9]{12}\b`)

var tokenRE = regexp.MustCompile(`[a-z0-9][a-z0-9_.-]*`)

func hashed(s string) string {
	h := sha256.Sum256([]byte(salt + s))
	return hex.EncodeToString(h[:])
}

func repoRoot(t *testing.T) string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		wd, _ := os.Getwd()
		return filepath.Join(wd, "..", "..")
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
		case ".ttf", ".png", ".otf", ".woff", ".woff2", ".ico", ".sum":
			continue
		}
		if strings.HasSuffix(f, "private_terms_test.go") || filepath.Base(f) == ".gitleaks.toml" {
			continue
		}
		list = append(list, f)
	}
	return list
}

func TestNoPrivateTerms(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	for _, rel := range files(t, root) {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue // unreadable or binary
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		n := 0
		for sc.Scan() {
			n++
			line := sc.Text()
			for _, re := range structural {
				// the documented generic placeholder is fine
				if strings.Contains(line, "op://Private/") {
					continue
				}
				if re.MatchString(line) {
					t.Errorf("%s:%d: matches %s", rel, n, re)
				}
			}
			for _, m := range accountID.FindAllString(line, -1) {
				if m != "123456789012" {
					t.Errorf("%s:%d: 12-digit number %s (an account id?)", rel, n, m)
				}
			}
			lower := strings.ToLower(line)
			for _, tok := range tokenRE.FindAllString(lower, -1) {
				if len(tok) < 6 {
					continue
				}
				if seen[tok] {
					continue
				}
				seen[tok] = true
				if hit := check(tok); hit != "" {
					t.Errorf("%s:%d: private term (hash %s…)", rel, n, hit[:12])
				}
			}
		}
	}
}

// check hashes the token and every substring of length 6..40 of it.
func check(tok string) string {
	if deny[hashed(tok)] {
		return hashed(tok)
	}
	r := []rune(tok)
	for l := 6; l <= 40 && l < len(r); l++ {
		for i := 0; i+l <= len(r); i++ {
			if h := hashed(string(r[i : i+l])); deny[h] {
				return h
			}
		}
	}
	return ""
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
