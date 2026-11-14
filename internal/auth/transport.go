package auth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
// Credentials are scoped by origin: when Origins is non-empty they are only
// attached to requests whose normalized scheme://host:port is listed. So
// artifact downloads or redirects to third-party hosts never receive the
// agent's credential, nor does a plaintext http:// URL on an https agent's
// host (an HTTPS->HTTP downgrade).
type Transport struct {
	Base      http.RoundTripper
	Source    Source // nil => anonymous, no header injected
	Placement Placement
	Origins   []string // normalized via Origin; empty => every origin (unscoped)
}

// Origin normalizes a URL to "scheme://host:port": lowercase scheme and
// host, IPv6 brackets preserved, default ports (http 80, https 443) made
// explicit so "https://h" and "https://h:443" compare equal. ok=false when
// the URL has no scheme or host.
func Origin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	return originOf(u)
}

func originOf(u *url.URL) (string, bool) {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if scheme == "" || host == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port), true
}

func (t *Transport) inScope(req *http.Request) bool {
	if len(t.Origins) == 0 {
		return true
	}
	o, ok := originOf(req.URL)
	if !ok {
		return false
	}
	for _, allowed := range t.Origins {
		if allowed == o {
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
// source and placement, scoped to the origins of the given URLs (none =
// unscoped; unparseable URLs are skipped, never widening scope). A nil
// source yields a plain client.
func HTTPClient(src Source, p Placement, scopeURLs ...string) *http.Client {
	var origins []string
	for _, u := range scopeURLs {
		if o, ok := Origin(u); ok {
			origins = append(origins, o)
		}
	}
	if len(scopeURLs) > 0 && len(origins) == 0 {
		// Scope was requested but nothing parsed: fail closed rather than
		// silently falling back to unscoped.
		origins = []string{"invalid://"}
	}
	return &http.Client{Transport: &Transport{Source: src, Placement: p, Origins: origins}}
}
