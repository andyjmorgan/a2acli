package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/config"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(EnvNoKeyring, "1")
}

func TestEnvVarName(t *testing.T) {
	if got := EnvVarName("prod-agent"); got != "A2ACLI_SECRET_PROD_AGENT" {
		t.Fatalf("got %s", got)
	}
}

func TestWalkEnvVerbatim(t *testing.T) {
	isolate(t)
	t.Setenv("MYKEY", "v1")
	if v, err := WalkSecret("MYKEY"); err != nil || v != "v1" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}

func TestWalkEnvNormalized(t *testing.T) {
	isolate(t)
	t.Setenv("A2ACLI_SECRET_PROD_AGENT", "v2")
	if v, err := WalkSecret("prod-agent"); err != nil || v != "v2" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}

func TestWalkSecretsFile(t *testing.T) {
	isolate(t)
	if err := config.SaveSecretsFile(map[string]string{"prod-agent": "v3"}); err != nil {
		t.Fatal(err)
	}
	if v, err := WalkSecret("prod-agent"); err != nil || v != "v3" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}

func TestWalkMissNamesPlaces(t *testing.T) {
	isolate(t)
	_, err := WalkSecret("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"A2ACLI_SECRET_GHOST", "secrets file"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestWalkPrecedenceEnvOverFile(t *testing.T) {
	isolate(t)
	if err := config.SaveSecretsFile(map[string]string{"k": "file"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2ACLI_SECRET_K", "env")
	if v, _ := WalkSecret("k"); v != "env" {
		t.Fatalf("want env to win, got %q", v)
	}
}

func TestCmdSourceCachesAndInvalidates(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	counter := dir + "/count"
	src := &cmdSource{cmdline: `echo x >> ` + counter + `; wc -l < ` + counter}
	v1, err := src.Get()
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := src.Get()
	if v1 != "1" || v2 != "1" {
		t.Fatalf("expected cached '1', got %q then %q", v1, v2)
	}
	src.Invalidate()
	if v3, _ := src.Get(); v3 != "2" {
		t.Fatalf("expected re-exec '2', got %q", v3)
	}
}

func TestParseOverride(t *testing.T) {
	if _, ok := ParseOverride("cmd:echo x").(*cmdSource); !ok {
		t.Fatal("cmd: not parsed as command")
	}
	if _, ok := ParseOverride("name:foo").(nameSource); !ok {
		t.Fatal("name: not parsed as name")
	}
	if s, ok := ParseOverride("literaltok").(literalSource); !ok || s.value != "literaltok" {
		t.Fatal("bare value not parsed as literal")
	}
}

func TestRenderTemplate(t *testing.T) {
	cases := []struct{ tmpl, want string }{
		{"$secret", "tok"},
		{"Bearer $secret", "Bearer tok"},
		{"Token $secret", "Token tok"},
		{"pre$$fix $secret", "pre$fix tok"},
	}
	for _, c := range cases {
		if got := (Placement{Value: c.tmpl}).Render("tok"); got != c.want {
			t.Errorf("Render(%q) = %q, want %q", c.tmpl, got, c.want)
		}
	}
}

func cardWith(scheme a2a.SecurityScheme) *a2a.AgentCard {
	name := a2a.SecuritySchemeName("main")
	return &a2a.AgentCard{
		SecuritySchemes:      a2a.NamedSecuritySchemes{name: scheme},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{name: {}}},
	}
}

func TestPlacementFromCardAPIKey(t *testing.T) {
	card := cardWith(&a2a.APIKeySecurityScheme{Location: "header", Name: "X-API-Key"})
	p := ResolvePlacement(nil, card)
	if p.Header != "X-API-Key" || p.Value != "$secret" {
		t.Fatalf("got %+v", p)
	}
}

func TestPlacementFromCardHTTPBearer(t *testing.T) {
	card := cardWith(&a2a.HTTPAuthSecurityScheme{Scheme: "bearer"})
	p := ResolvePlacement(nil, card)
	if p.Header != "Authorization" || p.Value != "Bearer $secret" {
		t.Fatalf("got %+v", p)
	}
}

func TestPlacementExplicitWins(t *testing.T) {
	card := cardWith(&a2a.APIKeySecurityScheme{Location: "header", Name: "X-API-Key"})
	p := ResolvePlacement(&config.Auth{Header: "X-Custom", Value: "Token $secret"}, card)
	if p.Header != "X-Custom" || p.Value != "Token $secret" {
		t.Fatalf("got %+v", p)
	}
}

func TestPlacementDefaults(t *testing.T) {
	if p := ResolvePlacement(&config.Auth{Secret: "x"}, nil); p.Header != "X-API-Key" {
		t.Fatalf("static default: %+v", p)
	}
	if p := ResolvePlacement(&config.Auth{SecretCmd: "c"}, nil); p.Header != "Authorization" || p.Value != "Bearer $secret" {
		t.Fatalf("cmd default: %+v", p)
	}
}

// Transport: refreshable source gets one retry after 401; then success.
func TestTransport401RefreshRetry(t *testing.T) {
	isolate(t)
	var issued atomic.Int32
	tokFile := t.TempDir() + "/n"
	src := &cmdSource{cmdline: `echo x >> ` + tokFile + `; printf "tok%s" "$(wc -l < ` + tokFile + ` | tr -d ' ')"`}

	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		got = append(got, h)
		if h != "Bearer tok2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		issued.Add(1)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := HTTPClient(src, Placement{Header: "Authorization", Value: "Bearer $secret"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"x":1}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, headers seen: %v", resp.StatusCode, got)
	}
	if len(got) != 2 || got[0] != "Bearer tok1" || got[1] != "Bearer tok2" {
		t.Fatalf("headers seen: %v", got)
	}
}

