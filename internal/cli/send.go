package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/normalize"
	"github.com/spf13/cobra"
)

func newSendCmd() *cobra.Command {
	var (
		contextID         string
		returnImmediately bool
		wait              int
	)
	cmd := &cobra.Command{
		Use:   "send <alias-or-url> <message>",
		Short: "Send a message (non-streaming message/send)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			conn, err := client.Connect(cmd.Context(), args[0], cfg, opts.Secret)
			if err != nil {
				return err
			}
			defer conn.Close()

			res, err := conn.Send(cmd.Context(), args[1], client.SendOptions{
				ContextID:         contextID,
				ReturnImmediately: returnImmediately,
			})
			if err != nil {
				return err
			}

			// --wait: if we got a task handle, poll until terminal or deadline.
			if task, ok := res.(*a2a.Task); ok && wait > 0 && !normalize.Terminal(normalize.State(task.Status.State)) {
				if t, err := pollUntil(cmd.Context(), conn, string(task.ID), time.Duration(wait)*time.Second); err == nil {
					res = t
				}
			}

			if opts.JSON {
				return printJSON(normalize.SendResult(res))
			}
			printSendHuman(res, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&contextID, "context-id", "", "continue an existing context")
	cmd.Flags().BoolVar(&returnImmediately, "return-immediately", false, "ask the server to return a task handle without waiting")
	cmd.Flags().IntVar(&wait, "wait", 0, "if a task handle is returned, poll up to this many seconds for completion")
	return cmd
}

// pollUntil polls a task until terminal state or the deadline, returning the
// last observed task.
func pollUntil(ctx context.Context, conn *client.Conn, taskID string, d time.Duration) (*a2a.Task, error) {
	deadline := time.Now().Add(d)
	var last *a2a.Task
	for {
		t, err := conn.GetTask(ctx, taskID)
		if err != nil {
			if last != nil {
				return last, nil
			}
			return nil, err
		}
		last = t
		if normalize.Terminal(normalize.State(t.Status.State)) || time.Now().After(deadline) {
			return t, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// printSendHuman renders the design note's human output. Busy envelopes are
// printed exactly as returned (they are Agentlings affordances, not A2A).
func printSendHuman(res a2a.SendMessageResult, target string) {
	switch v := res.(type) {
	case *a2a.Message:
		fmt.Println(normalize.MessageText(v))
	case *a2a.Task:
		state := normalize.State(v.Status.State)
		if normalize.Terminal(state) {
			if text := normalize.TaskText(v); text != "" {
				fmt.Println(text)
				return
			}
			fmt.Printf("Task %s %s in context %s.\n", v.ID, state, v.ContextID)
			return
		}
		fmt.Printf("Task %s is %s in context %s.\n", v.ID, state, v.ContextID)
		fmt.Printf("Watch: a2acli task watch %s %s\n", target, v.ID)
		fmt.Printf("Get:   a2acli task get %s %s\n", target, v.ID)
	}
}
