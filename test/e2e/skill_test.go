package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillEchoesToStdout(t *testing.T) {
	c := newCLI(t)
	r := c.mustRun("--skill")
	if !strings.HasPrefix(r.stdout, "---\nname: a2acli\n") {
		t.Fatalf("skill output missing frontmatter:\n%.120s", r.stdout)
	}
	for _, want := range []string{"agents describe", "task watch", "--secret", "context_busy"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestSkillInstallClaudeAndCodex(t *testing.T) {
	c := newCLI(t)
	home := t.TempDir()
	c.setenv("HOME=" + home)

	// --claude / --codex imply --skill; both in one invocation.
	r := c.mustRun("--skill", "--claude", "--codex")
	if !strings.Contains(r.stderr, "installed skill") {
		t.Fatalf("stderr: %s", r.stderr)
	}

	skill := c.mustRun("--skill").stdout
	for _, p := range []string{
		filepath.Join(home, ".claude", "skills", "a2acli", "SKILL.md"),
		filepath.Join(home, ".codex", "skills", "a2acli", "SKILL.md"),
	} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("skill not installed at %s: %v", p, err)
		}
		if string(got) != skill {
			t.Fatalf("installed skill at %s differs from bundled copy", p)
		}
	}

	// Re-run: idempotent, reports up-to-date.
	r = c.mustRun("--claude")
	if !strings.Contains(r.stderr, "already up to date") {
		t.Fatalf("second install stderr: %s", r.stderr)
	}

	// Drifted file gets updated back into lockstep.
	claudePath := filepath.Join(home, ".claude", "skills", "a2acli", "SKILL.md")
	if err := os.WriteFile(claudePath, []byte("stale local edits"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = c.mustRun("--skill", "--claude")
	if !strings.Contains(r.stderr, "installed skill") {
		t.Fatalf("update stderr: %s", r.stderr)
	}
	got, _ := os.ReadFile(claudePath)
	if string(got) != skill {
		t.Fatal("drifted skill was not updated")
	}
}
