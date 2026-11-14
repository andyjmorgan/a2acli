package mocka2a

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rpcResponse is the raw JSON-RPC envelope used for wire-shape assertions.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// wireTask mirrors the wire shape of an a2a.Task for assertions.
type wireTask struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	Status    struct {
		State   string       `json:"state"`
		Message *wireMessage `json:"message"`
	} `json:"status"`
	History []wireMessage `json:"history"`
}

type wireMessage struct {
	MessageID string `json:"messageId"`
	ContextID string `json:"contextId"`
	TaskID    string `json:"taskId"`
	Role      string `json:"role"`
	Parts     []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

func (m *wireMessage) text() string {
	var sb strings.Builder
	for _, p := range m.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// sendResult mirrors the StreamResponse wrapper the transport uses for
// SendMessage results.
type sendResult struct {
	Message *wireMessage `json:"message"`
	Task    *wireTask    `json:"task"`
}

func startServer(t *testing.T, opts ...Option) *httptest.Server {
	t.Helper()
	ts := New(opts...).Start()
	t.Cleanup(ts.Close)
	return ts
}

// rpcCall posts a raw JSON-RPC request to the server's /a2a endpoint.
func rpcCall(t *testing.T, ts *httptest.Server, headers map[string]string, method string, params any) rpcResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+A2APath, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post %s: unexpected HTTP status %d", method, resp.StatusCode)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	return out
}

func sendMessageParams(text string, extra map[string]any) map[string]any {
	msg := map[string]any{
		"messageId": "msg-1",
		"role":      "ROLE_USER",
		"parts":     []map[string]any{{"text": text}},
	}
	for k, v := range extra {
		msg[k] = v
	}
	return map[string]any{"message": msg}
}

func sendText(t *testing.T, ts *httptest.Server, text string) sendResult {
	t.Helper()
	return decodeSendResult(t, rpcCall(t, ts, nil, "SendMessage", sendMessageParams(text, nil)))
}

func decodeSendResult(t *testing.T, resp rpcResponse) sendResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("SendMessage returned error: %+v", resp.Error)
	}
	var res sendResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode send result: %v", err)
	}
	return res
}

