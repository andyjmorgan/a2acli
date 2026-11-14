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
