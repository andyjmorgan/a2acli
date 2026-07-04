package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

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
