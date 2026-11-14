package client

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// Interop regression tests: the python a2a-sdk (used by Agentlings) emits
// securityRequirements scopes in canonical ProtoJSON form ({} / {"list":[...]}),
// older Go SDKs emitted plain arrays. a2a-go >= v2.8 accepts both natively,
// which retired our own lenient card parser; these pin that behavior.
var parseCard = agentcard.DefaultCardParser

// pythonCard is the securityRequirements shape emitted by the python a2a-sdk
// (captured from a live Agentlings server, 2026-07).
const pythonCard = `{
  "name": "The Decliner",
  "description": "refusal specialist",
  "supportedInterfaces": [
    {"url": "https://decliner.example/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}
  ],
  "version": "0.1.0",
  "capabilities": {"streaming": false, "pushNotifications": false},
  "securitySchemes": {
    "apiKey": {"apiKeySecurityScheme": {"location": "header", "name": "X-API-Key"}}
  },
  "securityRequirements": [
    {"schemes": {"apiKey": {}}}
  ]
}`

const pythonCardWithScopes = `{
  "name": "x", "version": "1", "capabilities": {},
  "supportedInterfaces": [{"url": "https://x/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}],
  "securityRequirements": [{"schemes": {"oauth": {"list": ["read", "write"]}}}]
}`

func TestCardParserAcceptsProtoScopes(t *testing.T) {
	card, err := parseCard([]byte(pythonCard))
	if err != nil {
		t.Fatalf("python-sdk card rejected: %v", err)
	}
	if card.Name != "The Decliner" {
		t.Fatalf("name = %q", card.Name)
	}
	scheme, ok := card.SecuritySchemes["apiKey"].(a2a.APIKeySecurityScheme)
	if !ok || scheme.Name != "X-API-Key" {
		t.Fatalf("scheme not parsed: %#v", card.SecuritySchemes["apiKey"])
	}
	if len(card.SecurityRequirements) != 1 {
		t.Fatalf("requirements = %v", card.SecurityRequirements)
	}
}

func TestCardParserKeepsScopeLists(t *testing.T) {
	card, err := parseCard([]byte(pythonCardWithScopes))
	if err != nil {
		t.Fatal(err)
	}
	scopes := card.SecurityRequirements[0][a2a.SecuritySchemeName("oauth")]
	if len(scopes) != 2 || scopes[0] != "read" {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestCardParserGoShapeStillWorks(t *testing.T) {
	goShape := `{
	  "name": "go", "version": "1", "capabilities": {},
	  "supportedInterfaces": [{"url": "https://x/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}],
	  "securityRequirements": [{"schemes": {"apiKey": []}}]
	}`
	if _, err := parseCard([]byte(goShape)); err != nil {
		t.Fatal(err)
	}
}

func TestCardParserStillRejectsGarbage(t *testing.T) {
	if _, err := parseCard([]byte(`not json`)); err == nil {
		t.Fatal("garbage accepted")
	}
}
