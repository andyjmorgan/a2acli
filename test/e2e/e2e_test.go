// Package e2e drives the compiled a2acli binary end to end against an
// in-process mock A2A server (internal/mocka2a). No network access is needed;
// every scenario from the design note's Phase 1 contract is exercised at the
// process boundary: flags, env, exit codes, stdout shapes.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyjmorgan/a2acli/internal/mocka2a"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "a2acli-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "a2acli")
	build := exec.Command("go", "build", "-o", binPath, "github.com/andyjmorgan/a2acli/cmd/a2acli")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build a2acli:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// cli is one isolated CLI environment: its own config dir, no keyring.
type cli struct {
	t         *testing.T
	configDir string
	env       []string
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	dir := t.TempDir()
	return &cli{
		t:         t,
		configDir: dir,
		env: append(os.Environ(),
			"A2ACLI_CONFIG_DIR="+dir,
			"A2ACLI_NO_KEYRING=1",
		),
	}
}

func (c *cli) setenv(kv ...string) { c.env = append(c.env, kv...) }

type result struct {
	stdout string
	stderr string
	code   int
}

func (c *cli) run(args ...string) result {
	c.t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = c.env
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatalf("run %v: %v", args, err)
	}
	return result{stdout: out.String(), stderr: errb.String(), code: code}
}

func (c *cli) mustRun(args ...string) result {
	c.t.Helper()
	r := c.run(args...)
	if r.code != 0 {
		c.t.Fatalf("%v exited %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad JSON %q: %v", s, err)
	}
	return v
}

type sendResult struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	TaskID       string `json:"taskId"`
	ContextID    string `json:"contextId"`
	Status       string `json:"status"`
	Error        string `json:"error"`
	ActiveTaskID string `json:"activeTaskId"`
}

type taskView struct {
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId"`
	Status    string `json:"status"`
	Text      string `json:"text"`
}

// --- agent registry ---------------------------------------------------------

func TestAgentRegistryLifecycle(t *testing.T) {
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", "http://localhost:9999")
	r := c.mustRun("agent", "list")
	if !strings.Contains(r.stdout, "mock") || !strings.Contains(r.stdout, "http://localhost:9999") {
		t.Fatalf("list: %s", r.stdout)
	}
	r = c.mustRun("agent", "show", "mock")
	if !strings.Contains(r.stdout, "auth: none") {
		t.Fatalf("show: %s", r.stdout)
	}
	c.mustRun("agent", "remove", "mock")
	r = c.run("agent", "show", "mock")
	if r.code == 0 {
		t.Fatal("show after remove should fail")
	}

	// config.toml must be 0600
	st, err := os.Stat(filepath.Join(c.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %o", st.Mode().Perm())
	}
}

func TestAgentAddNeverStoresSecretInConfig(t *testing.T) {
	c := newCLI(t)
	r := c.mustRun("agent", "add", "mock", "http://localhost:1", "--secret", "supersecretvalue")
	if !strings.Contains(r.stderr, "warning") {
		t.Fatalf("literal secret should warn, stderr: %s", r.stderr)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(c.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfgBytes), "supersecretvalue") {
		t.Fatal("literal secret leaked into main config")
	}
	secBytes, err := os.ReadFile(filepath.Join(c.configDir, "secrets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secBytes), "supersecretvalue") {
		t.Fatal("literal secret not stored in secrets file")
	}
	st, _ := os.Stat(filepath.Join(c.configDir, "secrets.toml"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets mode %o", st.Mode().Perm())
	}
}

// --- card --------------------------------------------------------------------

func TestCardHumanAndJSON(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithName("mock-agent")).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("card", srv.URL)
	if !strings.Contains(r.stdout, "mock-agent") || !strings.Contains(r.stdout, "JSONRPC") {
		t.Fatalf("card human: %s", r.stdout)
	}
	r = c.mustRun("card", srv.URL, "--json")
	card := decode[map[string]any](t, r.stdout)
	if card["name"] != "mock-agent" {
		t.Fatalf("card json: %v", card)
	}
}

// --- send --------------------------------------------------------------------

func TestSendImmediateMessage(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "echo:hello world")
	if strings.TrimSpace(r.stdout) != "Mock response to: hello world" {
		t.Fatalf("stdout: %q", r.stdout)
	}
	r = c.mustRun("send", srv.URL, "echo:hi", "--json")
	res := decode[sendResult](t, r.stdout)
	if res.Type != "message" || res.Text != "Mock response to: hi" {
		t.Fatalf("json: %+v", res)
	}
}

func TestSendTaskHandle(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:3:done after three", "--json")
	res := decode[sendResult](t, r.stdout)
	if res.Type != "task" || res.TaskID == "" || res.Status != "working" {
		t.Fatalf("json: %+v", res)
	}

	// Human form includes the watch/get hints from the design note.
	r = c.mustRun("send", srv.URL, "task:3:x")
	for _, want := range []string{"is working in context", "task watch", "task get"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("human handle output missing %q: %s", want, r.stdout)
		}
	}
}

