package cli

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// skillMD is the a2acli agent skill, bundled at build time so any AI harness
// that finds the binary can bootstrap usage instructions matching this exact
// CLI surface — no network, no pre-installed skill.
// Canonical source: internal/cli/skill/SKILL.md.
//
//go:embed skill/SKILL.md
var skillMD string

// skillFlags are the root-level --skill / --claude / --codex flags:
//
//	a2acli --skill           print the bundled SKILL.md to stdout
//	a2acli --skill --claude  create/update ~/.claude/skills/a2acli/SKILL.md
//	a2acli --skill --codex   create/update ~/.codex/skills/a2acli/SKILL.md
type skillFlags struct {
	Skill  bool
	Claude bool
	Codex  bool
}

// runSkill implements the --skill behaviors. Install targets are
// create-or-update: the on-disk skill is kept in lockstep with the binary.
func runSkill(f skillFlags) error {
	if !f.Claude && !f.Codex {
		_, err := fmt.Fprint(os.Stdout, skillMD)
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locate home directory: %w", err)
	}
	var targets []string
	if f.Claude {
		targets = append(targets, filepath.Join(home, ".claude", "skills", "a2acli", "SKILL.md"))
	}
	if f.Codex {
		targets = append(targets, filepath.Join(home, ".codex", "skills", "a2acli", "SKILL.md"))
	}
	for _, dest := range targets {
		if err := installSkill(dest); err != nil {
			return err
		}
	}
	return nil
}

// installSkill writes the bundled skill to dest (create or update), atomically
// so a reader never observes a half-written file. Messages go to stderr.
func installSkill(dest string) error {
	want := []byte(skillMD)
	existing, err := os.ReadFile(dest) //nolint:gosec // program-controlled profile path
	switch {
	case err == nil:
		if bytes.Equal(existing, want) {
			fmt.Fprintf(os.Stderr, "skill already up to date: %s\n", dest)
			return nil
		}
	case errors.Is(err, os.ErrNotExist):
		// fresh install
	default:
		return fmt.Errorf("read %s: %w", dest, err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("create skill directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".skill-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once the rename succeeds
	if _, err := f.Write(want); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	fmt.Fprintf(os.Stderr, "installed skill → %s\n", dest)
	return nil
}
