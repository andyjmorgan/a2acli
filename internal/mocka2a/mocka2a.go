// Package mocka2a provides an in-process mock A2A server for e2e testing.
//
// The mock serves the standard agent card discovery endpoint and an A2A v1.0
// JSON-RPC endpoint at /a2a. Wire encoding, method dispatch and error-code
// mapping come from the SDK's a2asrv JSON-RPC transport; only the request
// handling behind it is scripted.
//
// Behaviors are keyed by the text of the incoming user message:
//
//	echo:<text>    -> immediate final Message "Mock response to: <text>"
//	task:<n>:<text>-> Task in state working; completes with agent message
//	                  <text> on the n-th subsequent tasks/get poll
//	fail:<text>    -> like task:1 but the terminal state is failed
//	busy:          -> immediate Message carrying the Agentlings context_busy
//	                  JSON envelope
//	input:<q>      -> Task in state working; the next poll pauses it in
//	                  input-required with <q> as the status message. Answer
//	                  by sending a message with TaskID set: the task resumes
//	                  working and completes with "answered: <answer text>"
//	authreq:<m>    -> like input: but the pause state is auth-required
//	artifact:<t>   -> Task that completes on the first poll carrying one
//	                  artifact "art_1"/"result" with a text part <t>, an
//	                  inline-bytes part data.bin and a structured-data part
//	drop:<n>:<text>-> streaming only: Task (working), <n> working updates,
//	                  then the stream errors without finalizing; ~2 further
//	                  tasks/get polls complete the task with <text>.
//	                  Non-streaming send treats it like task:2:<text>
//	msgfail:       -> streaming only: a final-looking Message event, then a
//	                  Task (working), then a failed status update — the
//	                  adversarial ordering for exit-code truth
//	anything else  -> immediate final Message "Mock response to: <full text>"
//
// If the request's SendMessageConfig.ReturnImmediately is true, message/send
// always returns a Task handle immediately; for immediate-message behaviors
// the final message then arrives via tasks/get on the next poll.
//
// A message whose TaskID references an existing task paused in
// input-required or auth-required continues that task (per the A2A
// continuation flow); referencing an unknown task yields ErrTaskNotFound.
package mocka2a

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// BusyEnvelope is the exact JSON payload returned as the text part of the
// message produced by the "busy:" scripted behavior (Agentlings context_busy
// envelope).
const BusyEnvelope = `{"status":"busy","error":"context_busy","contextId":"ctx_mock","activeTaskId":"task_mock","message":"Context ctx_mock is busy with task task_mock. Retry shortly."}`

const (
	// A2APath is the path the JSON-RPC A2A endpoint is served at.
	A2APath = "/a2a"

	defaultName = "mock-agent"

	apiKeyHeader     = "X-API-Key"
	apiKeySchemeName = "apiKey"
)

// Option customizes a mock Server.
type Option func(*Server)

// WithAPIKey requires every request to the A2A endpoint to carry the header
// "X-API-Key: <key>". Requests missing or mismatching the key receive a bare
// HTTP 401 without any JSON-RPC processing. The agent card endpoint stays
// public. The card additionally advertises an apiKey security scheme.
func WithAPIKey(key string) Option {
	return func(s *Server) { s.apiKey = key }
}

// WithName overrides the agent name served in the agent card.
func WithName(name string) Option {
	return func(s *Server) { s.name = name }
}

// mockTask tracks a scripted task's poll-driven lifecycle.
type mockTask struct {
	task *a2a.Task
	// pollsRemaining is the number of tasks/get calls left before the task
	// transitions to finalState. The poll that brings it to zero already
	// observes the final state.
	pollsRemaining int
	finalState     a2a.TaskState
	finalText      string
	// pauseState, when set, is the non-terminal pause (input-required /
	// auth-required) the countdown reaches instead of finalState; pauseText
	// is the question carried as the status message. A continuation message
	// (Message.TaskID set) resumes the task towards finalState.
	pauseState a2a.TaskState
	pauseText  string
	// artifacts are attached to the task when it completes.
	artifacts []*a2a.Artifact
}

// Server is an in-process mock A2A server. Create it with New and start it
// with Start; the returned httptest.Server is owned by the caller.
type Server struct {
	name      string
	apiKey    string
	streaming bool

	mu    sync.Mutex
	url   string
	tasks map[a2a.TaskID]*mockTask
}

