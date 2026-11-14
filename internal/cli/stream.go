package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"os/signal"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/normalize"
	"github.com/spf13/cobra"
)

func newStreamCmd() *cobra.Command {
	var (
		contextID string
		taskID    string
		outPath   string
		interval  int
		timeout   int
		input     inputFlags
	)
	cmd := &cobra.Command{
		Use:   "stream <alias-or-url> <message>",
		Short: "Send a message and stream events as JSONL (SendStreamingMessage)",
		Long: `Send a message over the A2A streaming transport and print one normalized
JSON line per event to stdout (task handle, status updates — including
extension metadata such as Agentlings tool progress — artifacts, and the
final result). --out additionally appends the same lines to a file.

If the agent's card does not advertise streaming, the command falls back to
send plus polling, emitting equivalent JSONL. If a live stream drops before
the task settles, observation continues transparently via polling — a
network blip never turns into a false verdict. Ctrl-C stops observing; it
never cancels the remote task.

Exit codes: 0 final message/completed/busy; 1 failed/cancelled/rejected;
3 input-required (answer with send --task-id); 4 auth-required; 5 network
failure or --timeout; 6 credential failure.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			iv, err := validateInterval(interval)
			if err != nil {
				return err
			}
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return classifyExit(err)
			}
			defer conn.Close()

			out, closeOut, err := openOut(outPath)
			if err != nil {
				return err
			}
			defer closeOut()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}

			sendOpts := client.SendOptions{ContextID: contextID, TaskID: taskID}
			if err := input.apply(&sendOpts); err != nil {
				return err
			}
			if !conn.SupportsStreaming() {
				fmt.Fprintln(os.Stderr, "note: agent does not advertise streaming; falling back to send + polling")
				return streamViaPolling(ctx, args[0], conn, args[1], sendOpts, iv, out)
			}

			st := &streamState{target: args[0], conn: conn, out: out, interval: iv}
			err = st.drain(ctx, conn.Stream(ctx, args[1], sendOpts))
			return st.finish(ctx, err)
		},
	}
	cmd.Flags().StringVar(&contextID, "context-id", "", "continue an existing context")
	cmd.Flags().StringVar(&taskID, "task-id", "", "continue an existing task (answer input-required / refine)")
	cmd.Flags().StringVar(&outPath, "out", "", "also append JSONL events to this file")
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds (fallback/reconnect mode)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "stop observing after this many seconds (0 = no limit)")
	input.register(cmd)
	return cmd
}

func newTaskSubscribeCmd() *cobra.Command {
	var (
		outPath  string
		interval int
		timeout  int
	)
	cmd := &cobra.Command{
		Use:   "subscribe [alias-or-url] <task-id>",
		Short: "Re-attach to a running task's event stream (SubscribeToTask)",
		Long: `Subscribe to a task's event stream and print one normalized JSON line per
event. Falls back to polling when the agent does not advertise streaming,
and reconnects via polling if a live stream drops. Ctrl-C stops observing
without cancelling. Exit codes match stream.

` + taskArgsUse,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, taskID, err := resolveTaskArgs(args)
			if err != nil {
				return err
			}
			iv, err := validateInterval(interval)
			if err != nil {
				return err
			}
			conn, err := connectTo(cmd, target)
			if err != nil {
				return classifyExit(err)
			}
			defer conn.Close()

			out, closeOut, err := openOut(outPath)
			if err != nil {
				return err
			}
			defer closeOut()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}

			st := &streamState{target: target, conn: conn, out: out, interval: iv, taskID: taskID}
			if !conn.SupportsStreaming() {
				fmt.Fprintln(os.Stderr, "note: agent does not advertise streaming; falling back to polling")
				return st.finish(ctx, st.pollRemainder(ctx))
			}
			err = st.drain(ctx, conn.Subscribe(ctx, taskID))
			if err == nil && st.count == 0 {
				// Some servers end a subscription to an already-terminal task
				// without yielding anything; never finish silently — emit one
				// final snapshot instead.
				err = st.pollRemainder(ctx)
			}
			return st.finish(ctx, err)
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "also append JSONL events to this file")
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds (fallback/reconnect mode)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "stop observing after this many seconds (0 = no limit)")
	return cmd
}

func openOut(path string) (*os.File, func(), error) {
	if path == "" {
		return nil, func() {}, nil
	}
	// 0600: streamed task output can be as sensitive as anything in config.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// streamState tracks one observation run across a live stream and any
// polling continuation, so the final exit code reflects what actually
// happened to the task — not merely how the bytes stopped flowing.
type streamState struct {
	target   string
	conn     *client.Conn
	out      *os.File
	interval time.Duration

	taskID     string
	contextID  string
	lastStatus string
	sawBusy    bool
	sawFinal   bool // saw a final Message event (immediate-answer path)
	count      int
}

// emit writes one normalized event line and folds it into the state.
func (s *streamState) emit(ev normalize.StreamEvent) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	if s.out != nil {
		if _, err := s.out.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	s.count++
	switch ev.Event {
	case "busy":
		s.sawBusy = true
	case "message":
		s.sawFinal = true
	case "task", "status_update":
		if ev.TaskID != "" {
			s.taskID = ev.TaskID
		}
		if ev.ContextID != "" {
			s.contextID = ev.ContextID
		}
		if ev.Status != "" {
			s.lastStatus = ev.Status
			// A message before a terminal status must not mask the verdict.
			s.sawFinal = false
		}
	case "artifact_update":
		if ev.TaskID != "" {
			s.taskID = ev.TaskID
		}
	}
	return nil
}

// drain consumes a protocol event stream.
func (s *streamState) drain(ctx context.Context, events iter.Seq2[a2a.Event, error]) error {
	for ev, err := range events {
		if err != nil {
			return err
		}
		nev := normalize.Stream(ev, time.Now())
		if err := s.emit(nev); err != nil {
			return err
		}
		if t, ok := ev.(*a2a.Task); ok {
			recordTask(s.target, s.conn, t)
		}
	}
	return nil
}

// pollRemainder continues observation over GetTask, emitting
// status_update-shaped lines for every transition (same shape as the
// polling fallback, so consumers see one contract).
func (s *streamState) pollRemainder(ctx context.Context) error {
	if s.taskID == "" {
		return fmt.Errorf("stream ended before a task handle was received")
	}
	t, perr := pollTask(ctx, s.conn, s.taskID, pollOpts{
		interval:    s.interval,
		stopOnPause: true,
		onSnapshot: func(t *a2a.Task, changed bool) error {
			state := normalize.State(t.Status.State)
			if !changed && state == s.lastStatus {
				return nil
			}
			recordTask(s.target, s.conn, t)
			ev := normalize.Stream(t, time.Now())
			ev.Event = "status_update"
			return s.emit(ev)
		},
	})
	if perr != nil && ctx.Err() == nil {
		return perr
	}
	_ = t
	return nil
}

// finish decides the run's outcome. A dropped live stream continues via
// polling; only after that does the state map to an exit code.
func (s *streamState) finish(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if context.Cause(ctx) == context.DeadlineExceeded || ctx.Err() == context.DeadlineExceeded {
			return &exitError{code: exitTransport, msg: "observation timed out; the task was NOT cancelled"}
		}
		fmt.Fprintln(os.Stderr, "stream interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
		return nil
	}
	if err != nil {
		switch {
		case normalize.Terminal(s.lastStatus) || normalize.Paused(s.lastStatus):
			// The task already settled; a trailing transport hiccup on
			// stream close must not overwrite the verdict.
			fmt.Fprintf(os.Stderr, "note: stream close error after task settled: %v\n", err)
		case s.taskID != "":
			// A live-stream failure with a known task is a blip, not a verdict.
			fmt.Fprintf(os.Stderr, "stream dropped (%v); continuing via polling\n", err)
			if perr := s.pollRemainder(ctx); perr != nil {
				return classifyExit(perr)
			}
			return s.finish(ctx, nil)
		default:
			return classifyExit(err)
		}
	}

	switch {
	case s.lastStatus == "" && (s.sawFinal || s.sawBusy):
		return nil // immediate answer or busy backpressure: delivered as emitted
	case s.lastStatus == "":
		return &exitError{code: exitTransport, msg: "stream ended without any task state"}
	case normalize.Terminal(s.lastStatus), normalize.Paused(s.lastStatus):
		return taskOutcome(s.taskID, s.lastStatus, "", false)
	default:
		// Stream closed cleanly but the task is still running: keep observing.
		fmt.Fprintln(os.Stderr, "stream ended before the task settled; continuing via polling")
		if perr := s.pollRemainder(ctx); perr != nil {
			return classifyExit(perr)
		}
		return taskOutcome(s.taskID, s.lastStatus, "", false)
	}
}

// streamViaPolling emulates the stream contract on non-streaming servers:
// send, then emit JSONL on every observed state transition.
func streamViaPolling(ctx context.Context, target string, conn *client.Conn, text string, sendOpts client.SendOptions, interval time.Duration, out *os.File) error {
	res, err := conn.Send(ctx, text, sendOpts)
	if err != nil {
		return classifyExit(err)
	}
	st := &streamState{target: target, conn: conn, out: out, interval: interval}
	switch v := res.(type) {
	case *a2a.Message:
		if err := st.emit(normalize.Stream(v, time.Now())); err != nil {
			return err
		}
		return st.finish(ctx, nil)
	case *a2a.Task:
		recordTask(target, conn, v)
		ev := normalize.Stream(v, time.Now())
		ev.Event = "status_update"
		if err := st.emit(ev); err != nil {
			return err
		}
		if normalize.Terminal(st.lastStatus) || normalize.Paused(st.lastStatus) {
			return st.finish(ctx, nil)
		}
		if err := st.pollRemainder(ctx); err != nil && ctx.Err() == nil {
			return classifyExit(err)
		}
		return st.finish(ctx, nil)
	}
	return nil
}
