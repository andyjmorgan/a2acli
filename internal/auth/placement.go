package auth

import (
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/config"
)

// Placement is the header half of auth: which header, and a value template
// containing "$secret" ("$$" escapes a literal dollar).
type Placement struct {
	Header string
	Value  string
}

// Render substitutes the secret into the value template.
func (p Placement) Render(secret string) string {
	const marker = "\x00literal-dollar\x00"
	v := strings.ReplaceAll(p.Value, "$$", marker)
	v = strings.ReplaceAll(v, "$secret", secret)
	return strings.ReplaceAll(v, marker, "$")
}

// titleScheme normalizes an RFC 7235 scheme word for the Authorization
// header ("bearer" => "Bearer").
func titleScheme(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// fromCard derives placement from an Agent Card's declared security schemes.
// It walks the card's security requirements in order and returns the first
// scheme it can express as a header. Query/cookie API keys and full OAuth
// flows are not expressible; those fall through.
func fromCard(card *a2a.AgentCard) (Placement, bool) {
	if card == nil || len(card.SecuritySchemes) == 0 {
		return Placement{}, false
	}
	// Preserve requirement order where declared; fall back to iterating all
	// declared schemes if the card lists schemes but no requirements.
	var names []a2a.SecuritySchemeName
	for _, req := range card.SecurityRequirements {
		for name := range req {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		for name := range card.SecuritySchemes {
			names = append(names, name)
		}
	}
	for _, name := range names {
		// The SDK yields value types from JSON parsing but pointer types are
		// common in hand-built cards; accept both.
		switch s := card.SecuritySchemes[name].(type) {
		case a2a.APIKeySecurityScheme:
			if p, ok := apiKeyPlacement(s); ok {
				return p, true
			}
		case *a2a.APIKeySecurityScheme:
			if p, ok := apiKeyPlacement(*s); ok {
				return p, true
			}
		case a2a.HTTPAuthSecurityScheme:
			if p, ok := httpPlacement(s); ok {
				return p, true
			}
		case *a2a.HTTPAuthSecurityScheme:
			if p, ok := httpPlacement(*s); ok {
				return p, true
			}
		case a2a.OpenIDConnectSecurityScheme, *a2a.OpenIDConnectSecurityScheme,
			a2a.OAuth2SecurityScheme, *a2a.OAuth2SecurityScheme:
			return Placement{Header: "Authorization", Value: "Bearer $secret"}, true
		}
	}
	return Placement{}, false
}

func apiKeyPlacement(s a2a.APIKeySecurityScheme) (Placement, bool) {
	if s.Location == "header" && s.Name != "" {
		return Placement{Header: s.Name, Value: "$secret"}, true
	}
	return Placement{}, false
}

func httpPlacement(s a2a.HTTPAuthSecurityScheme) (Placement, bool) {
	if s.Scheme != "" {
		return Placement{Header: "Authorization", Value: titleScheme(s.Scheme) + " $secret"}, true
	}
	return Placement{}, false
}

// ResolvePlacement decides where the secret goes: explicit config wins, then
// the Agent Card's declared schemes, then a default chosen by source shape
// (command sources look like tokens => Bearer; named/static => X-API-Key).
func ResolvePlacement(a *config.Auth, card *a2a.AgentCard) Placement {
	if a != nil && (a.Header != "" || a.Value != "") {
		p := Placement{Header: a.Header, Value: a.Value}
		if p.Header == "" {
			p.Header = "Authorization"
		}
		if p.Value == "" {
			p.Value = "$secret"
		}
		return p
	}
	if p, ok := fromCard(card); ok {
		return p
	}
	if a != nil && a.SecretCmd != "" {
		return Placement{Header: "Authorization", Value: "Bearer $secret"}
	}
	return Placement{Header: "X-API-Key", Value: "$secret"}
}
