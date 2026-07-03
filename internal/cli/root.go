// Package cli wires the a2acli command tree.
package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/andyjmorgan/a2acli/internal/config"
	"github.com/spf13/cobra"
)

// Options are the global flags shared by all commands.
type Options struct {
	JSON   bool
	Secret string // --secret override: cmd:<line> | name:<name> | literal
}

var opts Options

// NewRoot builds the root command.
func NewRoot(version string) *cobra.Command {
	cliVersion = version
	root := &cobra.Command{
		Use:           "a2acli",
		Short:         "A generic client for A2A agents",
		Long:          "a2acli is a generic, scriptable command-line client for A2A agents:\ndiscovery, auth, message sending, task handles, polling, and cancellation.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			maybeNotifyUpdate(cmd)
		},
	}
	root.PersistentFlags().BoolVar(&opts.JSON, "json", false, "print a normalized JSON object instead of human output")
	root.PersistentFlags().StringVar(&opts.Secret, "secret", "", "one-time credential override: cmd:<line>, name:<name>, or a literal")

	var sf skillFlags
	root.Flags().BoolVar(&sf.Skill, "skill", false, "print the bundled agent skill (SKILL.md) to stdout")
	root.Flags().BoolVar(&sf.Claude, "claude", false, "with --skill: create/update the skill in ~/.claude/skills/a2acli/")
	root.Flags().BoolVar(&sf.Codex, "codex", false, "with --skill: create/update the skill in ~/.codex/skills/a2acli/")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if sf.Claude || sf.Codex {
			sf.Skill = true // --claude/--codex imply --skill
		}
		if sf.Skill {
			return runSkill(sf)
		}
		return cmd.Help()
	}

	root.AddCommand(newAgentCmd(), newCardCmd(), newSendCmd(), newTaskCmd(), newUpdateCmd(), newUpdateCheckHiddenCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	root := NewRoot(version)
	if err := root.Execute(); err != nil {
		var ee *exitError
		if ok := asExitError(err, &ee); ok {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, "a2acli: "+ee.msg)
			}
			return ee.code
		}
		fmt.Fprintln(os.Stderr, "a2acli: "+err.Error())
		return 1
	}
	return 0
}

// exitError carries a specific exit code out of a command (task watch uses
// this for failed/cancelled terminal states).
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func asExitError(err error, target **exitError) bool {
	for err != nil {
		if ee, ok := err.(*exitError); ok {
			*target = ee
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// printJSON writes one normalized object to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
