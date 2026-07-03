package client

import (
	"encoding/json"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// lenientCardParser first tries the SDK's strict parser, then retries after
// normalizing known proto-JSON variants.
//
// Interop note: the python a2a-sdk (used by Agentlings) serializes
// securityRequirements scheme scopes in proto-JSON form — an object like
// {} or {"list":["scope"]} — while the Go SDK expects a plain JSON array.
// Both are the same A2A v1.0 card; we accept either.
func lenientCardParser(body []byte) (*a2a.AgentCard, error) {
	card, err := agentcard.DefaultCardParser(body)
	if err == nil {
		return card, nil
	}
	fixed, ok := normalizeProtoScopes(body)
	if !ok {
		return nil, err
	}
	card, err2 := agentcard.DefaultCardParser(fixed)
	if err2 != nil {
		return nil, err // report the original strict error
	}
	return card, nil
}

// normalizeProtoScopes rewrites securityRequirements[].schemes values from
// proto-JSON objects to scope arrays. Returns ok=false when the body isn't
// JSON or has no such requirements (nothing to fix).
func normalizeProtoScopes(body []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, false
	}
	reqs, _ := doc["securityRequirements"].([]any)
	changed := false
	for _, r := range reqs {
		req, _ := r.(map[string]any)
		schemes, _ := req["schemes"].(map[string]any)
		for name, scopes := range schemes {
			obj, isObj := scopes.(map[string]any)
			if !isObj {
				continue
			}
			if list, isList := obj["list"].([]any); isList {
				schemes[name] = list
			} else {
				schemes[name] = []any{}
			}
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	fixed, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return fixed, true
}