// New creates a mock Server. It does not listen until Start is called.
func New(opts ...Option) *Server {
	s := &Server{
		name:  defaultName,
		tasks: make(map[a2a.TaskID]*mockTask),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start begins serving on a random localhost port and returns the underlying
// httptest.Server. The caller is responsible for closing it. The server
// serves both the agent card (GET /.well-known/agent-card.json) and the A2A
// JSON-RPC endpoint (POST /a2a).
func (s *Server) Start() *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewAgentCardHandler(a2asrv.AgentCardProducerFn(s.card)))
	mux.Handle(A2APath, s.requireAPIKey(a2asrv.NewJSONRPCHandler(&scriptedHandler{s: s})))

	ts := httptest.NewServer(mux)

	s.mu.Lock()
	s.url = ts.URL
	s.mu.Unlock()

	return ts
}

// requireAPIKey rejects A2A requests lacking the configured API key with a
// bare 401 before any JSON-RPC processing happens.
func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	if s.apiKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(apiKeyHeader) != s.apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// card produces the public agent card pointing at this server's own URL.
func (s *Server) card(context.Context) (*a2a.AgentCard, error) {
	s.mu.Lock()
	url := s.url
	s.mu.Unlock()

	card := &a2a.AgentCard{
		Name:                s.name,
		Description:         "Mock A2A agent for e2e testing",
		Version:             "0.0.1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(url+A2APath, a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: s.streaming},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID:          "mock",
			Name:        "Mock skill",
			Description: "Scripted responses for e2e testing",
			Tags:        []string{"mock"},
		}},
	}
	if s.apiKey != "" {
		card.SecuritySchemes = a2a.NamedSecuritySchemes{
			apiKeySchemeName: a2a.APIKeySecurityScheme{
				Description: "API key passed in the X-API-Key header",
				Location:    a2a.APIKeySecuritySchemeLocationHeader,
				Name:        apiKeyHeader,
			},
		}
		card.SecurityRequirements = a2a.SecurityRequirementsOptions{
			{apiKeySchemeName: a2a.SecuritySchemeScopes{}},
		}
	}
	return card, nil
}

// sendMessage implements the scripted message/send behaviors.
func (s *Server) sendMessage(req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	switch {
	case req == nil || req.Message == nil:
		return nil, fmt.Errorf("message is required: %w", a2a.ErrInvalidParams)
	case req.Message.ID == "":
		return nil, fmt.Errorf("message ID is required: %w", a2a.ErrInvalidParams)
	case len(req.Message.Parts) == 0:
		return nil, fmt.Errorf("message parts are required: %w", a2a.ErrInvalidParams)
	case req.Message.Role == "":
		return nil, fmt.Errorf("message role is required: %w", a2a.ErrInvalidParams)
	}

	// Continuation of an existing (paused) task.
	if req.Message.TaskID != "" {
		return s.continueTask(req.Message)
	}

	text := messageText(req.Message)
	contextID := req.Message.ContextID
	if contextID == "" {
		contextID = a2a.NewContextID()
	}
	returnImmediately := req.Config != nil && req.Config.ReturnImmediately

	// Poll-driven task behaviors always return a Task handle.
	if rest, ok := strings.CutPrefix(text, "task:"); ok {
		if n, finalText, ok := parseTaskScript(rest); ok {
			return s.newScriptedTask(req.Message, contextID, a2a.TaskStateWorking, n, a2a.TaskStateCompleted, finalText), nil
		}
	}
	if rest, ok := strings.CutPrefix(text, "fail:"); ok {
		return s.newScriptedTask(req.Message, contextID, a2a.TaskStateWorking, 1, a2a.TaskStateFailed, rest), nil
	}
	if rest, ok := strings.CutPrefix(text, "input:"); ok {
		return s.newPausedTask(req.Message, contextID, a2a.TaskStateInputRequired, rest), nil
	}
	if rest, ok := strings.CutPrefix(text, "authreq:"); ok {
		return s.newPausedTask(req.Message, contextID, a2a.TaskStateAuthRequired, rest), nil
	}
	if rest, ok := strings.CutPrefix(text, "artifact:"); ok {
		return s.newArtifactTask(req.Message, contextID, rest), nil
	}
	if rest, ok := strings.CutPrefix(text, "drop:"); ok {
		// Non-streaming send treats drop:<n>:<text> like task:2:<text>.
		if _, finalText, ok := parseTaskScript(rest); ok {
			return s.newScriptedTask(req.Message, contextID, a2a.TaskStateWorking, 2, a2a.TaskStateCompleted, finalText), nil
		}
	}

	// Immediate-message behaviors.
	var replyText string
	switch {
	case text == "busy:":
		replyText = BusyEnvelope
	default:
		if rest, ok := strings.CutPrefix(text, "echo:"); ok {
			replyText = "Mock response to: " + rest
		} else {
			replyText = "Mock response to: " + text
		}
	}

	if returnImmediately {
		// Always hand back a Task; the final message arrives via tasks/get
		// on the next poll.
		return s.newScriptedTask(req.Message, contextID, a2a.TaskStateSubmitted, 1, a2a.TaskStateCompleted, replyText), nil
	}

	reply := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(replyText))
	reply.ContextID = contextID
	return reply, nil
}