func TestSendBusyEnvelope(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	// Human: printed exactly as returned.
	r := c.mustRun("send", srv.URL, "busy:")
	if strings.TrimSpace(r.stdout) != mocka2a.BusyEnvelope {
		t.Fatalf("busy human: %q", r.stdout)
	}
	// JSON: normalized.
	r = c.mustRun("send", srv.URL, "busy:", "--json")
	res := decode[sendResult](t, r.stdout)
	if res.Type != "busy" || res.Error != "context_busy" || res.ActiveTaskID != "task_mock" {
		t.Fatalf("busy json: %+v", res)
	}
}

func TestSendReturnImmediately(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "echo:fast", "--return-immediately", "--json")
	res := decode[sendResult](t, r.stdout)
	if res.Type != "task" {
		t.Fatalf("returnImmediately should yield a task handle: %+v", res)
	}
	// The final message arrives on the next poll.
	r = c.mustRun("task", "get", srv.URL, res.TaskID, "--json")
	tv := decode[taskView](t, r.stdout)
	if tv.Status != "completed" || !strings.Contains(tv.Text, "Mock response to: fast") {
		t.Fatalf("task after returnImmediately: %+v", tv)
	}
}

func TestSendWaitPollsToCompletion(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:1:final answer", "--wait", "10", "--json")
	res := decode[sendResult](t, r.stdout)
	if res.Type != "task" || res.Status != "completed" || !strings.Contains(res.Text, "final answer") {
		t.Fatalf("wait json: %+v", res)
	}
}

func TestSendContextIDRoundTrip(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:1:x", "--context-id", "ctx_e2e", "--json")
	res := decode[sendResult](t, r.stdout)
	if res.ContextID != "ctx_e2e" {
		t.Fatalf("contextId not honored: %+v", res)
	}
}

// --- auth --------------------------------------------------------------------

func TestAuthAPIKeyViaSecretWalkEnv(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("k-env")).Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", srv.URL, "--secret", "name:mockkey")
	c.setenv("A2ACLI_SECRET_MOCKKEY=k-env")
	r := c.mustRun("send", "mock", "echo:authed")
	if !strings.Contains(r.stdout, "Mock response to: authed") {
		t.Fatalf("stdout: %s", r.stdout)
	}
}

func TestAuthAPIKeyViaSecretsFile(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("k-file")).Start()
	defer srv.Close()
	c := newCLI(t)

	if err := os.WriteFile(filepath.Join(c.configDir, "secrets.toml"), []byte("mockkey = \"k-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.mustRun("agent", "add", "mock", srv.URL, "--secret", "name:mockkey")
	c.mustRun("send", "mock", "echo:authed")
}

func TestAuthWrongKeyIs401(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("right")).Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", srv.URL, "--secret", "name:mockkey")
	c.setenv("A2ACLI_SECRET_MOCKKEY=wrong")
	r := c.run("send", "mock", "echo:x")
	if r.code == 0 {
		t.Fatalf("wrong key should fail; stdout: %s", r.stdout)
	}
}

func TestAuthMissingSecretNamesPlacesLooked(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("k")).Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", srv.URL, "--secret", "name:ghost")
	r := c.run("send", "mock", "echo:x")
	if r.code == 0 {
		t.Fatal("missing secret should fail")
	}
	for _, want := range []string{"A2ACLI_SECRET_GHOST", "secrets file"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("stderr should name %q: %s", want, r.stderr)
		}
	}
}

func TestAuthSecretCmd(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("cmd-key")).Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", srv.URL,
		"--secret", "cmd:echo cmd-key",
		"--auth-header", "X-API-Key", "--auth-value", "$secret")
	c.mustRun("send", "mock", "echo:via-cmd")
}

func TestAuthSecretCmd401RefreshRetry(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("tok2")).Start()
	defer srv.Close()
	c := newCLI(t)

	// First execution prints tok1 (rejected), the post-401 re-execution
	// prints tok2. Success proves invalidate+retry ran inside one process.
	dir := t.TempDir()
	script := filepath.Join(dir, "token.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho x >> "+dir+"/calls\nprintf 'tok%s' $(wc -l < "+dir+"/calls | tr -d ' ')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.mustRun("agent", "add", "mock", srv.URL,
		"--secret", "cmd:"+script,
		"--auth-header", "X-API-Key", "--auth-value", "$secret")
	c.mustRun("send", "mock", "echo:refresh worked")

	calls, err := os.ReadFile(dir + "/calls")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(calls), "x"); n != 2 {
		t.Fatalf("secret_cmd executed %d times, want 2 (initial + post-401 refresh)", n)
	}
}

