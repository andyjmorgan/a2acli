package client

import (
	"context"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Default unary deadlines: a hung tool call kills an AI agent's whole turn,
// so no RPC may block indefinitely. Blocking message/send is legitimately
// slow (the server may run the task inline) and gets a generous cap; task
// lookups are quick. Callers with their own deadline are respected as-is.
const (
	sendTimeout  = 5 * time.Minute
	unaryTimeout = 30 * time.Second
)

// withDeadline adds a default deadline when the caller has none.
func withDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// SendOptions shape one message/send call.
type SendOptions struct {
	ContextID string
	// TaskID continues an existing task — the protocol's answer to
	// input-required/auth-required pauses and follow-up refinements.
	TaskID            string
	ReturnImmediately bool
	// Parts are extra parts (files, data) sent after the text part.
	Parts []*a2a.Part
	// AcceptedOutputModes tells the agent which media types the caller can
	// handle (SendMessageConfiguration.acceptedOutputModes).
	AcceptedOutputModes []string
}

func buildMessage(text string, opts SendOptions) *a2a.Message {
	parts := make([]*a2a.Part, 0, 1+len(opts.Parts))
	if text != "" || len(opts.Parts) == 0 {
		parts = append(parts, a2a.NewTextPart(text))
	}
	parts = append(parts, opts.Parts...)
	msg := a2a.NewMessage(a2a.MessageRoleUser, parts...)
	msg.ContextID = opts.ContextID
	msg.TaskID = a2a.TaskID(opts.TaskID)
	return msg
}

func buildRequest(text string, opts SendOptions) *a2a.SendMessageRequest {
	req := &a2a.SendMessageRequest{Message: buildMessage(text, opts)}
	if opts.ReturnImmediately || len(opts.AcceptedOutputModes) > 0 {
		req.Config = &a2a.SendMessageConfig{
			ReturnImmediately:   opts.ReturnImmediately,
			AcceptedOutputModes: opts.AcceptedOutputModes,
		}
	}
	return req
}

// Send performs a non-streaming message/send.
func (c *Conn) Send(ctx context.Context, text string, opts SendOptions) (a2a.SendMessageResult, error) {
	ctx, cancel := withDeadline(ctx, sendTimeout)
	defer cancel()
	return c.Client.SendMessage(ctx, buildRequest(text, opts))
}

// GetTask fetches current task state. historyLength < 0 leaves the server
// default; >= 0 requests that many history messages.
func (c *Conn) GetTask(ctx context.Context, id string) (*a2a.Task, error) {
	return c.GetTaskHistory(ctx, id, -1)
}

// GetTaskHistory is GetTask with an explicit history-length request.
func (c *Conn) GetTaskHistory(ctx context.Context, id string, historyLength int) (*a2a.Task, error) {
	ctx, cancel := withDeadline(ctx, unaryTimeout)
	defer cancel()
	req := &a2a.GetTaskRequest{ID: a2a.TaskID(id)}
	if historyLength >= 0 {
		req.HistoryLength = &historyLength
	}
	return c.Client.GetTask(ctx, req)
}

// CancelTask requests cancellation and returns the server-supplied task
// state. Callers should not assume an observable intermediate "cancelling"
// state; confirm terminal state via polling.
func (c *Conn) CancelTask(ctx context.Context, id string) (*a2a.Task, error) {
	ctx, cancel := withDeadline(ctx, unaryTimeout)
	defer cancel()
	return c.Client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: a2a.TaskID(id)})
}

// ListOptions shape one ListTasks call.
type ListOptions struct {
	ContextID string
	Status    a2a.TaskState // "" = any
	PageSize  int           // 0 = server default (spec: 50, max 100)
	PageToken string
}

// ListTasks queries the agent's task list (A2A v1.0 ListTasks): every task
// visible to this caller, newest first — not only the ones this machine
// delegated.
func (c *Conn) ListTasks(ctx context.Context, o ListOptions) (*a2a.ListTasksResponse, error) {
	ctx, cancel := withDeadline(ctx, unaryTimeout)
	defer cancel()
	return c.Client.ListTasks(ctx, &a2a.ListTasksRequest{
		ContextID: o.ContextID,
		Status:    o.Status,
		PageSize:  o.PageSize,
		PageToken: o.PageToken,
	})
}

// ExtendedCard fetches the authenticated extended agent card.
func (c *Conn) ExtendedCard(ctx context.Context) (*a2a.AgentCard, error) {
	ctx, cancel := withDeadline(ctx, unaryTimeout)
	defer cancel()
	return c.Client.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
}
