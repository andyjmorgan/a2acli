package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/normalize"
	"github.com/spf13/cobra"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect, watch, and cancel tasks",
	}
	cmd.AddCommand(newTaskGetCmd(), newTaskWatchCmd(), newTaskCancelCmd())
	return cmd
}

func connectTo(cmd *cobra.Command, target string) (*client.Conn, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return client.Connect(cmd.Context(), target, cfg, opts.Secret)
}

func newTaskGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <alias-or-url> <task-id>",
		Short: "Fetch current task state",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return err
			}
			defer conn.Close()
			t, err := conn.GetTask(cmd.Context(), args[1])
			if err != nil {
				return err
			}
			v := normalize.Task(t)
			if opts.JSON {
				return printJSON(v)
			}
			fmt.Printf("%s %s context=%s\n", v.TaskID, v.Status, v.ContextID)
			if normalize.Terminal(v.Status) && v.Text != "" {
				fmt.Printf("\n%s\n", v.Text)
			}
			return nil
		},
	}
}

func newTaskCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <alias-or-url> <task-id>",
		Short: "Request cancellation of a task",
		Long: `Request cancellation. Servers may not expose an intermediate "cancelling"
state; the returned state can still be non-terminal. Confirm terminal
cancellation with: a2acli task watch <target> <task-id>`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return err
			}
			defer conn.Close()
			t, err := conn.CancelTask(cmd.Context(), args[1])
			if err != nil {
				return err
			}
			v := normalize.Task(t)
			if opts.JSON {
				return printJSON(v)
			}
			fmt.Printf("Cancellation requested: %s %s context=%s\n", v.TaskID, v.Status, v.ContextID)
			if !normalize.Terminal(v.Status) {
				fmt.Printf("Confirm with: a2acli task watch %s %s\n", args[0], v.TaskID)
			}
			return nil
		},
	}
}

func newTaskWatchCmd() *cobra.Command {
	var (
		interval int
		outPath  string
		noFail   bool
	)
	cmd := &cobra.Command{
		Use:   "watch <alias-or-url> <task-id>",
		Short: "Poll a task until it reaches a terminal state",
		Long: `Poll a task until terminal (completed / failed / cancelled / rejected).

State transitions are printed as they happen. With --out, a normalized JSONL
snapshot is appended for every transition (including the first and last
observation). Ctrl-C stops watching but does NOT cancel the remote task.

Exit codes: 0 on completed; 1 on failed/cancelled/rejected (suppress with
--no-fail-on-task-failure).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectTo(cmd, args[0])
			if err != nil {
				return err
			}
			defer conn.Close()

			var out *os.File
			if outPath != "" {
				out, err = os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
				if err != nil {
					return err
				}
				defer out.Close()
			}

			// Ctrl-C stops observing only; it must never cancel the task.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			started := time.Now()
			lastState := ""
			for {
				t, err := conn.GetTask(ctx, args[1])
				if err != nil {
					if ctx.Err() != nil {
						fmt.Fprintln(os.Stderr, "watch interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
						return nil
					}
					return err
				}
				v := normalize.Task(t)

				if v.Status != lastState {
					lastState = v.Status
					elapsed := int64(time.Since(started).Seconds())
					if opts.JSON {
						snap := normalize.TaskSnapshot(t, started, time.Now())
						line, _ := json.Marshal(snap)
						fmt.Println(string(line))
					} else {
						fmt.Printf("%s %s context=%s elapsed=%ds\n", v.TaskID, v.Status, v.ContextID, elapsed)
					}
					if out != nil {
						snap := normalize.TaskSnapshot(t, started, time.Now())
						line, _ := json.Marshal(snap)
						if _, err := out.Write(append(line, '\n')); err != nil {
							return fmt.Errorf("write %s: %w", outPath, err)
						}
					}
				}

				if normalize.Terminal(v.Status) {
					if !opts.JSON && v.Text != "" {
						fmt.Printf("\n%s\n", v.Text)
					}
					if v.Status != "completed" && !noFail {
						return &exitError{code: 1, msg: fmt.Sprintf("task %s ended %s", v.TaskID, v.Status)}
					}
					return nil
				}

				select {
				case <-ctx.Done():
					fmt.Fprintln(os.Stderr, "watch interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
					return nil
				case <-time.After(time.Duration(interval) * time.Second):
				}
			}
		},
	}
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds")
	cmd.Flags().StringVar(&outPath, "out", "", "append JSONL snapshots to this file")
	cmd.Flags().BoolVar(&noFail, "no-fail-on-task-failure", false, "exit 0 even if the task fails or is cancelled")
	return cmd
}
