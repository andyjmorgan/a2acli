package e2e

import (
	"strings"
	"testing"

	"github.com/andyjmorgan/a2acli/internal/mocka2a"
)

func TestAgentsDescribe(t *testing.T) {
	srv1 := mocka2a.New(mocka2a.WithName("alpha-agent")).Start()
	defer srv1.Close()
	srv2 := mocka2a.New(mocka2a.WithName("beta-agent")).Start()
	defer srv2.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "alpha", srv1.URL)
	c.mustRun("agent", "add", "beta", srv2.URL)
	// One dead agent must not fail the enumeration.
	c.mustRun("agent", "add", "dead", "http://127.0.0.1:1")

	// The exact spelling the command was designed for.
	r := c.mustRun("agents", "describe")
	for _, want := range []string{"alpha-agent", "beta-agent", "UNREACHABLE"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("describe missing %q:\n%s", want, r.stdout)
		}
	}

	r = c.mustRun("agents", "describe", "--json")
	entries := decode[[]map[string]any](t, r.stdout)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	byAlias := map[string]map[string]any{}
	for _, e := range entries {
		byAlias[e["alias"].(string)] = e
	}
	if byAlias["alpha"]["name"] != "alpha-agent" || byAlias["beta"]["name"] != "beta-agent" {
		t.Fatalf("entries: %v", entries)
	}
	if byAlias["dead"]["error"] == nil || byAlias["dead"]["error"] == "" {
		t.Fatalf("dead agent should carry an error: %v", byAlias["dead"])
	}
}

func TestDescribeTruncatesLongDescriptions(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithName("wordy")).Start()
	defer srv.Close()
	c := newCLI(t)
	c.mustRun("agent", "add", "wordy", srv.URL)

	r := c.mustRun("agents", "describe", "--json")
	entries := decode[[]map[string]any](t, r.stdout)
	for _, e := range entries {
		if d, ok := e["description"].(string); ok && len([]rune(d)) > 140 {
			t.Fatalf("description not truncated: %d runes", len([]rune(d)))
		}
	}
}

// A dev build must refuse to self-update (and must not hit the network).
func TestUpdateDevBuildNoop(t *testing.T) {
	c := newCLI(t)
	r := c.mustRun("update")
	if !strings.Contains(r.stderr, "built from source") {
		t.Fatalf("dev update stderr: %s", r.stderr)
	}
	r = c.mustRun("update", "--check")
	if !strings.Contains(r.stderr, "built from source") {
		t.Fatalf("dev update --check stderr: %s", r.stderr)
	}
}

// The passive notice must never leak onto stdout or fire for JSON pipelines
// (stderr is not a terminal in tests, so it must stay completely silent).
func TestPassiveNoticeSilentWhenNotTTY(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)
	r := c.mustRun("send", srv.URL, "echo:x", "--json")
	if strings.Contains(r.stdout, "newer version") || strings.Contains(r.stderr, "newer version") {
		t.Fatalf("passive notice fired without a TTY: %s %s", r.stdout, r.stderr)
	}
}
