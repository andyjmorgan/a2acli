package cli

import (
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/spf13/cobra"
)

func newCardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "card <alias-or-url>",
		Short: "Fetch and display an agent's Agent Card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			card, err := client.ResolveCard(cmd.Context(), args[0], cfg)
			if err != nil {
				return err
			}
			if opts.JSON {
				return printJSON(card)
			}
			printCard(card)
			return nil
		},
	}
}

func printCard(card *a2a.AgentCard) {
	fmt.Printf("%s (version %s)\n", card.Name, card.Version)
	if card.Description != "" {
		fmt.Printf("  %s\n", card.Description)
	}
	for _, iface := range card.SupportedInterfaces {
		fmt.Printf("  interface: %s %s\n", iface.ProtocolBinding, iface.URL)
	}
	fmt.Printf("  streaming: %v\n", card.Capabilities.Streaming)
	for name, scheme := range card.SecuritySchemes {
		fmt.Printf("  security: %s (%s)\n", name, schemeKind(scheme))
	}
	for _, skill := range card.Skills {
		fmt.Printf("  skill: %s — %s\n", skill.Name, skill.Description)
	}
}

func schemeKind(s a2a.SecurityScheme) string {
	switch v := s.(type) {
	case a2a.APIKeySecurityScheme:
		return fmt.Sprintf("apiKey in %s %q", v.Location, v.Name)
	case *a2a.APIKeySecurityScheme:
		return fmt.Sprintf("apiKey in %s %q", v.Location, v.Name)
	case a2a.HTTPAuthSecurityScheme:
		return "http " + v.Scheme
	case *a2a.HTTPAuthSecurityScheme:
		return "http " + v.Scheme
	case a2a.OpenIDConnectSecurityScheme, *a2a.OpenIDConnectSecurityScheme:
		return "openIdConnect"
	case a2a.OAuth2SecurityScheme, *a2a.OAuth2SecurityScheme:
		return "oauth2"
	case a2a.MutualTLSSecurityScheme, *a2a.MutualTLSSecurityScheme:
		return "mutualTLS"
	default:
		return "unknown"
	}
}
