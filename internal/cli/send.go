package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/normalize"
	"github.com/spf13/cobra"
)

func newSendCmd() *cobra.Command {
	var (
		contextID         string
		taskID            string
		returnImmediately bool
		wait              int
		input             inputFlags
	)
	cmd := &cobra.Command{
		Use:   "send <alias-or-url> <message>",
		Short: "Send a message (non-streaming SendMessage)",
		Long: `Send a message. --context-id continues a conversation; --task-id continues
an existing task — the way to answer a task paused in input-required or
auth-required (exit codes 3/4 carry the agent's question).

--file and --data attach file and structured-data parts after the text
(pass "" as the message to send only attachments); --accept declares the
output media types you can handle.

Exit codes: 0 message/completed/busy/still-working snapshot; 1 task failed or
cancelled; 3 input-required; 4 auth-required; 5 network failure; 6 credential
failure.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return classifyExit(err)
			}
			defer conn.Close()

			sendOpts := client.SendOptions{
				ContextID:         contextID,
				TaskID:            taskID,
				ReturnImmediately: returnImmediately,
			}
			if err := input.apply(&sendOpts); err != nil {
				return err
			}
			res, err := conn.Send(cmd.Context(), args[1], sendOpts)
			if err != nil {
				return classifyExit(err)
			}

			// --wait: if we got a live task handle, poll until terminal,
			// paused, or deadline. Poll failures are surfaced, never
			// swallowed: a stale snapshot presented as the result is a lie.
			if task, ok := res.(*a2a.Task); ok && wait > 0 {
				state := normalize.State(task.Status.State)
				if !normalize.Terminal(state) && !normalize.Paused(state) {
					t, perr := pollTask(cmd.Context(), conn, string(task.ID), pollOpts{
						interval:    time.Second,
						timeout:     time.Duration(wait) * time.Second,
						stopOnPause: true,
					})
					switch {
					case perr == nil, perr == errPollDeadline:
						res = t // deadline with a live task is a truthful non-terminal snapshot
					case cmd.Context().Err() != nil:
						fmt.Fprintln(os.Stderr, "interrupted while waiting; task keeps running")
						if t != nil {
							res = t
						}
					default:
						return classifyExit(perr)
					}
				}
			}

			recordResult(args[0], conn, res)
			if opts.JSON {
				if err := printJSON(normalize.SendResult(res)); err != nil {
					return err
				}
			} else {
				printSendHuman(res, args[0])
			}
			if task, ok := res.(*a2a.Task); ok {
				return taskOutcome(string(task.ID), normalize.State(task.Status.State), normalize.TaskText(task), false)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&contextID, "context-id", "", "continue an existing context")
	cmd.Flags().StringVar(&taskID, "task-id", "", "continue an existing task (answer input-required / refine)")
	cmd.Flags().BoolVar(&returnImmediately, "return-immediately", false, "ask the server to return a task handle without waiting")
	cmd.Flags().IntVar(&wait, "wait", 0, "if a task handle is returned, poll up to this many seconds for completion")
	input.register(cmd)
	return cmd
}

// printSendHuman renders the design note's human output. Busy envelopes are
// printed exactly as returned (they are Agentlings affordances, not A2A).
func printSendHuman(res a2a.SendMessageResult, target string) {
	switch v := res.(type) {
	case *a2a.Message:
		fmt.Println(normalize.Truncate(normalize.MessageText(v)))
	case *a2a.Task:
		state := normalize.State(v.Status.State)
		text := normalize.Truncate(normalize.TaskText(v))
		switch {
		case normalize.Terminal(state):
			if text != "" {
				fmt.Println(text)
				return
			}
			fmt.Printf("Task %s %s in context %s.\n", v.ID, state, v.ContextID)
		case normalize.Paused(state):
			fmt.Printf("Task %s is %s in context %s.\n", v.ID, state, v.ContextID)
			if text != "" {
				fmt.Printf("\n%s\n\n", text)
			}
			fmt.Printf("Answer: a2acli send %s \"<answer>\" --task-id %s\n", target, v.ID)
		default:
			fmt.Printf("Task %s is %s in context %s.\n", v.ID, state, v.ContextID)
			fmt.Printf("Watch: a2acli task watch %s %s\n", target, v.ID)
			fmt.Printf("Get:   a2acli task get %s %s\n", target, v.ID)
		}
	}
}
