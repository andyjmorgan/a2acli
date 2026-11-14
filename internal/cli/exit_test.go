package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/auth"
)

func TestClassifyExitTypedA2AErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		code     int
		contains string
	}{
		{"task not found", fmt.Errorf("get: %w", a2a.ErrTaskNotFound), exitTaskFailed, "unknown to this agent"},
		{"unauthenticated", a2a.ErrUnauthenticated, exitCredential, ""},
		{"permission denied", a2a.ErrUnauthorized, exitCredential, ""},
		{"version", a2a.ErrVersionNotSupported, exitTaskFailed, "protocol version"},
		{"extension required", a2a.ErrExtensionSupportRequired, exitTaskFailed, "--extension"},
		// A typed server error mentioning "timeout" is the agent's verdict,
		// not a network failure.
		{"typed beats heuristics", &a2a.Error{Err: a2a.ErrInternalError, Message: "upstream timeout"}, exitTaskFailed, ""},
		{"credential sentinel", fmt.Errorf("%w: no secret", auth.ErrCredential), exitCredential, ""},
		{"raw 401", errors.New("unexpected HTTP status: 401 Unauthorized"), exitCredential, ""},
		{"deadline", context.DeadlineExceeded, exitTransport, ""},
		{"refused", errors.New("dial tcp: connection refused"), exitTransport, ""},
		{"other", errors.New("boom"), exitTaskFailed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ee := classifyExit(c.err)
			if ee.code != c.code {
				t.Fatalf("code = %d, want %d (msg %q)", ee.code, c.code, ee.msg)
			}
			if c.contains != "" && !strings.Contains(ee.msg, c.contains) {
				t.Fatalf("msg %q lacks %q", ee.msg, c.contains)
			}
		})
	}
}
