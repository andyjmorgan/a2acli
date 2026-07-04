package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/normalize"
)

// errPollDeadline marks an overall --timeout / --wait deadline expiring with
// the task still running: not a failure, the caller decides what to report.
var errPollDeadline = errors.New("poll deadline reached")

// maxPollRetries is how many consecutive GetTask failures the engine absorbs
// (with exponential backoff) before giving up. One blip must never kill a
// long watch; a dead server must not hang it forever.
const maxPollRetries = 4

// pollOpts shape one poll run.
type pollOpts struct {
	interval time.Duration
	timeout  time.Duration // 0 = no overall deadline
	// onSnapshot fires on every successful poll; changed marks a state
	// transition (including the first observation).
	onSnapshot func(t *a2a.Task, changed bool) error
	// stopOnPause ends the run when the task pauses (input/auth-required)
	// instead of polling a paused task forever.
	stopOnPause bool
}

// pollTask polls until the task is terminal (or paused, when stopOnPause),
// retrying transient errors with backoff. It returns the last observed task.
// A ctx cancellation returns (last, ctx.Err()); an expired opts.timeout
// returns (last, errPollDeadline).
func pollTask(ctx context.Context, conn *client.Conn, taskID string, o pollOpts) (*a2a.Task, error) {
	if o.interval < time.Second {
		o.interval = time.Second
	}
	var deadline time.Time
	if o.timeout > 0 {
		deadline = time.Now().Add(o.timeout)
	}

	var last *a2a.Task
	lastState := ""
	failures := 0
	for {
		t, err := conn.GetTask(ctx, taskID)
		switch {
		case err != nil && ctx.Err() != nil:
			return last, ctx.Err()
		case err != nil:
			failures++
			if failures > maxPollRetries {
				return last, fmt.Errorf("polling task %s: giving up after %d consecutive failures: %w", taskID, failures, err)
			}
			backoff := time.Duration(1<<(failures-1)) * time.Second
			backoff += time.Duration(rand.Int63n(int64(backoff) / 2)) //nolint:gosec // jitter, not crypto
			fmt.Fprintf(os.Stderr, "poll error (attempt %d/%d, retrying in %s): %v\n", failures, maxPollRetries+1, backoff.Round(time.Millisecond), err)
			if !sleepCtx(ctx, backoff) {
				return last, ctx.Err()
			}
			continue
		}
		failures = 0
		last = t

		state := normalize.State(t.Status.State)
		changed := state != lastState
		lastState = state
		if o.onSnapshot != nil {
			if err := o.onSnapshot(t, changed); err != nil {
				return last, err
			}
		}

		if normalize.Terminal(state) || (o.stopOnPause && normalize.Paused(state)) {
			return t, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return t, errPollDeadline
		}
		if !sleepCtx(ctx, o.interval) {
			return last, ctx.Err()
		}
	}
}

// sleepCtx sleeps unless the context ends first; false means interrupted.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// validateInterval guards the shared --interval flag: a zero/negative value
// would hot-loop against the remote agent.
func validateInterval(seconds int) (time.Duration, error) {
	if seconds < 1 {
		return 0, fmt.Errorf("--interval must be >= 1 second (got %d)", seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}
