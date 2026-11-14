// Agent-first contract tests: exit codes for paused/failed/timed-out tasks,
// continuation via --task-id, artifacts (views, chunked streaming, --save),
// the local delegation ledger, --max-text frugality, and stream resilience.
package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyjmorgan/a2acli/internal/mocka2a"
)

// Exit codes from the CLI contract (internal/cli/exit.go).
const (
	exitTaskFailed    = 1
	exitInputRequired = 3
	exitAuthRequired  = 4
	exitTransport     = 5
)

const truncationMark = "…[truncated: full text via task get]"

// afEvent is the full JSONL stream-event shape (superset of streamEvent).
type afEvent struct {
	TS           string `json:"ts"`
	Event        string `json:"event"`
	TaskID       string `json:"taskId"`
	ContextID    string `json:"contextId"`
	Status       string `json:"status"`
	Text         string `json:"text"`
	ArtifactID   string `json:"artifactId"`
	ArtifactName string `json:"artifactName"`
	Append       bool   `json:"append"`
	LastChunk    bool   `json:"lastChunk"`
	Error        string `json:"error"`
	ActiveTaskID string `json:"activeTaskId"`
}

func parseEvents(t *testing.T, s string) []afEvent {
	t.Helper()
	var events []afEvent
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var ev afEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("non-JSONL line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// fullTaskView is the complete normalize.TaskView shape including artifacts.
type fullTaskView struct {
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId"`
	Status    string `json:"status"`
	Text      string `json:"text"`
	Artifacts []struct {
		ID    string `json:"artifactId"`
		Name  string `json:"name"`
		Text  string `json:"text"`
		Files []struct {
			Name      string `json:"name"`
			MediaType string `json:"mediaType"`
			SizeBytes int    `json:"sizeBytes"`
			URI       string `json:"uri"`
		} `json:"files"`
		DataParts int `json:"dataParts"`
	} `json:"artifacts"`
}

// decodeSingle decodes s as exactly one JSON value; trailing values fail.
func decodeSingle[T any](t *testing.T, s string) T {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var v T
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("bad JSON %q: %v", s, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("expected exactly ONE JSON object, found more: %q", s)
	}
	return v
}

// --- input-required / auth-required pause + continuation ---------------------

func TestInputRequiredLifecycle(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "input:What color?", "--json")
	id := decode[sendResult](t, r.stdout).TaskID
	if id == "" {
		t.Fatalf("no task handle: %s", r.stdout)
	}

	// wait drains the poll and stops on the pause: exit 3, question on stdout,
	// exactly ONE JSON object.
	r = c.run("task", "wait", srv.URL, id, "--interval", "1", "--json")
	if r.code != exitInputRequired {
		t.Fatalf("wait on paused task: code=%d stderr=%s", r.code, r.stderr)
	}
	tv := decodeSingle[fullTaskView](t, r.stdout)
	if tv.Status != "input-required" || tv.Text != "What color?" {
		t.Fatalf("paused view: %+v", tv)
	}

	// Continue with send --task-id: the task resumes working.
	r = c.mustRun("send", srv.URL, "blue", "--task-id", id, "--json")
	res := decode[sendResult](t, r.stdout)
	if res.Type != "task" || res.TaskID != id || res.Status != "working" {
		t.Fatalf("continuation: %+v", res)
	}

	// It then completes with the echoed answer.
	r = c.mustRun("task", "wait", srv.URL, id, "--interval", "1", "--json")
	tv = decodeSingle[fullTaskView](t, r.stdout)
	if tv.Status != "completed" || !strings.Contains(tv.Text, "answered: blue") {
		t.Fatalf("after continuation: %+v", tv)
	}
}

func TestSendWaitStopsOnPauseAndPrintsQuestion(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.run("send", srv.URL, "input:Pick one option", "--wait", "10")
	if r.code != exitInputRequired {
		t.Fatalf("send --wait on pausing task: code=%d stderr=%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "Pick one option") {
		t.Fatalf("question not printed: %s", r.stdout)
	}
}

func TestAuthRequiredExitCode(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "authreq:need a token", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.run("task", "wait", srv.URL, id, "--interval", "1", "--json")
	if r.code != exitAuthRequired {
		t.Fatalf("code=%d stderr=%s", r.code, r.stderr)
	}
	tv := decodeSingle[fullTaskView](t, r.stdout)
	if tv.Status != "auth-required" || tv.Text != "need a token" {
		t.Fatalf("paused view: %+v", tv)
	}
}

func TestWatchStopsOnInputRequired(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "input:Which env?", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	// watch must stop on the pause (no infinite poll) and exit 3.
	r = c.run("task", "watch", srv.URL, id, "--interval", "1")
	if r.code != exitInputRequired {
		t.Fatalf("watch on pausing task: code=%d stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "Which env?") {
		t.Fatalf("question missing from watch output: %s", r.stdout)
	}
}

// --- timeouts and flag validation ---------------------------------------------

func TestWatchTimeoutExitsTransport(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:600:x", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.run("task", "watch", srv.URL, id, "--timeout", "2", "--interval", "1")
	if r.code != exitTransport {
		t.Fatalf("watch --timeout: code=%d stderr=%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "timed out") {
		t.Fatalf("stderr should mention the timeout: %s", r.stderr)
	}
}

func TestIntervalZeroIsUsageError(t *testing.T) {
	c := newCLI(t)

	// Validated before any connection: no server needed.
	r := c.run("task", "watch", "http://localhost:1", "tid", "--interval", "0")
	if r.code == 0 || !strings.Contains(r.stderr, "--interval") {
		t.Fatalf("watch --interval 0: code=%d stderr=%s", r.code, r.stderr)
	}
	r = c.run("stream", "http://localhost:1", "hi", "--interval", "0")
	if r.code == 0 || !strings.Contains(r.stderr, "--interval") {
		t.Fatalf("stream --interval 0: code=%d stderr=%s", r.code, r.stderr)
	}
}

// --- artifacts -----------------------------------------------------------------

func TestArtifactTaskViewAndSave(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "artifact:hello", "--json")
	id := decode[sendResult](t, r.stdout).TaskID
	c.mustRun("task", "wait", srv.URL, id, "--interval", "1", "--json")

	// task get --json carries the normalized artifact.
	r = c.mustRun("task", "get", srv.URL, id, "--json")
	tv := decode[fullTaskView](t, r.stdout)
	if tv.Status != "completed" || len(tv.Artifacts) != 1 {
		t.Fatalf("task view: %+v", tv)
	}
	a := tv.Artifacts[0]
	if a.ID != "art_1" || a.Name != "result" || a.Text != "hello" {
		t.Fatalf("artifact view: %+v", a)
	}
	if len(a.Files) != 1 || a.Files[0].SizeBytes != len("hello-bytes") ||
		a.Files[0].Name != "data.bin" || a.Files[0].MediaType != "application/octet-stream" {
		t.Fatalf("artifact files: %+v", a.Files)
	}
	if a.DataParts != 1 {
		t.Fatalf("dataParts = %d, want 1", a.DataParts)
	}

	// --save downloads every part; --json reports the saved paths.
	dir := t.TempDir()
	r = c.mustRun("task", "artifacts", srv.URL, id, "--save", dir, "--json")
	var out struct {
		TaskID string   `json:"taskId"`
		Saved  []string `json:"saved"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("save json: %v: %s", err, r.stdout)
	}
	if out.TaskID != id || len(out.Saved) < 3 {
		t.Fatalf("save output: %+v", out)
	}

	wantFile := func(base, contains string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, base))
		if err != nil {
			t.Fatalf("saved %s: %v (saved: %v)", base, err, out.Saved)
		}
		if !strings.Contains(string(data), contains) {
			t.Fatalf("%s content %q missing %q", base, data, contains)
		}
	}
	wantFile("result.txt", "hello")
	wantFile("data.bin", "hello-bytes")

	jsonParts := 0
	for _, p := range out.Saved {
		if strings.HasSuffix(p, ".json") {
			jsonParts++
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"k"`) {
				t.Fatalf("data part content: %s", data)
			}
		}
	}
	if jsonParts != 1 {
		t.Fatalf("want 1 saved .json data part, got %d: %v", jsonParts, out.Saved)
	}
}

// --- --max-text ------------------------------------------------------------------

func TestMaxTextTruncatesWithMarker(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	long := strings.Repeat("abcdefghij", 10)
	r := c.mustRun("send", srv.URL, "echo:"+long, "--max-text", "10", "--json")
	res := decode[sendResult](t, r.stdout)
	if !strings.HasSuffix(res.Text, truncationMark) {
		t.Fatalf("missing truncation marker: %q", res.Text)
	}
	if got := strings.TrimSuffix(res.Text, truncationMark); got != "Mock respo" {
		t.Fatalf("truncated prefix: %q", got)
	}
}

// --- ledger ----------------------------------------------------------------------

type ledgerRow struct {
	TS        string `json:"ts"`
	Target    string `json:"target"`
	URL       string `json:"url"`
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId"`
	State     string `json:"state"`
	Text      string `json:"text"`
}

func TestLedgerRecordsAndFilters(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "echo:ledger done", "--return-immediately", "--json")
	id := decode[sendResult](t, r.stdout).TaskID
	c.mustRun("task", "wait", srv.URL, id, "--interval", "1", "--json")

	// The ledger file lives in the config dir.
	if _, err := os.Stat(filepath.Join(c.configDir, "ledger.jsonl")); err != nil {
		t.Fatalf("ledger file: %v", err)
	}

	r = c.mustRun("task", "list", "--json")
	rows := decode[[]ledgerRow](t, r.stdout)
	var found *ledgerRow
	for i := range rows {
		if rows[i].TaskID == id {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("task %s not in ledger: %s", id, r.stdout)
	}
	if found.State != "completed" || found.Target != srv.URL || found.TS == "" {
		t.Fatalf("ledger row: %+v", *found)
	}

	// --active excludes terminal tasks.
	r = c.mustRun("task", "list", "--active", "--json")
	if strings.Contains(r.stdout, id) {
		t.Fatalf("--active should exclude completed task: %s", r.stdout)
	}
}

func TestSingleArgTaskAddressingViaLedger(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:1:solo", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	// Full ID, single argument: agent recalled from the ledger.
	r = c.mustRun("task", "get", id, "--json")
	if tv := decode[fullTaskView](t, r.stdout); tv.TaskID != id {
		t.Fatalf("single-arg get: %+v", tv)
	}
	// Unique prefix works too.
	r = c.mustRun("task", "get", id[:8], "--json")
	tv := decode[fullTaskView](t, r.stdout)
	if tv.TaskID != id || tv.Status != "completed" || !strings.Contains(tv.Text, "solo") {
		t.Fatalf("prefix get: %+v", tv)
	}
}

// --- stream resilience and adversarial orderings ----------------------------------

func TestStreamMsgfailStillExitsFailed(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	// A final-looking Message followed by a failed task must not exit 0.
	r := c.run("stream", srv.URL, "msgfail:")
	if r.code != exitTaskFailed {
		t.Fatalf("msgfail stream: code=%d stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
	}
	events := parseEvents(t, r.stdout)
	if events[0].Event != "message" || !strings.Contains(events[0].Text, "looks done") {
		t.Fatalf("first event: %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Status != "failed" {
		t.Fatalf("last event: %+v", last)
	}
}

func TestStreamDropContinuesViaPolling(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.run("stream", srv.URL, "drop:2:recovered", "--interval", "1")
	if r.code != 0 {
		t.Fatalf("dropped stream must recover to exit 0: code=%d stderr=%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "continuing via polling") {
		t.Fatalf("stderr should note the polling continuation: %s", r.stderr)
	}
	events := parseEvents(t, r.stdout)
	last := events[len(events)-1]
	if last.Status != "completed" || !strings.Contains(last.Text, "recovered") {
		t.Fatalf("last event after recovery: %+v", last)
	}
}

func TestStreamInputRequiredEndsStreamWithQuestion(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.run("stream", srv.URL, "input:What color?")
	if r.code != exitInputRequired {
		t.Fatalf("stream on input-required: code=%d stderr=%s", r.code, r.stderr)
	}
	events := parseEvents(t, r.stdout)
	last := events[len(events)-1]
	if last.Event != "status_update" || last.Status != "input-required" || !strings.Contains(last.Text, "What color?") {
		t.Fatalf("input-required event: %+v", last)
	}
}

func TestStreamArtifactChunks(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("stream", srv.URL, "artifact:chunked")
	events := parseEvents(t, r.stdout)

	var chunks []afEvent
	for _, ev := range events {
		if ev.Event == "artifact_update" {
			chunks = append(chunks, ev)
		}
	}
	if len(chunks) != 2 {
		t.Fatalf("want 2 artifact_update events, got %d: %s", len(chunks), r.stdout)
	}
	if chunks[0].ArtifactID != "art_1" || chunks[0].Append || !strings.Contains(chunks[0].Text, "chunked") {
		t.Fatalf("first chunk: %+v", chunks[0])
	}
	if chunks[1].ArtifactID != "art_1" || !chunks[1].Append || !chunks[1].LastChunk || !strings.Contains(chunks[1].Text, "-more") {
		t.Fatalf("second chunk: %+v", chunks[1])
	}
	if last := events[len(events)-1]; last.Status != "completed" {
		t.Fatalf("last event: %+v", last)
	}

	// The completed task from GetTask carries the full artifact.
	r = c.mustRun("task", "get", srv.URL, chunks[0].TaskID, "--json")
	tv := decode[fullTaskView](t, r.stdout)
	if len(tv.Artifacts) != 1 || tv.Artifacts[0].ID != "art_1" || tv.Artifacts[0].Text != "chunked" {
		t.Fatalf("task after chunked stream: %+v", tv)
	}
}

func TestStreamBusyEnvelope(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("stream", srv.URL, "busy:")
	events := parseEvents(t, r.stdout)
	if len(events) != 1 {
		t.Fatalf("want 1 busy event, got %d: %s", len(events), r.stdout)
	}
	if events[0].Event != "busy" || events[0].Error != "context_busy" || events[0].ActiveTaskID != "task_mock" {
		t.Fatalf("busy event: %+v", events[0])
	}
}

// --- --out permissions --------------------------------------------------------------

func TestOutFileIsPrivate(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:1:x", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	out := filepath.Join(t.TempDir(), "watch.jsonl")
	c.mustRun("task", "watch", srv.URL, id, "--interval", "1", "--out", out)
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("--out file mode %o, want 0600", st.Mode().Perm())
	}
}
