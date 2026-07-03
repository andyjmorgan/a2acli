package auth

import (
	"fmt"
	"net/http"
)

// Transport is an http.RoundTripper that injects the resolved credential
// into every request. On a 401/403 from a refreshable source (secret_cmd) it
// invalidates the cache, re-resolves, and retries exactly once — expired
// OAuth tokens refresh transparently. Static sources fail immediately: a 401
// there means a wrong key, and retrying would not change the outcome.
type Transport struct {
	Base      http.RoundTripper
	Source    Source // nil => anonymous, no header injected
	Placement Placement
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Source == nil {
		return t.base().RoundTrip(req)
	}

	secret, err := t.Source.Get()
	if err != nil {
		return nil, fmt.Errorf("resolve credential: %w", err)
	}

	// Clone: RoundTrippers must not mutate the caller's request.
	first := req.Clone(req.Context())
	first.Header.Set(t.Placement.Header, t.Placement.Render(secret))

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
		return nil, fmt.Errorf("refresh credential after %d: %w", resp.StatusCode, err)
	}
	retry := req.Clone(req.Context())
	retry.Header.Set(t.Placement.Header, t.Placement.Render(secret))
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
// source and placement. A nil source yields a plain client.
func HTTPClient(src Source, p Placement) *http.Client {
	return &http.Client{Transport: &Transport{Source: src, Placement: p}}
}
