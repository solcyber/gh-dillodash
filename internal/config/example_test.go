package config

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// -update rewrites the repo-root config.example.toml from the embedded
// internal/config/example.toml (the source of truth):
//
//	go test ./internal/config -update
var update = flag.Bool("update", false, "rewrite config.example.toml from the embedded example")

const rootExample = "../../config.example.toml"

func TestExampleParsesClean(t *testing.T) {
	c, err := Parse([]byte(Example()), "config.example.toml", Overrides{})
	if err != nil {
		t.Fatalf("Example() does not parse: %v", err)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("Example() warns: %v", c.Warnings)
	}
	if len(c.Reviews.Repos) != 1 || c.Reviews.Repos[0].String() != "acme/infra" {
		t.Errorf("example repos = %v", c.Reviews.Repos)
	}
	if c.TerraformApprove != nil || c.MaintenanceWindow != nil || c.ReleasePR != nil {
		t.Error("example workflows should be off")
	}
}

// Every key of the schema appears in the example, either live or
// commented out with its default, and the commented defaults really are
// the defaults: uncommenting all of them must change nothing.
func TestExampleCoversEveryKey(t *testing.T) {
	src := Example()
	keyRE := regexp.MustCompile(`(?m)^#? ?([a-z_]+) = `)
	seen := map[string]bool{}
	for _, m := range keyRE.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	want := []string{
		"host", "login", "repos", "ci_recheck_secs", "refresh_secs", "icons", "theme", "motion", "mascot", "logins",
		"enabled", "digest_bot", "plan_workflow", "gate_context", "gate_done_prefixes", "approve_command",
		"confirm_destroys_flag", "outside_window_flag", "outside_window_min_reason", "ceremony_window",
		"manifest_repo", "timezone", "tz_label", "cutoff", "start", "release_from", "end", "queued_label",
		"scheduled_label", "rolled_label", "manifest_label", "manifest_title", "conductor_bot", "team",
		"attend_command", "release_command", "cancel_command",
		"org", "ruleset", "discover_ttl_secs", "cache", "production_branch", "staging_branch", "release_prefix",
		"hotfix_prefix", "go_tag_prefix", "hold_tag_prefix", "list_other_prs", "release_now_command",
		"release_now_min_reason", "approval_queues_for_window", "merge_method", "merged_keep_secs",
		"deploy_stuck_secs", "deploy_refresh_secs", "deploy_approval_comment",
	}
	for _, k := range want {
		if !seen[k] {
			t.Errorf("example lacks key %s", k)
		}
	}

	// uncomment every "# key = value" line (not banner/prose comments)
	uncomment := regexp.MustCompile(`(?m)^# ([a-z_]+ = )`)
	live := uncomment.ReplaceAllString(src, "$1")
	c, err := Parse([]byte(live), "uncommented", Overrides{})
	if err != nil {
		t.Fatalf("uncommented example does not parse: %v\n%s", err, live)
	}
	// the ceremony window, manifest_repo and org defaults are documented as
	// "unset"; everything else must equal the defaults
	want2 := Default()
	want2.Path = "uncommented"
	want2.Reviews.Repos = c.Reviews.Repos
	if c.GitHub != want2.GitHub || c.Reviews.CIRecheckSecs != want2.Reviews.CIRecheckSecs || c.Display != want2.Display {
		t.Errorf("uncommented core differs from Default():\n got %+v\nwant %+v", c, want2)
	}
	if strings.Join(c.Bots.Logins, ",") != strings.Join(want2.Bots.Logins, ",") {
		t.Errorf("bots = %v", c.Bots.Logins)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("uncommented example warns: %v", c.Warnings)
	}
	if c.TerraformApprove != nil {
		t.Error("enabled = false stays in effect")
	}

	// flip the workflows on too, and compare to the workflow defaults
	on := strings.ReplaceAll(live, "enabled = false", "enabled = true")
	c, err = Parse([]byte(on), "on", Overrides{})
	if err != nil {
		t.Fatalf("enabled example does not parse: %v", err)
	}
	ta := DefaultTerraformApprove()
	ta.Repos = c.Reviews.Repos
	ta.CeremonyWindow = c.TerraformApprove.CeremonyWindow // the example shows a window; it is optional
	if c.TerraformApprove.CeremonyWindow == nil || c.TerraformApprove.CeremonyWindow.Timezone != "America/New_York" {
		t.Errorf("example ceremony window = %+v", c.TerraformApprove.CeremonyWindow)
	}
	if got, want := strings.Join(c.TerraformApprove.GateDonePrefixes, "|"), strings.Join(ta.GateDonePrefixes, "|"); got != want {
		t.Errorf("gate_done_prefixes = %q", got)
	}
	mw := DefaultMaintenanceWindow()
	mw.ManifestRepo = c.Reviews.Repos[0]
	if *c.MaintenanceWindow != *mw {
		t.Errorf("maintenance_window defaults differ:\n got %+v\nwant %+v", *c.MaintenanceWindow, *mw)
	}
	rp := DefaultReleasePR()
	rp.Org = "acme"
	rp.Cache = c.ReleasePR.Cache // expanded
	if !reflect.DeepEqual(c.ReleasePR, rp) {
		t.Errorf("release_pr defaults differ:\n got %+v\nwant %+v", *c.ReleasePR, *rp)
	}
	if !strings.HasSuffix(rp.Cache, "release-repos-acme.json") {
		t.Errorf("cache = %q", rp.Cache)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("enabled example warns: %v", c.Warnings)
	}
}

func TestExampleMatchesRepoRoot(t *testing.T) {
	path := filepath.Clean(rootExample)
	if *update {
		if err := os.WriteFile(path, []byte(Example()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(got) != Example() {
		t.Fatalf("%s differs from internal/config/example.toml; run `go test ./internal/config -update`", path)
	}
}