// Static sources must NOT retry on 401.
func TestTransportStaticNoRetry(t *testing.T) {
	isolate(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := HTTPClient(literalSource{value: "bad"}, Placement{Header: "X-API-Key", Value: "$secret"})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || hits != 1 {
		t.Fatalf("status=%d hits=%d", resp.StatusCode, hits)
	}
}

func TestPlacementFromCardAPIKeyQueryAndCookie(t *testing.T) {
	p := ResolvePlacement(nil, cardWith(a2a.APIKeySecurityScheme{Location: "query", Name: "api_key"}))
	if p.In != InQuery || p.Header != "api_key" {
		t.Fatalf("query: %+v", p)
	}
	p = ResolvePlacement(nil, cardWith(a2a.APIKeySecurityScheme{Location: "cookie", Name: "sid"}))
	if p.In != InCookie || p.Header != "sid" {
		t.Fatalf("cookie: %+v", p)
	}
}

// Schemes inside one requirement are unordered (a map); resolution must be
// deterministic anyway.
func TestPlacementDeterministicWithinRequirement(t *testing.T) {
	card := &a2a.AgentCard{
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"b-key":  a2a.APIKeySecurityScheme{Location: "header", Name: "X-B"},
			"a-key":  a2a.APIKeySecurityScheme{Location: "header", Name: "X-A"},
			"c-http": a2a.HTTPAuthSecurityScheme{Scheme: "bearer"},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{"c-http": {}, "b-key": {}, "a-key": {}}},
	}
	for range 20 {
		if p := ResolvePlacement(nil, card); p.Header != "X-A" {
			t.Fatalf("non-deterministic placement: %+v", p)
		}
	}
}

func TestTransportQueryAndCookiePlacement(t *testing.T) {
	var gotQuery, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("api_key")
		if c, err := r.Cookie("sid"); err == nil {
			gotCookie = c.Value
		}
	}))
	defer srv.Close()

	resp, err := HTTPClient(literalSource{value: "q1"}, Placement{In: InQuery, Header: "api_key", Value: "$secret"}).Get(srv.URL + "/a2a?x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotQuery != "q1" {
		t.Fatalf("query credential = %q", gotQuery)
	}
	resp, err = HTTPClient(literalSource{value: "c1"}, Placement{In: InCookie, Header: "sid", Value: "$secret"}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotCookie != "c1" {
		t.Fatalf("cookie credential = %q", gotCookie)
	}
}

// Credentials go only to the agent's hosts: never to third-party artifact
// URLs, and never across a redirect to another host.
func TestTransportScopesCredentialsToHosts(t *testing.T) {
	var thirdPartySaw string
	third := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		thirdPartySaw = r.Header.Get("X-API-Key")
	}))
	defer third.Close()
	var agentSaw string
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentSaw = r.Header.Get("X-API-Key")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, third.URL+"/file", http.StatusFound)
		}
	}))
	defer agent.Close()

	agentHost := strings.TrimPrefix(agent.URL, "http://")
	c := HTTPClient(literalSource{value: "sekrit"}, Placement{Header: "X-API-Key", Value: "$secret"}, agentHost)

	for _, u := range []string{agent.URL + "/a2a", third.URL + "/artifact.bin", agent.URL + "/redirect"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if agentSaw != "sekrit" {
		t.Fatalf("agent host did not receive the credential: %q", agentSaw)
	}
	if thirdPartySaw != "" {
		t.Fatalf("credential leaked to a third-party host: %q", thirdPartySaw)
	}
}
