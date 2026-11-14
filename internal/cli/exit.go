package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/auth"
)

// Exit codes are the CLI's branchable outcome contract for scripts and
// agents. An orchestrator's next action should be a switch on these, never a
// parse of stderr.
//
//	0  success: final message, completed task, or requested snapshot delivered
//	1  the delegated task ended failed/cancelled/rejected (or a generic error)
//	3  task paused input-required: the agent asked a question — answer with
//	   `send --task-id`; the question text is on stdout/JSON
//	4  task paused auth-required
//	5  observation failed: network/timeout/protocol error after retries; the
//	   remote task may well still be running
//	6  credential failure: secret unresolvable or authentication rejected
const (
	exitOK            = 0
	exitTaskFailed    = 1
	exitInputRequired = 3
	exitAuthRequired  = 4
	exitTransport     = 5
	exitCredential    = 6
)

// taskOutcome maps a normalized terminal-or-paused state to the exit
// contract. nil means the state is not an exit condition (or completed).
func taskOutcome(taskID, state, text string, noFail bool) error {
	switch state {
	case "input-required":
		msg := fmt.Sprintf("task %s needs input", taskID)
		if text != "" {
			msg += ": " + text
		}
		return &exitError{code: exitInputRequired, msg: msg}
	case "auth-required":
		msg := fmt.Sprintf("task %s needs authentication", taskID)
		if text != "" {
			msg += ": " + text
		}
		return &exitError{code: exitAuthRequired, msg: msg}
	case "failed", "cancelled", "rejected":
		if noFail {
			return nil
		}
		return &exitError{code: exitTaskFailed, msg: fmt.Sprintf("task %s ended %s", taskID, state)}
	}
	return nil
}

// classifyExit maps an arbitrary error onto the exit contract. Explicit
// exitErrors pass through; credential and transport failures get their
// dedicated codes; anything else stays 1.
func classifyExit(err error) *exitError {
	var ee *exitError
	if asExitError(err, &ee) {
		return ee
	}
	if errors.Is(err, auth.ErrCredential) {
		return &exitError{code: exitCredential, msg: err.Error()}
	}
	// Typed A2A protocol errors are authoritative: classify them before any
	// string heuristics, so e.g. a server error whose text mentions "timeout"
	// is not misreported as a network failure.
	if ee := classifyA2AError(err); ee != nil {
		return ee
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "403") || strings.Contains(lower, "forbidden") {
		return &exitError{code: exitCredential, msg: msg}
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &netErr),
		errors.Is(err, os.ErrDeadlineExceeded),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "unexpected eof"):
		return &exitError{code: exitTransport, msg: msg}
	}
	return &exitError{code: exitTaskFailed, msg: msg}
}

// a2aHints add an actionable next step to protocol errors an orchestrator
// can do something about.
var a2aHints = []struct {
	target error
	hint   string
}{
	{a2a.ErrTaskNotFound, "the task id is unknown to this agent (wrong agent, typo, or expired)"},
	{a2a.ErrTaskNotCancelable, "the task is already terminal or the agent does not allow cancelling it"},
	{a2a.ErrUnsupportedOperation, "the agent does not support this operation (or the task is already terminal)"},
	{a2a.ErrVersionNotSupported, "the agent does not speak this client's A2A protocol version"},
	{a2a.ErrExtensionSupportRequired, "the agent requires an A2A extension: activate it with --extension <uri> (see `a2acli card`)"},
	{a2a.ErrExtendedCardNotConfigured, "the agent advertises an extended card but has none configured"},
	{a2a.ErrUnsupportedContentType, "the agent does not accept this content type (check the card's input/output modes)"},
	{a2a.ErrPushNotificationNotSupported, "the agent does not support push notifications"},
	{a2a.ErrMethodNotFound, "the agent does not implement this A2A method"},
}

// definitiveErrors are protocol answers that retrying cannot change. Only
// these stop a poll loop early; everything else — including typed internal
// or server errors, which are often transient — is retried with backoff.
var definitiveErrors = []error{
	a2a.ErrTaskNotFound,
	a2a.ErrUnauthenticated,
	a2a.ErrUnauthorized,
	a2a.ErrVersionNotSupported,
	a2a.ErrExtensionSupportRequired,
	a2a.ErrUnsupportedOperation,
	a2a.ErrMethodNotFound,
	a2a.ErrInvalidParams,
	auth.ErrCredential,
}

// isDefinitive reports whether err is a final answer not worth retrying.
func isDefinitive(err error) bool {
	for _, target := range definitiveErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// classifyA2AError maps typed A2A errors from the SDK onto the exit contract.
// Authentication errors are credential failures (6); every other protocol
// error is a definitive answer from the agent (1) — retrying will not help,
// so it must not masquerade as a transport failure (5).
func classifyA2AError(err error) *exitError {
	if errors.Is(err, a2a.ErrUnauthenticated) || errors.Is(err, a2a.ErrUnauthorized) {
		return &exitError{code: exitCredential, msg: err.Error()}
	}
	for _, h := range a2aHints {
		if errors.Is(err, h.target) {
			return &exitError{code: exitTaskFailed, msg: err.Error() + " (" + h.hint + ")"}
		}
	}
	var a2aErr *a2a.Error
	if errors.As(err, &a2aErr) {
		return &exitError{code: exitTaskFailed, msg: err.Error()}
	}
	return nil
}
