// Package client is the thin adapter between the CLI and the a2a-go SDK.
// It owns target resolution (alias / base URL / card URL / direct endpoint),
// card discovery with fallbacks, and construction of an authenticated
// a2aclient.Client. Command code never imports the SDK's client directly.
package client

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2aext"
	"github.com/andyjmorgan/a2acli/internal/auth"
	"github.com/andyjmorgan/a2acli/internal/config"
)

const (
	wellKnownCard   = "/.well-known/agent-card.json"
	wellKnownLegacy = "/.well-known/agent.json"

	// discoveryTimeout bounds each card-probe request.
	discoveryTimeout = 8 * time.Second
)

// Conn is an established connection to one agent.
type Conn struct {
	Target   string         // what the user typed (alias or URL)
	URL      string         // resolved base or endpoint URL
	Card     *a2a.AgentCard // nil when discovery failed and we fell back to a direct endpoint
	Client   *a2aclient.Client
	HTTP     *http.Client // the authenticated client (artifact URI fetches reuse it)
	AuthUsed bool
}

// Close releases transport resources.
func (c *Conn) Close() {
	if c.Client != nil {
		_ = c.Client.Destroy()
	}
}

// ToolProgressExtensionURI is the Agentlings tool-progress extension. The
// CLI understands its metadata (it rides along verbatim in stream events),
// so it is activated by default whenever an agent advertises it.
const ToolProgressExtensionURI = "https://donkeywork.dev/a2a/extensions/tool-progress/v1"

// DefaultExtensions are activated for every agent that advertises them.
var DefaultExtensions = []string{ToolProgressExtensionURI}

// ConnectOptions tune one connection.
type ConnectOptions struct {
	// Secret is the --secret override ("" = use config).
	Secret string
	// Extensions are extension URIs to activate in addition to
	// DefaultExtensions and the agent's configured list. Per the A2A spec
	// they are sent in the A2A-Extensions header, and only when the agent's
	// card advertises them.
	Extensions []string
}

// Connect resolves a target (alias from cfg, or any URL form) into a working
// client.
func Connect(ctx context.Context, target string, cfg *config.Config, o ConnectOptions) (*Conn, error) {
	url := target
	var authCfg *config.Auth
	exts := append(append([]string(nil), DefaultExtensions...), o.Extensions...)

	if cfg != nil {
		if ag, ok := cfg.Agents[target]; ok {
			url = ag.URL
			authCfg = ag.Auth
			exts = append(exts, ag.Extensions...)
		}
	}
	if !strings.Contains(url, "://") {
		if url == target {
			return nil, fmt.Errorf("unknown agent %q: not an alias in config and not a URL", target)
		}
		return nil, fmt.Errorf("agent %q has invalid url %q", target, url)
	}

	card, base := discoverCard(ctx, url)

	src, err := auth.SourceFor(authCfg, o.Secret)
	if err != nil {
		return nil, err
	}
	placement := auth.ResolvePlacement(authCfg, card)
	httpClient := auth.HTTPClient(src, placement, credentialScope(base, card)...)

	// Register both HTTP transports; the SDK picks per the card's declared
	// interfaces, so REST-only agents work too. (gRPC agents remain out of
	// scope until demand shows up.)
	exts = dedupe(exts)
	transports := []a2aclient.FactoryOption{
		a2aclient.WithJSONRPCTransport(httpClient),
		a2aclient.WithRESTTransport(httpClient),
		a2aclient.WithCallInterceptors(a2aext.NewActivator(exts...)),
	}
	warnRequiredExtensions(card, exts)

	var sdk *a2aclient.Client
	switch {
	case card != nil:
		sdk, err = a2aclient.NewFromCard(ctx, card, transports...)
	default:
		// Discovery failed: treat the supplied URL as the A2A endpoint itself.
		fmt.Fprintf(os.Stderr, "note: no agent card at %s; assuming it is a JSON-RPC A2A endpoint\n", base)
		endpoint := a2a.NewAgentInterface(base, a2a.TransportProtocolJSONRPC)
		sdk, err = a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{endpoint}, transports...)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", base, err)
	}

	return &Conn{Target: target, URL: base, Card: card, Client: sdk, HTTP: httpClient, AuthUsed: src != nil}, nil
}

// credentialScope lists the URLs whose origins (scheme, host, port) may
// receive the agent credential: the agent's own base URL plus every
// interface its card declares.
func credentialScope(base string, card *a2a.AgentCard) []string {
	urls := []string{base}
	if card != nil {
		for _, iface := range card.SupportedInterfaces {
			if iface != nil && iface.URL != "" {
				urls = append(urls, iface.URL)
			}
		}
	}
	return dedupe(urls)
}

// warnRequiredExtensions tells the user, before the request fails, which
// required extensions the agent demands that this invocation won't activate.
func warnRequiredExtensions(card *a2a.AgentCard, active []string) {
	if card == nil {
		return
	}
	for _, ext := range card.Capabilities.Extensions {
		if ext.Required && !slices.Contains(active, ext.URI) {
			fmt.Fprintf(os.Stderr, "note: agent requires extension %s; activate it with --extension %s (or extensions = [...] in config) if you support it\n", ext.URI, ext.URI)
		}
	}
}

func dedupe(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := list[:0:0]
	for _, v := range list {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ResolveCard fetches only the agent card for a target (the `card` command).
func ResolveCard(ctx context.Context, target string, cfg *config.Config) (*a2a.AgentCard, error) {
	url := target
	if cfg != nil {
		if ag, ok := cfg.Agents[target]; ok {
			url = ag.URL
		}
	}
	if !strings.Contains(url, "://") {
		return nil, fmt.Errorf("unknown agent %q: not an alias in config and not a URL", target)
	}
	card, _ := discoverCard(ctx, url)
	if card == nil {
		return nil, fmt.Errorf("no agent card found at %s (%s, %s)", url, wellKnownCard, wellKnownLegacy)
	}
	return card, nil
}

// discoverCard implements the resolution order from the design note:
// explicit card URL => well-known agent-card.json => well-known agent.json.
// It returns the card (nil on total failure) and the base/endpoint URL to use.
func discoverCard(ctx context.Context, url string) (*a2a.AgentCard, string) {
	// Bounded per probe: discovery tries up to three URLs sequentially and a
	// black-holed host must not hang the CLI before it even fails over.
	resolver := &agentcard.Resolver{Client: &http.Client{Timeout: discoveryTimeout}}

	// Explicit card URL: split base and path.
	if i := strings.Index(url, "/.well-known/"); i >= 0 {
		base, path := url[:i], url[i:]
		card, err := resolver.Resolve(ctx, base, agentcard.WithPath(path))
		if err == nil {
			return card, base
		}
		return nil, base
	}

	base := strings.TrimRight(url, "/")
	if card, err := resolver.Resolve(ctx, base); err == nil {
		return card, base
	}
	if card, err := resolver.Resolve(ctx, base, agentcard.WithPath(wellKnownLegacy)); err == nil {
		return card, base
	}

	// A direct endpoint like http://host/a2a: try card discovery at the root.
	if i := strings.Index(base, "://"); i >= 0 {
		if j := strings.Index(base[i+3:], "/"); j >= 0 {
			root := base[:i+3+j]
			if card, err := resolver.Resolve(ctx, root); err == nil {
				return card, root
			}
		}
	}
	return nil, base
}