func getTask(t *testing.T, ts *httptest.Server, id string) wireTask {
	t.Helper()
	resp := rpcCall(t, ts, nil, "GetTask", map[string]any{"id": id})
	if resp.Error != nil {
		t.Fatalf("GetTask returned error: %+v", resp.Error)
	}
	var task wireTask
	if err := json.Unmarshal(resp.Result, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return task
}

func TestAgentCardDefaults(t *testing.T) {
	ts := startServer(t)

	resp, err := http.Get(ts.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("card status = %d, want 200", resp.StatusCode)
	}

	var card struct {
		Name                string `json:"name"`
		SupportedInterfaces []struct {
			URL             string `json:"url"`
			ProtocolBinding string `json:"protocolBinding"`
		} `json:"supportedInterfaces"`
		Capabilities struct {
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
		SecuritySchemes      map[string]json.RawMessage `json:"securitySchemes"`
		SecurityRequirements []json.RawMessage          `json:"securityRequirements"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}

	if card.Name != "mock-agent" {
		t.Errorf("card name = %q, want %q", card.Name, "mock-agent")
	}
	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("supportedInterfaces len = %d, want 1", len(card.SupportedInterfaces))
	}
	if got, want := card.SupportedInterfaces[0].URL, ts.URL+A2APath; got != want {
		t.Errorf("interface url = %q, want %q", got, want)
	}
	if got := card.SupportedInterfaces[0].ProtocolBinding; got != "JSONRPC" {
		t.Errorf("protocolBinding = %q, want JSONRPC", got)
	}
	if card.Capabilities.Streaming {
		t.Error("capabilities.streaming = true, want false")
	}
	if len(card.SecuritySchemes) != 0 {
		t.Errorf("securitySchemes present without WithAPIKey: %v", card.SecuritySchemes)
	}
	if len(card.SecurityRequirements) != 0 {
		t.Errorf("securityRequirements present without WithAPIKey: %v", card.SecurityRequirements)
	}
}

func TestAgentCardWithNameAndAPIKey(t *testing.T) {
	ts := startServer(t, WithName("custom-agent"), WithAPIKey("sekrit"))

	resp, err := http.Get(ts.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("card status = %d, want 200 (card must stay public)", resp.StatusCode)
	}

	var card struct {
		Name            string `json:"name"`
		SecuritySchemes map[string]struct {
			APIKey *struct {
				Location string `json:"location"`
				Name     string `json:"name"`
			} `json:"apiKeySecurityScheme"`
		} `json:"securitySchemes"`
		SecurityRequirements []struct {
			Schemes map[string]json.RawMessage `json:"schemes"` // {} or {"list":[...]} (ProtoJSON)
		} `json:"securityRequirements"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}

	if card.Name != "custom-agent" {
		t.Errorf("card name = %q, want %q", card.Name, "custom-agent")
	}
	scheme, ok := card.SecuritySchemes["apiKey"]
	if !ok || scheme.APIKey == nil {
		t.Fatalf("apiKey security scheme missing: %v", card.SecuritySchemes)
	}
	if scheme.APIKey.Name != "X-API-Key" {
		t.Errorf("scheme header name = %q, want X-API-Key", scheme.APIKey.Name)
	}
	if scheme.APIKey.Location != "header" {
		t.Errorf("scheme location = %q, want header", scheme.APIKey.Location)
	}
	found := false
	for _, req := range card.SecurityRequirements {
		if _, ok := req.Schemes["apiKey"]; ok {
			found = true
		}
	}
	if !found {
		t.Errorf("securityRequirements does not reference apiKey: %v", card.SecurityRequirements)
	}
}

func TestEchoReturnsImmediateMessage(t *testing.T) {
	ts := startServer(t)

	res := sendText(t, ts, "echo:hello world")
	if res.Message == nil {
		t.Fatalf("want message result, got %+v", res)
	}
	if got, want := res.Message.text(), "Mock response to: hello world"; got != want {
		t.Errorf("message text = %q, want %q", got, want)
	}
	if res.Message.Role != "ROLE_AGENT" {
		t.Errorf("message role = %q, want ROLE_AGENT", res.Message.Role)
	}
}

func TestDefaultTextEchoesFullText(t *testing.T) {
	ts := startServer(t)

	res := sendText(t, ts, "just some text")
	if res.Message == nil {
		t.Fatalf("want message result, got %+v", res)
	}
	if got, want := res.Message.text(), "Mock response to: just some text"; got != want {
		t.Errorf("message text = %q, want %q", got, want)
	}
}

func TestBusyEnvelope(t *testing.T) {
	ts := startServer(t)

	res := sendText(t, ts, "busy:")
	if res.Message == nil {
		t.Fatalf("want message result, got %+v", res)
	}
	if got := res.Message.text(); got != BusyEnvelope {
		t.Errorf("busy text = %q, want %q", got, BusyEnvelope)
	}
	var envelope struct {
		Status       string `json:"status"`
		Error        string `json:"error"`
		ContextID    string `json:"contextId"`
		ActiveTaskID string `json:"activeTaskId"`
	}
	if err := json.Unmarshal([]byte(res.Message.text()), &envelope); err != nil {
		t.Fatalf("busy text is not valid JSON: %v", err)
	}
	if envelope.Status != "busy" || envelope.Error != "context_busy" ||
		envelope.ContextID != "ctx_mock" || envelope.ActiveTaskID != "task_mock" {
		t.Errorf("unexpected busy envelope: %+v", envelope)
	}
}

func TestTaskCompletesAfterNPolls(t *testing.T) {
	ts := startServer(t)

	res := sendText(t, ts, "task:3:all done")
	if res.Task == nil {
		t.Fatalf("want task result, got %+v", res)
	}
	if res.Task.Status.State != "TASK_STATE_WORKING" {
		t.Errorf("initial state = %q, want TASK_STATE_WORKING", res.Task.Status.State)
	}
	if res.Task.ID == "" || res.Task.ContextID == "" {
		t.Fatalf("task must have id and contextId: %+v", res.Task)
	}

	// Polls 1 and 2 stay working, poll 3 observes completion.
	for i := 1; i <= 2; i++ {
		task := getTask(t, ts, res.Task.ID)
		if task.Status.State != "TASK_STATE_WORKING" {
			t.Fatalf("poll %d state = %q, want TASK_STATE_WORKING", i, task.Status.State)
		}
		if task.ContextID != res.Task.ContextID {
			t.Errorf("poll %d contextId = %q, want %q", i, task.ContextID, res.Task.ContextID)
		}
	}
	task := getTask(t, ts, res.Task.ID)
	if task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("poll 3 state = %q, want TASK_STATE_COMPLETED", task.Status.State)
	}
	if task.Status.Message == nil {
		t.Fatal("completed task has no status message")
	}
	if got, want := task.Status.Message.text(), "all done"; got != want {
		t.Errorf("final message = %q, want %q", got, want)
	}
	if task.Status.Message.Role != "ROLE_AGENT" {
		t.Errorf("final message role = %q, want ROLE_AGENT", task.Status.Message.Role)
	}
	// Terminal state is stable on further polls.
	again := getTask(t, ts, res.Task.ID)
	if again.Status.State != "TASK_STATE_COMPLETED" {
		t.Errorf("post-terminal poll state = %q, want TASK_STATE_COMPLETED", again.Status.State)
	}
}

func TestTaskHonorsClientContextID(t *testing.T) {
	ts := startServer(t)

	params := sendMessageParams("task:1:done", map[string]any{"contextId": "ctx-from-client"})
	res := decodeSendResult(t, rpcCall(t, ts, nil, "SendMessage", params))
	if res.Task == nil {
		t.Fatalf("want task result, got %+v", res)
	}
	if res.Task.ContextID != "ctx-from-client" {
		t.Errorf("contextId = %q, want ctx-from-client", res.Task.ContextID)
	}
	task := getTask(t, ts, res.Task.ID)
	if task.ContextID != "ctx-from-client" {
		t.Errorf("polled contextId = %q, want ctx-from-client", task.ContextID)
	}
	if task.Status.State != "TASK_STATE_COMPLETED" {
		t.Errorf("state after 1 poll = %q, want TASK_STATE_COMPLETED", task.Status.State)
	}
}

func TestTaskIDsAreUnique(t *testing.T) {
	ts := startServer(t)

	first := sendText(t, ts, "task:1:a")
	second := sendText(t, ts, "task:1:b")
	if first.Task == nil || second.Task == nil {
		t.Fatalf("want task results, got %+v and %+v", first, second)
	}
	if first.Task.ID == second.Task.ID {
		t.Errorf("task IDs must be unique, both were %q", first.Task.ID)
	}
	if first.Task.ContextID == second.Task.ContextID {
		t.Errorf("generated contextIds should differ per task, both were %q", first.Task.ContextID)
	}
}

func TestFailBehavior(t *testing.T) {
	ts := startServer(t)

	res := sendText(t, ts, "fail:boom")
	if res.Task == nil {
		t.Fatalf("want task result, got %+v", res)
	}
	if res.Task.Status.State != "TASK_STATE_WORKING" {
		t.Errorf("initial state = %q, want TASK_STATE_WORKING", res.Task.Status.State)
	}
	task := getTask(t, ts, res.Task.ID)
	if task.Status.State != "TASK_STATE_FAILED" {
		t.Fatalf("state after poll = %q, want TASK_STATE_FAILED", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.text() != "boom" {
		t.Errorf("failure message = %+v, want text %q", task.Status.Message, "boom")
	}
}

func TestReturnImmediatelyForcesTaskHandle(t *testing.T) {
	ts := startServer(t)

	msg := sendMessageParams("echo:deferred", nil)
	msg["configuration"] = map[string]any{"returnImmediately": true}
	res := decodeSendResult(t, rpcCall(t, ts, nil, "SendMessage", msg))
	if res.Task == nil {
		t.Fatalf("returnImmediately must produce a task handle, got %+v", res)
	}
	state := res.Task.Status.State
	if state != "TASK_STATE_SUBMITTED" && state != "TASK_STATE_WORKING" {
		t.Errorf("initial state = %q, want submitted or working", state)
	}

	task := getTask(t, ts, res.Task.ID)
	if task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("state on next poll = %q, want TASK_STATE_COMPLETED", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.text() != "Mock response to: deferred" {
		t.Errorf("final message = %+v, want %q", task.Status.Message, "Mock response to: deferred")
	}
}

func TestGetTaskUnknownIDReturnsTaskNotFound(t *testing.T) {
	ts := startServer(t)

	resp := rpcCall(t, ts, nil, "GetTask", map[string]any{"id": "no-such-task"})
	if resp.Error == nil {
		t.Fatalf("want TaskNotFound error, got result %s", resp.Result)
	}
	if resp.Error.Code != -32001 {
		t.Errorf("error code = %d, want -32001 (TaskNotFound)", resp.Error.Code)
	}
}

func TestCancelTask(t *testing.T) {
	ts := startServer(t)

	t.Run("non-terminal task is cancelled", func(t *testing.T) {
		res := sendText(t, ts, "task:5:never")
		if res.Task == nil {
			t.Fatalf("want task result, got %+v", res)
		}
		resp := rpcCall(t, ts, nil, "CancelTask", map[string]any{"id": res.Task.ID})
		if resp.Error != nil {
			t.Fatalf("CancelTask returned error: %+v", resp.Error)
		}
		var task wireTask
		if err := json.Unmarshal(resp.Result, &task); err != nil {
			t.Fatalf("decode cancelled task: %v", err)
		}
		if task.Status.State != "TASK_STATE_CANCELED" {
			t.Errorf("state = %q, want TASK_STATE_CANCELED", task.Status.State)
		}
		// Cancellation is sticky: subsequent polls stay cancelled.
		polled := getTask(t, ts, res.Task.ID)
		if polled.Status.State != "TASK_STATE_CANCELED" {
			t.Errorf("polled state = %q, want TASK_STATE_CANCELED", polled.Status.State)
		}
	})

	t.Run("unknown task id", func(t *testing.T) {
		resp := rpcCall(t, ts, nil, "CancelTask", map[string]any{"id": "no-such-task"})
		if resp.Error == nil {
			t.Fatalf("want TaskNotFound error, got result %s", resp.Result)
		}
		if resp.Error.Code != -32001 {
			t.Errorf("error code = %d, want -32001 (TaskNotFound)", resp.Error.Code)
		}
	})

	t.Run("terminal task is not cancelable", func(t *testing.T) {
		res := sendText(t, ts, "task:1:done")
		if res.Task == nil {
			t.Fatalf("want task result, got %+v", res)
		}
		if got := getTask(t, ts, res.Task.ID); got.Status.State != "TASK_STATE_COMPLETED" {
			t.Fatalf("setup: state = %q, want TASK_STATE_COMPLETED", got.Status.State)
		}
		resp := rpcCall(t, ts, nil, "CancelTask", map[string]any{"id": res.Task.ID})
		if resp.Error == nil {
			t.Fatalf("want TaskNotCancelable error, got result %s", resp.Result)
		}
		if resp.Error.Code != -32002 {
			t.Errorf("error code = %d, want -32002 (TaskNotCancelable)", resp.Error.Code)
		}
	})
}

func TestAPIKeyEnforcement(t *testing.T) {
	ts := startServer(t, WithAPIKey("sekrit"))

	post := func(t *testing.T, headers map[string]string) *http.Response {
		t.Helper()
		body := []byte(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":` +
			`{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"echo:hi"}]}}}`)
		req, err := http.NewRequest(http.MethodPost, ts.URL+A2APath, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		return resp
	}

	t.Run("missing key gets bare 401", func(t *testing.T) {
		resp := post(t, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("401 body = %q, want empty (no JSON-RPC processing)", body)
		}
	})

	t.Run("wrong key gets 401", func(t *testing.T) {
		resp := post(t, map[string]string{"X-API-Key": "wrong"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("correct key is processed", func(t *testing.T) {
		resp := post(t, map[string]string{"X-API-Key": "sekrit"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out rpcResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if out.Error != nil {
			t.Fatalf("unexpected JSON-RPC error: %+v", out.Error)
		}
		var res sendResult
		if err := json.Unmarshal(out.Result, &res); err != nil {
			t.Fatalf("decode send result: %v", err)
		}
		if res.Message == nil || res.Message.text() != "Mock response to: hi" {
			t.Errorf("unexpected result: %s", out.Result)
		}
	})

	t.Run("agent card stays public", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/.well-known/agent-card.json")
		if err != nil {
			t.Fatalf("get card: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("card status = %d, want 200", resp.StatusCode)
		}
	})
}

func TestConcurrentServersAreIsolated(t *testing.T) {
	ts1 := startServer(t)
	ts2 := startServer(t)

	res := sendText(t, ts1, "task:1:one")
	if res.Task == nil {
		t.Fatalf("want task result, got %+v", res)
	}
	resp := rpcCall(t, ts2, nil, "GetTask", map[string]any{"id": res.Task.ID})
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Errorf("server 2 should not know server 1's task, got %+v / %s", resp.Error, resp.Result)
	}
}