// newScriptedTask registers a new poll-driven task and returns a snapshot of
// its initial state.
func (s *Server) newScriptedTask(userMsg *a2a.Message, contextID string, initial a2a.TaskState, polls int, finalState a2a.TaskState, finalText string) *a2a.Task {
	if polls < 1 {
		polls = 1
	}
	now := time.Now().UTC()
	task := &a2a.Task{
		ID:        a2a.NewTaskID(),
		ContextID: contextID,
		Status:    a2a.TaskStatus{State: initial, Timestamp: &now},
		History:   []*a2a.Message{userMsg},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = &mockTask{
		task:           task,
		pollsRemaining: polls,
		finalState:     finalState,
		finalText:      finalText,
	}
	return snapshotTask(task)
}

// configureTask mutates a registered scripted task under the server lock.
func (s *Server) configureTask(id a2a.TaskID, fn func(*mockTask)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mt, ok := s.tasks[id]; ok {
		fn(mt)
	}
}

// newPausedTask scripts a task that pauses in pauseState (input-required /
// auth-required) with question as its status message on the first poll. A
// later continuation completes it with "answered: <answer>".
func (s *Server) newPausedTask(userMsg *a2a.Message, contextID string, pauseState a2a.TaskState, question string) *a2a.Task {
	t := s.newScriptedTask(userMsg, contextID, a2a.TaskStateWorking, 1, a2a.TaskStateCompleted, "")
	s.configureTask(t.ID, func(mt *mockTask) {
		mt.pauseState = pauseState
		mt.pauseText = question
	})
	return t
}

// newArtifactTask scripts a task that completes on the first poll carrying
// the scripted artifact.
func (s *Server) newArtifactTask(userMsg *a2a.Message, contextID, text string) *a2a.Task {
	t := s.newScriptedTask(userMsg, contextID, a2a.TaskStateWorking, 1, a2a.TaskStateCompleted, text)
	s.configureTask(t.ID, func(mt *mockTask) {
		mt.artifacts = []*a2a.Artifact{scriptedArtifact(text)}
	})
	return t
}

// scriptedArtifact builds the single artifact attached by artifact:<text>:
// a text part, an inline-bytes file part, and a structured-data part.
func scriptedArtifact(text string) *a2a.Artifact {
	raw := a2a.NewRawPart([]byte("hello-bytes"))
	raw.Filename = "data.bin"
	raw.MediaType = "application/octet-stream"
	return &a2a.Artifact{
		ID:    "art_1",
		Name:  "result",
		Parts: a2a.ContentParts{a2a.NewTextPart(text), raw, a2a.NewDataPart(map[string]any{"k": "v"})},
	}
}

// continueTask handles a message that references an existing task: a task
// paused in input-required/auth-required resumes working and completes with
// "answered: <answer>" on the next poll; any other known task just returns
// its current snapshot.
func (s *Server) continueTask(msg *a2a.Message) (a2a.SendMessageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mt, ok := s.tasks[msg.TaskID]
	if !ok {
		return nil, a2a.ErrTaskNotFound
	}
	if st := mt.task.Status.State; st == a2a.TaskStateInputRequired || st == a2a.TaskStateAuthRequired {
		now := time.Now().UTC()
		mt.task.History = append(mt.task.History, msg)
		mt.task.Status = a2a.TaskStatus{State: a2a.TaskStateWorking, Timestamp: &now}
		mt.pollsRemaining = 1
		mt.finalState = a2a.TaskStateCompleted
		mt.finalText = "answered: " + messageText(msg)
	}
	return snapshotTask(mt.task), nil
}

// getTask implements tasks/get with the poll countdown.
func (s *Server) getTask(req *a2a.GetTaskRequest) (*a2a.Task, error) {
	if req == nil || req.ID == "" {
		return nil, fmt.Errorf("missing TaskID: %w", a2a.ErrInvalidParams)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mt, ok := s.tasks[req.ID]
	if !ok {
		return nil, a2a.ErrTaskNotFound
	}

	if !mt.task.Status.State.Terminal() && mt.pollsRemaining > 0 {
		mt.pollsRemaining--
		if mt.pollsRemaining == 0 {
			advanceTask(mt)
		}
	}

	task := snapshotTask(mt.task)
	if req.HistoryLength != nil {
		if n := *req.HistoryLength; n == 0 {
			task.History = []*a2a.Message{}
		} else if n > 0 && n < len(task.History) {
			task.History = task.History[len(task.History)-n:]
		}
	}
	return task, nil
}

// cancelTask implements tasks/cancel.
func (s *Server) cancelTask(req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if req == nil || req.ID == "" {
		return nil, fmt.Errorf("missing TaskID: %w", a2a.ErrInvalidParams)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mt, ok := s.tasks[req.ID]
	if !ok {
		return nil, a2a.ErrTaskNotFound
	}
	if mt.task.Status.State.Terminal() {
		return nil, fmt.Errorf("task in state %q: %w", mt.task.Status.State, a2a.ErrTaskNotCancelable)
	}

	now := time.Now().UTC()
	mt.pollsRemaining = 0
	mt.task.Status = a2a.TaskStatus{State: a2a.TaskStateCanceled, Timestamp: &now}
	return snapshotTask(mt.task), nil
}

// advanceTask moves a drained scripted task to its next scripted state: a
// pending pause when one is configured, the terminal state otherwise.
// Callers must hold s.mu.
func advanceTask(mt *mockTask) {
	if mt.pauseState != "" {
		pauseTask(mt)
		return
	}
	finalizeTask(mt)
}

// pauseTask moves a scripted task to its non-terminal pause state, carrying
// the question as the status message. The pause is one-shot: a continuation
// then drives the task towards its terminal state. Callers must hold s.mu.
func pauseTask(mt *mockTask) {
	q := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(mt.pauseText))
	q.ContextID = mt.task.ContextID
	q.TaskID = mt.task.ID
	now := time.Now().UTC()
	mt.task.History = append(mt.task.History, q)
	mt.task.Status = a2a.TaskStatus{State: mt.pauseState, Message: q, Timestamp: &now}
	mt.pauseState = ""
	mt.pauseText = ""
}

// finalizeTask moves a scripted task to its terminal state, attaching the
// final agent message and any scripted artifacts. Callers must hold s.mu.
func finalizeTask(mt *mockTask) {
	final := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(mt.finalText))
	final.ContextID = mt.task.ContextID
	final.TaskID = mt.task.ID
	now := time.Now().UTC()
	mt.task.History = append(mt.task.History, final)
	mt.task.Status = a2a.TaskStatus{State: mt.finalState, Message: final, Timestamp: &now}
	if mt.finalState == a2a.TaskStateCompleted && len(mt.artifacts) > 0 {
		mt.task.Artifacts = mt.artifacts
	}
}

// paused reports the non-terminal waiting-on-caller states.
func paused(st a2a.TaskState) bool {
	return st == a2a.TaskStateInputRequired || st == a2a.TaskStateAuthRequired
}

// snapshotTask returns a shallow copy of the task with a cloned history so
// callers can marshal it outside the server lock.
func snapshotTask(t *a2a.Task) *a2a.Task {
	cp := *t
	cp.History = append([]*a2a.Message(nil), t.History...)
	return &cp
}

// messageText concatenates the text parts of a message.
func messageText(m *a2a.Message) string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if p == nil {
			continue
		}
		if text, ok := p.Content.(a2a.Text); ok {
			sb.WriteString(string(text))
		}
	}
	return sb.String()
}

