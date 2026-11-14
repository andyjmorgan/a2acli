package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/normalize"
	"github.com/spf13/cobra"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect, watch, wait on, list, and cancel tasks",
	}
	cmd.AddCommand(newTaskGetCmd(), newTaskWatchCmd(), newTaskWaitCmd(), newTaskListCmd(),
		newTaskArtifactsCmd(), newTaskCancelCmd(), newTaskSubscribeCmd())
	return cmd
}

func connectTo(cmd *cobra.Command, target string) (*client.Conn, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return client.Connect(cmd.Context(), target, cfg, connectOptions())
}

// connectOptions maps global flags onto client.ConnectOptions.
func connectOptions() client.ConnectOptions {
	return client.ConnectOptions{Secret: opts.Secret, Extensions: opts.Extensions}
}

// taskArgsUse documents the shared addressing forms.
const taskArgsUse = `Accepts "<alias-or-url> <task-id>" or just "<task-id>" — with one argument the
agent is recalled from the local ledger (unique task-id prefixes work).`

func newTaskGetCmd() *cobra.Command {
	var history int
	cmd := &cobra.Command{
		Use:   "get [alias-or-url] <task-id>",
		Short: "Fetch current task state",
		Long:  "Fetch current task state, including normalized artifacts.\n\n" + taskArgsUse,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, taskID, err := resolveTaskArgs(args)
			if err != nil {
				return err
			}
			conn, err := connectTo(cmd, target)
			if err != nil {
				return classifyExit(err)
			}
			defer conn.Close()
			t, err := conn.GetTaskHistory(cmd.Context(), taskID, history)
			if err != nil {
				return classifyExit(err)
			}
			recordTask(target, conn, t)
			v := normalize.Task(t)
			if opts.JSON {
				return printJSON(v)
			}
			fmt.Printf("%s %s context=%s\n", v.TaskID, v.Status, v.ContextID)
			if v.Text != "" && (normalize.Terminal(v.Status) || normalize.Paused(v.Status)) {
				fmt.Printf("\n%s\n", v.Text)
			}
			printArtifactSummary(v.Artifacts, target, v.TaskID)
			return nil
		},
	}
	cmd.Flags().IntVar(&history, "history", -1, "request this many history messages (server default if unset)")
	return cmd
}

func printArtifactSummary(arts []normalize.ArtifactView, target, taskID string) {
	if len(arts) == 0 {
		return
	}
	fmt.Printf("\nArtifacts (%d):\n", len(arts))
	for _, a := range arts {
		desc := a.Name
		if desc == "" {
			desc = a.ID
		}
		extras := ""
		if len(a.Files) > 0 {
			extras = fmt.Sprintf(" [%d file(s)]", len(a.Files))
		}
		if a.DataParts > 0 {
			extras += fmt.Sprintf(" [%d data part(s)]", a.DataParts)
		}
		fmt.Printf("  - %s%s\n", desc, extras)
	}
	fmt.Printf("Fetch: a2acli task artifacts %s %s --save <dir>\n", target, taskID)
}

func newTaskCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel [alias-or-url] <task-id>",
		Short: "Request cancellation of a task",
		Long: `Request cancellation. Servers may not expose an intermediate "cancelling"
state; the returned state can still be non-terminal. Confirm terminal
cancellation with: a2acli task watch <task-id>

` + taskArgsUse,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, taskID, err := resolveTaskArgs(args)
			if err != nil {
				return err
			}
			conn, err := connectTo(cmd, target)
			if err != nil {
				return classifyExit(err)
			}
			defer conn.Close()
			t, err := conn.CancelTask(cmd.Context(), taskID)
			if err != nil {
				return classifyExit(err)
			}
			recordTask(target, conn, t)
			v := normalize.Task(t)
			if opts.JSON {
				return printJSON(v)
			}
			fmt.Printf("Cancellation requested: %s %s context=%s\n", v.TaskID, v.Status, v.ContextID)
			if !normalize.Terminal(v.Status) {
				fmt.Printf("Confirm with: a2acli task watch %s %s\n", target, v.TaskID)
			}
			return nil
		},
	}
}

