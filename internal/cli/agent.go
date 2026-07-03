package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/andyjmorgan/a2acli/internal/auth"
	"github.com/andyjmorgan/a2acli/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage local agent aliases",
	}
	cmd.AddCommand(newAgentAddCmd(), newAgentListCmd(), newAgentShowCmd(), newAgentRemoveCmd(), newAgentSetKeyCmd())
	return cmd
}

func newAgentAddCmd() *cobra.Command {
	var (
		authHeader string
		authValue  string
	)
	cmd := &cobra.Command{
		Use:   "add <alias> <url>",
		Short: "Register an agent alias",
		Long: `Register an agent alias in ~/.config/a2acli/config.toml.

Credentials use --secret with a source reference:
  --secret name:<name>   walk env => keyring => 0600 secrets file for <name>
  --secret cmd:"<line>"  execute <line> per request; stdout is the secret
  --secret <literal>     store the literal in the 0600 secrets file under the alias name (testing only)

Placement defaults come from the discovered Agent Card; override with
--auth-header and --auth-value (a template containing $secret).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias, url := args[0], args[1]
			if !strings.Contains(url, "://") {
				return fmt.Errorf("%q does not look like a URL", url)
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			ag := config.Agent{URL: url}
			var authCfg config.Auth
			switch {
			case strings.HasPrefix(opts.Secret, "name:"):
				authCfg.Secret = strings.TrimPrefix(opts.Secret, "name:")
			case strings.HasPrefix(opts.Secret, "cmd:"):
				authCfg.SecretCmd = strings.TrimPrefix(opts.Secret, "cmd:")
			case opts.Secret != "":
				// Literal: keep it out of the main config — store it in the
				// 0600 secrets file under the alias name and reference it.
				if err := auth.StoreSecretsFile(alias, opts.Secret); err != nil {
					return fmt.Errorf("store literal secret: %w", err)
				}
				authCfg.Secret = alias
				fmt.Fprintf(os.Stderr, "warning: literal secret written to 0600 secrets file as %q; prefer name:/cmd: sources\n", alias)
			}
			authCfg.Header = authHeader
			authCfg.Value = authValue
			if authCfg != (config.Auth{}) {
				if err := authCfg.Validate(); err != nil {
					return err
				}
				ag.Auth = &authCfg
			}

			cfg.Agents[alias] = ag
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("Added agent %s -> %s\n", alias, url)
			return nil
		},
	}
	cmd.Flags().StringVar(&authHeader, "auth-header", "", "header carrying the credential (default: derived from Agent Card)")
	cmd.Flags().StringVar(&authValue, "auth-value", "", `header value template, e.g. "Bearer $secret" (default: derived from Agent Card)`)
	return cmd
}

func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if opts.JSON {
				type row struct {
					Alias string `json:"alias"`
					URL   string `json:"url"`
					Auth  string `json:"auth"`
				}
				rows := []row{}
				for _, name := range cfg.AgentNames() {
					rows = append(rows, row{name, cfg.Agents[name].URL, authSummary(cfg.Agents[name].Auth)})
				}
				return printJSON(rows)
			}
			if len(cfg.Agents) == 0 {
				fmt.Println("No agents registered. Add one: a2acli agent add <alias> <url>")
				return nil
			}
			for _, name := range cfg.AgentNames() {
				ag := cfg.Agents[name]
				fmt.Printf("%-16s %-44s auth=%s\n", name, ag.URL, authSummary(ag.Auth))
			}
			return nil
		},
	}
}

// authSummary names the credential source — never the value.
func authSummary(a *config.Auth) string {
	switch {
	case a == nil:
		return "none"
	case a.SecretCmd != "":
		return "cmd: " + a.SecretCmd
	case a.Secret != "":
		return "secret: " + a.Secret
	default:
		return "custom"
	}
}

func newAgentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <alias>",
		Short: "Show one registered agent (source names only, never secret values)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ag, ok := cfg.Agents[args[0]]
			if !ok {
				return fmt.Errorf("no agent named %q", args[0])
			}
			if opts.JSON {
				return printJSON(map[string]any{
					"alias": args[0],
					"url":   ag.URL,
					"auth":  authSummary(ag.Auth),
				})
			}
			fmt.Printf("%s\n  url:  %s\n  auth: %s\n", args[0], ag.URL, authSummary(ag.Auth))
			if ag.Auth != nil && (ag.Auth.Header != "" || ag.Auth.Value != "") {
				fmt.Printf("  placement: %s: %s\n", ag.Auth.Header, ag.Auth.Value)
			}
			return nil
		},
	}
}

func newAgentRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <alias>",
		Short: "Remove a registered agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, ok := cfg.Agents[args[0]]; !ok {
				return fmt.Errorf("no agent named %q", args[0])
			}
			delete(cfg.Agents, args[0])
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("Removed agent %s\n", args[0])
			return nil
		},
	}
}

func newAgentSetKeyCmd() *cobra.Command {
	var (
		name string
		file bool
	)
	cmd := &cobra.Command{
		Use:   "set-key <alias>",
		Short: "Prompt for a secret (no echo) and store it in the OS keyring",
		Long: `Prompt for a secret without echoing it and store it in the OS keyring
(or, with --file, the 0600 secrets file for headless machines). The agent's
config is updated to reference the stored name.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ag, ok := cfg.Agents[alias]
			if !ok {
				return fmt.Errorf("no agent named %q", alias)
			}
			if name == "" {
				name = alias
			}

			fmt.Fprintf(os.Stderr, "Secret for %s (input hidden): ", name)
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return fmt.Errorf("read secret: %w", err)
			}
			secret := strings.TrimSpace(string(raw))
			if secret == "" {
				return fmt.Errorf("empty secret, nothing stored")
			}

			where := "keyring"
			if file {
				err = auth.StoreSecretsFile(name, secret)
				where = "secrets file"
			} else {
				err = auth.StoreKeyring(name, secret)
			}
			if err != nil {
				return fmt.Errorf("store secret in %s: %w", where, err)
			}

			if ag.Auth == nil {
				ag.Auth = &config.Auth{}
			}
			ag.Auth.Secret = name
			ag.Auth.SecretCmd = ""
			cfg.Agents[alias] = ag
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("Stored secret %q in %s; agent %s now references it\n", name, where, alias)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "secret name (default: the alias)")
	cmd.Flags().BoolVar(&file, "file", false, "store in the 0600 secrets file instead of the keyring")
	return cmd
}
