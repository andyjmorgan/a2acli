package auth

import (
	"net/http"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/config"
)

// Placement locations (OpenAPI "in"); the zero value means header.
const (
	InHeader = "header"
	InQuery  = "query"
	InCookie = "cookie"
)

// Placement is the where half of auth: the location (header, query
// parameter or cookie), its name, and a value template containing "$secret"
// ("$$" escapes a literal dollar).
type Placement struct {
	In     string // InHeader (default when empty), InQuery or InCookie
	Header string // the header, query-parameter or cookie name
	Value  string
}

// apply writes the rendered credential into req (which must be a clone).
func (p Placement) apply(req *http.Request, secret string) {
	v := p.Render(secret)
	switch p.In {
	case InQuery:
		q := req.URL.Query()
		q.Set(p.Header, v)
		req.URL.RawQuery = q.Encode()
	case InCookie:
		req.AddCookie(&http.Cookie{Name: p.Header, Value: v})
	default:
		req.Header.Set(p.Header, v)
	}
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
// scheme it can express: API keys (header, query or cookie), HTTP auth, and
// OAuth2/OIDC as a bearer token obtained out of band (secret_cmd). Mutual
// TLS is not expressible and falls through.
func fromCard(card *a2a.AgentCard) (Placement, bool) {
	if card == nil || len(card.SecuritySchemes) == 0 {
		return Placement{}, false
	}
	// Preserve requirement order where declared; fall back to iterating all
	// declared schemes if the card lists schemes but no requirements.
	var names []a2a.SecuritySchemeName
	// Requirements are ordered; schemes within one requirement (and the
	// scheme map itself) are not, so sort those for deterministic results.
	for _, req := range card.SecurityRequirements {
		start := len(names)
		for name := range req {
			names = append(names, name)
		}
		sortNames(names[start:])
	}
	if len(names) == 0 {
		for name := range card.SecuritySchemes {
			names = append(names, name)
		}
		sortNames(names)
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

func sortNames(n []a2a.SecuritySchemeName) {
	sort.Slice(n, func(i, j int) bool { return n[i] < n[j] })
}

func apiKeyPlacement(s a2a.APIKeySecurityScheme) (Placement, bool) {
	if s.Name == "" {
		return Placement{}, false
	}
	switch strings.ToLower(string(s.Location)) {
	case InHeader:
		return Placement{In: InHeader, Header: s.Name, Value: "$secret"}, true
	case InQuery:
		return Placement{In: InQuery, Header: s.Name, Value: "$secret"}, true
	case InCookie:
		return Placement{In: InCookie, Header: s.Name, Value: "$secret"}, true
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
		p := Placement{In: a.In, Header: a.Header, Value: a.Value}
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