func newTaskWatchCmd() *cobra.Command {
	var (
		interval int
		timeout  int
		outPath  string
		noFail   bool
	)
	cmd := &cobra.Command{
		Use:   "watch [alias-or-url] <task-id>",
		Short: "Poll a task until it reaches a terminal or paused state",
		Long: `Poll a task until terminal (completed / failed / cancelled / rejected) or
paused (input-required / auth-required — the agent is waiting on you; answer
with send --task-id).

State transitions are printed as they happen. With --out, a normalized JSONL
snapshot is appended for every transition. Transient poll errors retry with
backoff. Ctrl-C stops watching but does NOT cancel the remote task.

Exit codes: 0 completed; 1 failed/cancelled/rejected (suppress with
--no-fail-on-task-failure); 3 input-required; 4 auth-required; 5 network
failure or --timeout expired.

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

			// Ctrl-C stops observing only; it must never cancel the task.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			started := time.Now()
			onSnapshot := func(t *a2a.Task, changed bool) error {
				if !changed {
					return nil
				}
				recordTask(target, conn, t)
				v := normalize.Task(t)
				snap := normalize.TaskSnapshot(t, started, time.Now())
				if opts.JSON {
					line, _ := json.Marshal(snap)
					fmt.Println(string(line))
				} else {
					fmt.Printf("%s %s context=%s elapsed=%ds\n", v.TaskID, v.Status, v.ContextID, snap.ElapsedSeconds)
				}
				if out != nil {
					line, _ := json.Marshal(snap)
					if _, err := out.Write(append(line, '\n')); err != nil {
						return fmt.Errorf("write %s: %w", outPath, err)
					}
				}
				return nil
			}

			t, perr := pollTask(ctx, conn, taskID, pollOpts{
				interval:    iv,
				timeout:     time.Duration(timeout) * time.Second,
				onSnapshot:  onSnapshot,
				stopOnPause: true,
			})
			switch {
			case ctx.Err() != nil:
				fmt.Fprintln(os.Stderr, "watch interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
				return nil
			case perr == errPollDeadline:
				return &exitError{code: exitTransport, msg: fmt.Sprintf("watch timed out after %ds; task %s still running (it was NOT cancelled)", timeout, taskID)}
			case perr != nil:
				return classifyExit(perr)
			}

			v := normalize.Task(t)
			if !opts.JSON && v.Text != "" {
				fmt.Printf("\n%s\n", v.Text)
			}
			return taskOutcome(v.TaskID, v.Status, v.Text, noFail)
		},
	}
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds (>= 1)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "give up observing after this many seconds (0 = no limit)")
	cmd.Flags().StringVar(&outPath, "out", "", "append JSONL snapshots to this file")
	cmd.Flags().BoolVar(&noFail, "no-fail-on-task-failure", false, "exit 0 even if the task fails or is cancelled")
	return cmd
}

func newTaskWaitCmd() *cobra.Command {
	var (
		interval int
		timeout  int
		noFail   bool
	)
	cmd := &cobra.Command{
		Use:   "wait [alias-or-url] <task-id>",
		Short: "Block until a task settles, then print one final result",
		Long: `Like watch, but silent while the task runs: no transition chatter, just one
final normalized object when the task reaches a terminal or paused state.
Built for AI agents — one line of output instead of a transcript.

Exit codes match watch. On --timeout the last snapshot is printed and the
exit code is 5 (the task keeps running).

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

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			t, perr := pollTask(ctx, conn, taskID, pollOpts{
				interval:    iv,
				timeout:     time.Duration(timeout) * time.Second,
				stopOnPause: true,
			})
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "interrupted; task keeps running (cancel explicitly with: a2acli task cancel)")
				return nil
			}
			if perr != nil && perr != errPollDeadline {
				return classifyExit(perr)
			}
			recordTask(target, conn, t)
			v := normalize.Task(t)
			if opts.JSON {
				if err := printJSON(v); err != nil {
					return err
				}
			} else {
				fmt.Printf("%s %s context=%s\n", v.TaskID, v.Status, v.ContextID)
				if v.Text != "" {
					fmt.Printf("\n%s\n", v.Text)
				}
			}
			if perr == errPollDeadline {
				return &exitError{code: exitTransport, msg: fmt.Sprintf("wait timed out after %ds; task %s still running (it was NOT cancelled)", timeout, taskID)}
			}
			return taskOutcome(v.TaskID, v.Status, v.Text, noFail)
		},
	}
	cmd.Flags().IntVar(&interval, "interval", 2, "poll interval in seconds (>= 1)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "give up waiting after this many seconds (0 = no limit)")
	cmd.Flags().BoolVar(&noFail, "no-fail-on-task-failure", false, "exit 0 even if the task fails or is cancelled")
	return cmd
}