func TestAuthOverrideFlag(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("override-key")).Start()
	defer srv.Close()
	c := newCLI(t)

	// No configured auth at all: --secret provides it one-time.
	c.mustRun("agent", "add", "mock", srv.URL)
	c.mustRun("send", "mock", "echo:x", "--secret", "cmd:echo override-key")
	c.mustRun("send", "mock", "echo:y", "--secret", "override-key") // literal
	c.setenv("A2ACLI_SECRET_OV=override-key")
	c.mustRun("send", "mock", "echo:z", "--secret", "name:ov")
}

// Placement derived from the card: no --auth-header/--auth-value needed,
// the mock card advertises apiKey in header X-API-Key.
func TestAuthPlacementDerivedFromCard(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithAPIKey("derived")).Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", srv.URL, "--secret", "name:d")
	c.setenv("A2ACLI_SECRET_D=derived")
	c.mustRun("send", "mock", "echo:derived placement")
}

// --- task get / cancel -------------------------------------------------------

func TestTaskGetProgression(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:2:all done", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.mustRun("task", "get", srv.URL, id, "--json")
	if tv := decode[taskView](t, r.stdout); tv.Status != "working" {
		t.Fatalf("poll 1: %+v", tv)
	}
	r = c.mustRun("task", "get", srv.URL, id, "--json")
	tv := decode[taskView](t, r.stdout)
	if tv.Status != "completed" || !strings.Contains(tv.Text, "all done") {
		t.Fatalf("poll 2: %+v", tv)
	}

	// Human output for a completed task includes the final text.
	r = c.mustRun("task", "get", srv.URL, id)
	if !strings.Contains(r.stdout, "completed") || !strings.Contains(r.stdout, "all done") {
		t.Fatalf("human: %s", r.stdout)
	}
}

func TestTaskGetUnknownFails(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	if r := c.run("task", "get", srv.URL, "task_ghost"); r.code == 0 {
		t.Fatal("unknown task should fail")
	}
}

func TestTaskCancel(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:50:never", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.mustRun("task", "cancel", srv.URL, id, "--json")
	if tv := decode[taskView](t, r.stdout); tv.Status != "cancelled" {
		t.Fatalf("cancel: %+v", tv)
	}
	if r := c.run("task", "cancel", srv.URL, "task_ghost"); r.code == 0 {
		t.Fatal("cancel of unknown task should fail")
	}
}

// --- task watch ----------------------------------------------------------------

func TestWatchToCompletionExitZeroAndJSONL(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:2:watched result", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	out := filepath.Join(t.TempDir(), "task.jsonl")
	r = c.mustRun("task", "watch", srv.URL, id, "--interval", "1", "--out", out)
	if !strings.Contains(r.stdout, "working") || !strings.Contains(r.stdout, "completed") || !strings.Contains(r.stdout, "watched result") {
		t.Fatalf("watch human: %s", r.stdout)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("want >=2 JSONL snapshots, got %d: %s", len(lines), data)
	}
	type snap struct {
		Event   string `json:"event"`
		TaskID  string `json:"taskId"`
		Status  string `json:"status"`
		Message string `json:"message"`
		TS      string `json:"ts"`
	}
	first := decode[snap](t, lines[0])
	last := decode[snap](t, lines[len(lines)-1])
	if first.Event != "task_snapshot" || first.Status != "working" || first.TaskID != id {
		t.Fatalf("first snapshot: %+v", first)
	}
	if last.Status != "completed" || !strings.Contains(last.Message, "watched result") || last.TS == "" {
		t.Fatalf("last snapshot: %+v", last)
	}
}

func TestWatchFailedTaskExitsNonZero(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "fail:it broke", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	if r := c.run("task", "watch", srv.URL, id, "--interval", "1"); r.code == 0 {
		t.Fatalf("failed task should exit non-zero: %s", r.stdout)
	}
	// Suppressed with the flag.
	r2 := c.mustRun("send", srv.URL, "fail:again", "--json")
	id2 := decode[sendResult](t, r2.stdout).TaskID
	if r := c.run("task", "watch", srv.URL, id2, "--interval", "1", "--no-fail-on-task-failure"); r.code != 0 {
		t.Fatalf("--no-fail-on-task-failure should exit 0, got %d", r.code)
	}
}

func TestWatchCancelledTaskExitsNonZero(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:50:never", "--json")
	id := decode[sendResult](t, r.stdout).TaskID
	c.mustRun("task", "cancel", srv.URL, id)

	if r := c.run("task", "watch", srv.URL, id, "--interval", "1"); r.code == 0 {
		t.Fatal("cancelled task should exit non-zero")
	}
}

func TestWatchJSONModeEmitsSnapshots(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:1:done", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.mustRun("task", "watch", srv.URL, id, "--interval", "1", "--json")
	for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("non-JSONL line in --json watch output: %q", line)
		}
	}
}

// --- errors -------------------------------------------------------------------

func TestUnknownAliasError(t *testing.T) {
	c := newCLI(t)
	r := c.run("send", "nosuch", "hi")
	if r.code == 0 || !strings.Contains(r.stderr, "nosuch") {
		t.Fatalf("code=%d stderr=%s", r.code, r.stderr)
	}
}