// parseTaskScript parses "<n>:<text>" from a "task:" prefixed message.
func parseTaskScript(rest string) (polls int, text string, ok bool) {
	nStr, text, found := strings.Cut(rest, ":")
	if !found {
		return 0, "", false
	}
	n, err := strconv.Atoi(nStr)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, text, true
}

// scriptedHandler adapts Server's scripted behaviors to a2asrv.RequestHandler
// so the SDK's JSON-RPC transport provides protocol conformance (method
// names, envelopes and error-code mapping).
type scriptedHandler struct {
	s *Server
}

var _ a2asrv.RequestHandler = (*scriptedHandler)(nil)

func (h *scriptedHandler) SendMessage(_ context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	return h.s.sendMessage(req)
}

func (h *scriptedHandler) GetTask(_ context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	return h.s.getTask(req)
}

func (h *scriptedHandler) CancelTask(_ context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	return h.s.cancelTask(req)
}

func (h *scriptedHandler) ListTasks(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	return nil, a2a.ErrUnsupportedOperation
}

func (h *scriptedHandler) SubscribeToTask(ctx context.Context, req *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return h.s.subscribeToTask(ctx, req)
}

func (h *scriptedHandler) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return h.s.sendStreamingMessage(ctx, req)
}

func (h *scriptedHandler) GetTaskPushConfig(context.Context, *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

func (h *scriptedHandler) ListTaskPushConfigs(context.Context, *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

func (h *scriptedHandler) CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

func (h *scriptedHandler) DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error {
	return a2a.ErrPushNotificationNotSupported
}

func (h *scriptedHandler) GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return nil, a2a.ErrExtendedCardNotConfigured
}

func unsupportedStream(yield func(a2a.Event, error) bool) {
	yield(nil, a2a.ErrUnsupportedOperation)
}