func newTaskArtifactsCmd() *cobra.Command {
	var saveDir string
	cmd := &cobra.Command{
		Use:   "artifacts [alias-or-url] <task-id>",
		Short: "List a task's artifacts; --save downloads them",
		Long: `List the task's artifacts (the protocol's durable-output channel). With
--save, text parts, inline bytes, structured data, and URL parts are written
to files in the given directory (URL parts are fetched with the agent's
credentials).

` + taskArgsUse,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, taskID, err := resolveTaskArgs(args)
			if err != nil {
				return err
			}
			conn, err := connectTo(cmd, target)
			if err != nil {
				return classifyExit(err)
			}
			defer conn.Close()
			t, err := conn.GetTask(cmd.Context(), taskID)
			if err != nil {
				return classifyExit(err)
			}
			recordTask(target, conn, t)

			views := normalize.Artifacts(t.Artifacts)
			if saveDir != "" {
				saved, err := saveArtifacts(cmd.Context(), conn, t.Artifacts, saveDir)
				if err != nil {
					return classifyExit(err)
				}
				if opts.JSON {
					return printJSON(map[string]any{"taskId": string(t.ID), "saved": saved, "artifacts": views})
				}
				for _, p := range saved {
					fmt.Println(p)
				}
				return nil
			}

			if opts.JSON {
				return printJSON(map[string]any{"taskId": string(t.ID), "artifacts": views})
			}
			if len(views) == 0 {
				fmt.Println("no artifacts")
				return nil
			}
			printArtifactSummary(views, target, string(t.ID))
			return nil
		},
	}
	cmd.Flags().StringVar(&saveDir, "save", "", "download all artifact contents into this directory")
	return cmd
}

