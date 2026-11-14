package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrCredential marks failures that are about the credential itself (not the
// network): unresolvable secrets, failed refreshes. The CLI maps it to a
// dedicated exit code so an agent can branch "fix my auth" vs "retry later".
var ErrCredential = errors.New("credential error")

// Transport is an http.RoundTripper that injects the resolved credential
// into every request. On a 401/403 from a refreshable source (secret_cmd) it
// invalidates the cache, re-resolves, and retries exactly once — expired
// OAuth tokens refresh transparently. Static sources fail immediately: a 401
// there means a wrong key, and retrying would not change the outcome.
//
// Credentials are scoped: when Hosts is non-empty they are only attached to
// requests whose host (host[:port]) is listed — so artifact downloads or
// redirects to third-party hosts never receive the agent's credential.
type Transport struct {
	Base      http.RoundTripper
	Source    Source // nil => anonymous, no header injected
	Placement Placement
	Hosts     []string // empty => every host (unscoped)
}

func (t *Transport) inScope(req *http.Request) bool {
	if len(t.Hosts) == 0 {
		return true
	}
	for _, h := range t.Hosts {
		if strings.EqualFold(h, req.URL.Host) {
			return true
		}
	}
	return false
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Source == nil || !t.inScope(req) {
		return t.base().RoundTrip(req)
	}

	secret, err := t.Source.Get()
	if err != nil {
		return nil, fmt.Errorf("%w: resolve: %w", ErrCredential, err)
	}

	// Clone: RoundTrippers must not mutate the caller's request.
	first := req.Clone(req.Context())
	t.Placement.apply(first, secret)

	resp, err := t.base().RoundTrip(first)
	if err != nil {
		return nil, err
	}

	retriable := resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
	if !retriable || !t.Source.Refreshable() || req.GetBody == nil && req.Body != nil {
		return resp, nil
	}

	// Refresh and retry once. The server rejected the request without
	// processing it, so a replay is safe.
	resp.Body.Close()
	t.Source.Invalidate()
	secret, err = t.Source.Get()
	if err != nil {
		return nil, fmt.Errorf("%w: refresh after %d: %w", ErrCredential, resp.StatusCode, err)
	}
	retry := req.Clone(req.Context())
	t.Placement.apply(retry, secret)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		retry.Body = body
	}
	return t.base().RoundTrip(retry)
}

// HTTPClient returns an *http.Client wired with a Transport for the given
// source and placement, scoped to hosts (none = unscoped). A nil source
// yields a plain client.
func HTTPClient(src Source, p Placement, hosts ...string) *http.Client {
	return &http.Client{Transport: &Transport{Source: src, Placement: p, Hosts: hosts}}
}
