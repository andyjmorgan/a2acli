package cli

import (
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/spf13/cobra"
)

func newCardCmd() *cobra.Command {
	var extended bool
	cmd := &cobra.Command{
		Use:   "card <alias-or-url>",
		Short: "Fetch and display an agent's Agent Card",
		Long: `Fetch and display an agent's public Agent Card.

--extended fetches the authenticated extended card (A2A GetExtendedAgentCard)
using the agent's configured credentials — agents may reveal extra skills
there. Requires the card to advertise capabilities.extendedAgentCard.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			var card *a2a.AgentCard
			if extended {
				card, err = fetchExtendedCard(cmd, args[0])
			} else {
				card, err = client.ResolveCard(cmd.Context(), args[0], cfg)
			}
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
	cmd.Flags().BoolVar(&extended, "extended", false, "fetch the authenticated extended card (GetExtendedAgentCard)")
	return cmd
}

func fetchExtendedCard(cmd *cobra.Command, target string) (*a2a.AgentCard, error) {
	conn, err := connectTo(cmd, target)
	if err != nil {
		return nil, classifyExit(err)
	}
	defer conn.Close()
	if conn.Card == nil || !conn.Card.Capabilities.ExtendedAgentCard {
		return nil, fmt.Errorf("agent %s does not advertise an extended card (capabilities.extendedAgentCard)", target)
	}
	card, err := conn.ExtendedCard(cmd.Context())
	if err != nil {
		return nil, classifyExit(err)
	}
	return card, nil
}

func printCard(card *a2a.AgentCard) {
	fmt.Printf("%s (version %s)\n", card.Name, card.Version)
	if card.Description != "" {
		fmt.Printf("  %s\n", card.Description)
	}
	for _, iface := range card.SupportedInterfaces {
		fmt.Printf("  interface: %s %s\n", iface.ProtocolBinding, iface.URL)
	}
	caps := card.Capabilities
	fmt.Printf("  streaming: %v  push: %v  extended card: %v\n", caps.Streaming, caps.PushNotifications, caps.ExtendedAgentCard)
	for _, ext := range caps.Extensions {
		req := ""
		if ext.Required {
			req = " (required)"
		}
		fmt.Printf("  extension: %s%s\n", ext.URI, req)
	}
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
