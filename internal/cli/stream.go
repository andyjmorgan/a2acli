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
		outPath   string
		interval  int
	)
	cmd := &cobra.Command{
		Use:   "stream <alias-or-url> <message>",
		Short: "Send a message and stream events as JSONL (message/stream)",
		Long: `Send a message over the A2A streaming transport and print one normalized
JSON line per event to stdout (task handle, status updates — including
extension metadata such as Agentlings tool progress — artifacts, and the
final result). --out additionally appends the same lines to a file.

If the agent's card does not advertise streaming, the command falls back to
non-streaming send plus polling, emitting equivalent JSONL snapshots (a note
goes to stderr). Ctrl-C stops observing; it never cancels the remote task.

Exit codes: 0 when the stream ends in a final message or a completed task;
1 when the task ends failed/cancelled/rejected.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return err
			}
			defer conn.Close()

			out, closeOut, err := openOut(outPath)
			if err != nil {
				return err
			}
			defer closeOut()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			if !conn.SupportsStreaming() {
				fmt.Fprintln(os.Stderr, "note: agent does not advertise streaming; falling back to send + polling")
				return streamViaPolling(ctx, conn, args[1], contextID, interval, out)
			}

			events := conn.Stream(ctx, args[1], client.SendOptions{ContextID: contextID})
			_, err = emitStream(ctx, events, out)
			return err
		},
	}
	cmd.Flags().StringVar(&contextID, "context-id", "", "continue an existing context")
	cmd.Flags().StringVar(&outPath, "out", "", "also append JSONL events to this file")
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds (fallback mode only)")
	return cmd
}

func newTaskSubscribeCmd() *cobra.Command {
	var (
		outPath  string
		interval int
	)
	cmd := &cobra.Command{
		Use:   "subscribe <alias-or-url> <task-id>",
		Short: "Re-attach to a running task's event stream (tasks/subscribe)",
		Long: `Subscribe to a task's event stream and print one normalized JSON line per
event. Falls back to polling (equivalent to task watch --json) when the agent
does not advertise streaming. Ctrl-C stops observing without cancelling.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return err
			}
			defer conn.Close()

			out, closeOut, err := openOut(outPath)
			if err != nil {
				return err
			}
			defer closeOut()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			if !conn.SupportsStreaming() {
				fmt.Fprintln(os.Stderr, "note: agent does not advertise streaming; falling back to polling")
				return pollTaskAsStream(ctx, conn, args[1], interval, out)
			}
			n, err := emitStream(ctx, conn.Subscribe(ctx, args[1]), out)
			if err != nil || n > 0 {
				return err
			}
			// Some servers end a subscription to an already-terminal task
			// without yielding anything; never finish silently — emit one
			// final snapshot instead.
			t, gerr := conn.GetTask(ctx, args[1])
			if gerr != nil {
				return gerr
			}
			ev := normalize.Stream(t, time.Now())
			if err := emitLine(ev, out); err != nil {
				return err
			}
			if normalize.Terminal(ev.Status) && ev.Status != "completed" {
				return &exitError{code: 1, msg: "task ended " + ev.Status}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "also append JSONL events to this file")
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds (fallback mode only)")
	return cmd
}

func openOut(path string) (*os.File, func(), error) {
	if path == "" {
		return nil, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// emitLine writes one normalized event to stdout (and --out when set).
func emitLine(ev normalize.StreamEvent, out *os.File) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	if out != nil {
		if _, err := out.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// emitStream drains a protocol event stream to JSONL, tracking the outcome
// for the exit code. It returns the number of events emitted.
func emitStream(ctx context.Context, events iter.Seq2[a2a.Event, error], out *os.File) (int, error) {
	finalStatus := ""
	sawMessage := false
	count := 0
	for ev, err := range events {
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "stream interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
				return count, nil
			}
			return count, err
		}
		nev := normalize.Stream(ev, time.Now())
		if err := emitLine(nev, out); err != nil {
			return count, err
		}
		count++
		switch nev.Event {
		case "message":
			sawMessage = true
		case "task", "status_update":
			if nev.Status != "" {
				finalStatus = nev.Status
			}
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "stream interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
		return count, nil
	}
	if sawMessage || finalStatus == "completed" || finalStatus == "" {
		return count, nil
	}
	if normalize.Terminal(finalStatus) {
		return count, &exitError{code: 1, msg: "task ended " + finalStatus}
	}
	return count, nil
}

// streamViaPolling emulates the stream contract on non-streaming servers:
// send, then emit JSONL snapshots on every observed state transition.
func streamViaPolling(ctx context.Context, conn *client.Conn, text, contextID string, interval int, out *os.File) error {
	res, err := conn.Send(ctx, text, client.SendOptions{ContextID: contextID})
	if err != nil {
		return err
	}
	now := time.Now()
	switch v := res.(type) {
	case *a2a.Message:
		return emitLine(normalize.Stream(v, now), out)
	case *a2a.Task:
		if err := emitLine(normalize.Stream(v, now), out); err != nil {
			return err
		}
		return pollTaskAsStream(ctx, conn, string(v.ID), interval, out)
	}
	return nil
}

// pollTaskAsStream polls a task and emits stream-shaped JSONL on each state
// transition until terminal.
func pollTaskAsStream(ctx context.Context, conn *client.Conn, taskID string, interval int, out *os.File) error {
	lastState := ""
	for {
		t, err := conn.GetTask(ctx, taskID)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
				return nil
			}
			return err
		}
		state := normalize.State(t.Status.State)
		if state != lastState {
			lastState = state
			ev := normalize.Stream(t, time.Now())
			ev.Event = "status_update"
			if err := emitLine(ev, out); err != nil {
				return err
			}
		}
		if normalize.Terminal(state) {
			if state != "completed" {
				return &exitError{code: 1, msg: "task ended " + state}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
			return nil
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
}
