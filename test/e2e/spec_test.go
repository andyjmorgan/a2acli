package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/andyjmorgan/a2acli/internal/mocka2a"
)

// Typed A2A errors are definitive: an unknown task id fails fast with an
// actionable hint instead of retrying with backoff or reading as a network
// failure.
func TestUnknownTaskIsDefinitiveAndFast(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.run("task", "get", srv.URL, "no-such-task")
	if r.code != 1 || !strings.Contains(r.stderr, "unknown to this agent") {
		t.Fatalf("task get unknown: code=%d stderr=%s", r.code, r.stderr)
	}

	start := time.Now()
	r = c.run("task", "wait", srv.URL, "no-such-task", "--interval", "1")
	if r.code != 1 {
		t.Fatalf("task wait unknown: code=%d stderr=%s", r.code, r.stderr)
	}
	if strings.Contains(r.stderr, "retrying") || time.Since(start) > 3*time.Second {
		t.Fatalf("task-not-found must not be retried (took %s): %s", time.Since(start), r.stderr)
	}
}

// Extensions are activated per the spec: sent in A2A-Extensions only when
// the card advertises them; the tool-progress extension is on by default;
// required extensions the caller won't activate produce a clear error.
func TestExtensionActivation(t *testing.T) {
	const custom = "https://example.com/ext/custom/v1"
	m := mocka2a.New(
		mocka2a.WithExtension(mocka2a.ToolProgressExtensionURI, false),
		mocka2a.WithExtension(custom, false),
	)
	srv := m.Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("send", srv.URL, "echo:hi")
	if got := m.LastActivatedExtensions(); len(got) != 1 || got[0] != mocka2a.ToolProgressExtensionURI {
		t.Fatalf("default activation = %v, want only tool-progress", got)
	}
	if v := m.LastProtocolVersion(); v != "1.0" {
		t.Fatalf("A2A-Version = %q, want 1.0", v)
	}

	c.mustRun("send", srv.URL, "echo:hi", "--extension", custom, "--extension", "https://not-advertised.example/x")
	got := strings.Join(m.LastActivatedExtensions(), " ")
	if !strings.Contains(got, custom) || strings.Contains(got, "not-advertised") {
		t.Fatalf("activation = %q: want custom, never unadvertised URIs", got)
	}
}

func TestRequiredExtension(t *testing.T) {
	const req = "https://example.com/ext/required/v1"
	srv := mocka2a.New(mocka2a.WithExtension(req, true)).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.run("send", srv.URL, "echo:hi")
	if r.code != 1 || !strings.Contains(r.stderr, "--extension "+req) {
		t.Fatalf("missing required ext: code=%d stderr=%s", r.code, r.stderr)
	}
	r = c.mustRun("send", srv.URL, "echo:hi", "--extension", req)
	if !strings.Contains(r.stdout, "Mock response to: hi") {
		t.Fatalf("with extension: %s", r.stdout)
	}
}

type taskPage struct {
	Tasks         []taskView `json:"tasks"`
	TotalSize     int        `json:"totalSize"`
	NextPageToken string     `json:"nextPageToken"`
}

// task list --remote uses A2A ListTasks: it sees tasks this machine never
// recorded, filters server-side, and pages.
func TestRemoteTaskList(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	other := newCLI(t) // a different machine: its ledger is not ours
	other.mustRun("send", srv.URL, "task:600:a", "--json", "--context-id", "ctx_a")
	other.mustRun("send", srv.URL, "task:600:b", "--json", "--context-id", "ctx_a")
	other.mustRun("send", srv.URL, "task:600:c", "--json", "--context-id", "ctx_b")

	c := newCLI(t)
	page := decode[taskPage](t, c.mustRun("task", "list", "--remote", srv.URL, "--json").stdout)
	if page.TotalSize != 3 || len(page.Tasks) != 3 {
		t.Fatalf("remote list = %+v", page)
	}

	page = decode[taskPage](t, c.mustRun("task", "list", "--remote", srv.URL, "--context-id", "ctx_a", "--status", "working", "--json").stdout)
	if page.TotalSize != 2 {
		t.Fatalf("filtered list = %+v", page)
	}

	first := decode[taskPage](t, c.mustRun("task", "list", "--remote", srv.URL, "--limit", "2", "--json").stdout)
	if len(first.Tasks) != 2 || first.NextPageToken == "" {
		t.Fatalf("page 1 = %+v", first)
	}
	second := decode[taskPage](t, c.mustRun("task", "list", "--remote", srv.URL, "--limit", "2", "--page-token", first.NextPageToken, "--json").stdout)
	if len(second.Tasks) != 1 || second.NextPageToken != "" {
		t.Fatalf("page 2 = %+v", second)
	}

	if r := c.run("task", "list", "--remote", srv.URL, "--status", "bogus"); r.code == 0 {
		t.Fatal("bogus --status accepted")
	}
	if r := c.run("task", "list", "--status", "working"); r.code == 0 {
		t.Fatal("--status without --remote accepted")
	}
}
