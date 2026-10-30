// Package scriptedmodel is a deterministic stand-in for a language model used
// by the zero-key demo and by end-to-end tests. It is not an LLM: it follows a
// fixed script (list the orders, refund the order the customer named with the
// tool the order total requires, then report the outcome) so the platform's
// wiring, approvals and crash recovery can be exercised without model
// credentials. Every reply it writes starts with "[scripted]" so nobody mistakes
// it for the real agent.
package scriptedmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// provider implements model.Provider with the fixed script.
	provider struct{}

	// stream replays a precomputed list of chunks and its canonical response.
	stream struct {
		// chunks are returned by Recv in order.
		chunks []model.Chunk
		// next is the index of the next chunk.
		next int
		// response is the canonical response matching chunks.
		response *model.Response
	}

	// toolOutcome is the part of a tool result the script reads.
	toolOutcome struct {
		// Orders lists orders returned by list_orders.
		Orders []struct {
			ID         string `json:"id"`
			TotalCents int64  `json:"total_cents"`
		} `json:"orders"`
		// Status is the refund status returned by refund tools.
		Status string `json:"status"`
	}
)

// storeCreditLimitCents mirrors the business rule only to pick the tool the
// server will accept on the first try; the server still enforces it.
const storeCreditLimitCents = 5000

// orderMention finds an order ID such as "o-2" in the customer's message.
var orderMention = regexp.MustCompile(`\bo-[A-Za-z0-9]+\b`)

// New returns the scripted model client.
func New() (model.Client, error) {
	return model.NewClient(provider{})
}

// Complete is not used by the refund planner.
func (provider) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, errors.New("scripted model supports streaming only")
}

// Stream returns the script's next step for the conversation so far.
func (provider) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	call, text, err := nextStep(req)
	if err != nil {
		return nil, err
	}
	msg := model.Message{Role: model.ConversationRoleAssistant}
	resp := &model.Response{StopReason: "end_turn"}
	var chunks []model.Chunk
	if text != "" {
		msg.Parts = append(msg.Parts, model.TextPart{Text: text})
		chunks = append(chunks, model.TextChunk{Message: model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: text}},
		}})
	}
	if call != nil {
		msg.Parts = append(msg.Parts, model.ToolUsePart{ID: call.ID, Name: string(call.Name), Input: call.Payload})
		chunks = append(chunks, model.ToolCallChunk{ToolCall: *call})
		resp.StopReason = "tool_use"
	}
	chunks = append(chunks, model.StopChunk{Reason: resp.StopReason})
	resp.Content = []model.Message{msg}
	return &stream{chunks: chunks, response: resp}, nil
}

// nextStep decides the script's next tool call, or its final text when no
// tool call remains.
func nextStep(req *model.Request) (*model.ToolCall, string, error) {
	orderID := mentionedOrder(req.Messages)
	last := lastToolResult(req.Messages)
	switch {
	case orderID == "":
		return nil, "[scripted] Which order would you like refunded? Please give its ID, such as o-2.", nil
	case last == nil && len(req.Tools) > 0:
		name, err := toolNamed(req, "list_orders")
		if err != nil {
			return nil, "", err
		}
		return &model.ToolCall{ID: "call-list", Name: name, Payload: rawjson.Message(`{}`)}, "", nil
	case last != nil && last.IsError:
		return nil, fmt.Sprintf("[scripted] I could not refund order %s: %v", orderID, last.Content), nil
	}
	var out toolOutcome
	if err := decodeContent(last, &out); err != nil {
		return nil, "", err
	}
	if out.Status != "" {
		return nil, finalText(orderID, out.Status), nil
	}
	for _, o := range out.Orders {
		if o.ID != orderID {
			continue
		}
		tool := "issue_store_credit"
		if o.TotalCents > storeCreditLimitCents {
			tool = "request_refund"
		}
		name, err := toolNamed(req, tool)
		if err != nil {
			return nil, "", err
		}
		payload, err := json.Marshal(map[string]string{"order_id": orderID})
		if err != nil {
			return nil, "", fmt.Errorf("encode refund arguments: %w", err)
		}
		return &model.ToolCall{ID: "call-refund", Name: name, Payload: payload}, "", nil
	}
	return nil, fmt.Sprintf("[scripted] I could not find order %s among your orders.", orderID), nil
}

// finalText reports a refund outcome to the customer.
func finalText(orderID, status string) string {
	if status == "denied" {
		return fmt.Sprintf("[scripted] A manager declined the refund for order %s. Please contact support.", orderID)
	}
	return fmt.Sprintf("[scripted] Order %s has been refunded.", orderID)
}

// mentionedOrder returns the first order ID in the customer's messages.
func mentionedOrder(msgs []*model.Message) string {
	for _, m := range msgs {
		if m.Role != model.ConversationRoleUser {
			continue
		}
		for _, p := range m.Parts {
			if t, ok := p.(model.TextPart); ok {
				if id := orderMention.FindString(t.Text); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

// lastToolResult returns the newest tool result in the conversation.
func lastToolResult(msgs []*model.Message) *model.ToolResultPart {
	for i := len(msgs) - 1; i >= 0; i-- {
		for j := len(msgs[i].Parts) - 1; j >= 0; j-- {
			if r, ok := msgs[i].Parts[j].(model.ToolResultPart); ok {
				return &r
			}
		}
	}
	return nil
}

// toolNamed returns the advertised tool whose name ends with suffix; provider
// adapters may prefix or sanitize names.
func toolNamed(req *model.Request, suffix string) (tools.Ident, error) {
	for _, t := range req.Tools {
		if strings.HasSuffix(t.Name, suffix) {
			return tools.Ident(t.Name), nil
		}
	}
	return "", fmt.Errorf("tool %q is not advertised", suffix)
}

// decodeContent decodes a tool result's JSON-compatible content.
func decodeContent(r *model.ToolResultPart, out *toolOutcome) error {
	b, err := json.Marshal(r.Content)
	if err != nil {
		return fmt.Errorf("encode tool result: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	return nil
}

// Recv returns the next chunk or io.EOF.
func (s *stream) Recv() (model.Chunk, error) {
	if s.next >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.next]
	s.next++
	return c, nil
}

// Response returns the canonical response once the stream is drained.
func (s *stream) Response() *model.Response {
	return s.response
}

// Close releases nothing; the stream holds no resources.
func (s *stream) Close() error {
	return nil
}