func newTaskListCmd() *cobra.Command {
	var (
		activeOnly bool
		agent      string
		refresh    bool
		limit      int
		remote     string
		contextID  string
		status     string
		pageToken  string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List delegated tasks (local ledger, or the agent's own list with --remote)",
		Long: `Every task handle this machine has observed (via send, stream, watch, get)
is recorded in a local ledger — the delegation memory an agent needs across
sessions: what did I delegate, where, and what state was it in.

--refresh re-polls every non-terminal task for its current state (updating
the ledger); --active hides finished tasks.

--remote <alias-or-url> instead asks the agent itself (A2A ListTasks): every
task visible to your credentials, newest first, including ones created from
other machines. Filter with --context-id and --status; --limit sets the page
size and --page-token fetches the next page (printed when more exist).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote != "" {
				if activeOnly || refresh || agent != "" {
					return fmt.Errorf("--active, --refresh and --agent apply to the local ledger, not --remote (use --status)")
				}
				return listRemote(cmd, remote, contextID, status, limit, pageToken)
			}
			if contextID != "" || status != "" || pageToken != "" {
				return fmt.Errorf("--context-id, --status and --page-token require --remote")
			}
			entries, err := ledgerList()
			if err != nil {
				return err
			}
			if refresh {
				entries = refreshEntries(cmd.Context(), entries)
			}
			filtered := entries[:0:0]
			for _, e := range entries {
				if activeOnly && (normalize.Terminal(e.State)) {
					continue
				}
				if agent != "" && e.Target != agent {
					continue
				}
				filtered = append(filtered, e)
			}
			if limit > 0 && len(filtered) > limit {
				filtered = filtered[:limit]
			}
			if opts.JSON {
				return printJSON(filtered)
			}
			if len(filtered) == 0 {
				fmt.Println("no tasks recorded (the ledger fills as you send/watch tasks)")
				return nil
			}
			for _, e := range filtered {
				fmt.Printf("%-19s  %-14s  %-12s  %s\n", e.TS, e.State, e.Target, e.TaskID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&activeOnly, "active", false, "only tasks not yet terminal")
	cmd.Flags().StringVar(&agent, "agent", "", "only tasks delegated to this alias/URL")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "re-poll non-terminal tasks for current state")
	cmd.Flags().IntVar(&limit, "limit", 0, "max entries to show (0 = all; with --remote: page size, 1-100)")
	cmd.Flags().StringVar(&remote, "remote", "", "list tasks from this agent via A2A ListTasks instead of the local ledger")
	cmd.Flags().StringVar(&contextID, "context-id", "", "with --remote: only tasks in this context")
	cmd.Flags().StringVar(&status, "status", "", "with --remote: only tasks in this state (working, input-required, completed, ...)")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "with --remote: continue from a previous page")
	return cmd
}

// listRemote implements `task list --remote`.
func listRemote(cmd *cobra.Command, target, contextID, status string, limit int, pageToken string) error {
	lo := client.ListOptions{ContextID: contextID, PageSize: limit, PageToken: pageToken}
	if status != "" {
		st, ok := normalize.ParseState(status)
		if !ok {
			return fmt.Errorf("unknown --status %q (want submitted, working, input-required, auth-required, completed, failed, cancelled, rejected)", status)
		}
		lo.Status = st
	}
	if limit < 0 || limit > 100 {
		return fmt.Errorf("--limit with --remote must be 1-100 (got %d)", limit)
	}
	conn, err := connectTo(cmd, target)
	if err != nil {
		return classifyExit(err)
	}
	defer conn.Close()
	resp, err := conn.ListTasks(cmd.Context(), lo)
	if err != nil {
		return classifyExit(err)
	}
	page := normalize.TaskPage{TotalSize: resp.TotalSize, NextPageToken: resp.NextPageToken, Tasks: []normalize.TaskView{}}
	for _, t := range resp.Tasks {
		if t != nil {
			page.Tasks = append(page.Tasks, normalize.Task(t))
		}
	}
	if opts.JSON {
		return printJSON(page)
	}
	if len(page.Tasks) == 0 {
		fmt.Println("no tasks")
		return nil
	}
	for _, t := range page.Tasks {
		fmt.Printf("%-14s  %-36s  %s\n", t.Status, t.TaskID, t.ContextID)
	}
	fmt.Printf("(%d of %d)\n", len(page.Tasks), page.TotalSize)
	if page.NextPageToken != "" {
		fmt.Printf("more: a2acli task list --remote %s --page-token %s\n", target, page.NextPageToken)
	}
	return nil
}

// refreshEntries polls each non-terminal ledger entry once, updating states.
// Unreachable agents leave the stale entry annotated on stderr.
func refreshEntries(ctx context.Context, entries []ledgerEntry) []ledgerEntry {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: refresh skipped: %v\n", err)
		return entries
	}
	conns := map[string]*client.Conn{}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i, e := range entries {
		if normalize.Terminal(e.State) {
			continue
		}
		target := e.Target
		if target == "" {
			target = e.URL
		}
		conn, ok := conns[target]
		if !ok {
			conn, err = client.Connect(ctx, target, cfg, connectOptions())
			if err != nil {
				fmt.Fprintf(os.Stderr, "note: %s unreachable, state may be stale: %v\n", target, err)
				conns[target] = nil
				continue
			}
			conns[target] = conn
		}
		if conn == nil {
			continue
		}
		t, err := conn.GetTask(ctx, e.TaskID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: refresh %s failed: %v\n", e.TaskID, err)
			continue
		}
		recordTask(target, conn, t)
		entries[i].State = normalize.State(t.Status.State)
		entries[i].Text = normalize.TaskText(t)
		entries[i].TS = time.Now().UTC().Format(time.RFC3339)
	}
	return entries
}
