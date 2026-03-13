package provider

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kamalgs/infermesh/api"
	"github.com/kamalgs/infermesh/internal/testutil"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	name string
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) ChatCompletion(_ context.Context, req *api.ProviderRequest) (*api.ChatResponse, error) {
	content := "mock response"
	if len(req.Request.Messages) > 0 {
		last := req.Request.Messages[len(req.Request.Messages)-1]
		if last.Content != "" {
			content = "echo: " + last.Content
		}
	}
	return &api.ChatResponse{
		ID:      "mock-1",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.UpstreamModel,
		Choices: []api.Choice{{
			Index:        0,
			Message:      &api.Message{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: &api.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}, nil
}

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSessionHandler_TextMode(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	log := silentLog()

	adapter := &mockProvider{name: "test"}
	handler := NewSessionHandler(adapter, nc, log)
	defer handler.Close()
	subs, err := handler.Subscribe("test-q")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() {
		for _, s := range subs {
			s.Drain()
		}
	}()

	req := api.ProviderRequest{
		UpstreamModel: "model-1",
		Request: api.ChatRequest{
			Model:    "model-1",
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(req)

	msg, err := nc.Request("llm.chat.model-1", data, 5*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var resp api.ChatResponse
	json.Unmarshal(msg.Data, &resp)

	if resp.Choices[0].Message.Content != "echo: hello" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.SessionID == "" {
		t.Error("expected session_id in response")
	}
	if resp.SessionSubject == "" {
		t.Error("expected session_subject in response")
	}
}

func TestSessionHandler_SessionContinuation(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	log := silentLog()

	adapter := &mockProvider{name: "test"}
	handler := NewSessionHandler(adapter, nc, log)
	defer handler.Close()
	subs, err := handler.Subscribe("test-q")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() {
		for _, s := range subs {
			s.Drain()
		}
	}()

	// First request: create session.
	req := api.ProviderRequest{
		UpstreamModel: "model-1",
		Request: api.ChatRequest{
			Model:    "model-1",
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(req)
	msg, err := nc.Request("llm.chat.model-1", data, 5*time.Second)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	var resp1 api.ChatResponse
	json.Unmarshal(msg.Data, &resp1)

	// Second request: use session subject.
	req2 := api.ProviderRequest{
		UpstreamModel: "model-1",
		Request: api.ChatRequest{
			Model:     "model-1",
			Messages:  []api.Message{{Role: "user", Content: "world"}},
			SessionID: resp1.SessionID,
		},
	}
	data2, _ := json.Marshal(req2)
	msg2, err := nc.Request(resp1.SessionSubject, data2, 5*time.Second)
	if err != nil {
		t.Fatalf("session request: %v", err)
	}

	var resp2 api.ChatResponse
	json.Unmarshal(msg2.Data, &resp2)

	if resp2.SessionID != resp1.SessionID {
		t.Errorf("session_id mismatch: %q != %q", resp2.SessionID, resp1.SessionID)
	}
}

func TestSessionHandler_RejectSystemMessageInSession(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	log := silentLog()

	adapter := &mockProvider{name: "test"}
	handler := NewSessionHandler(adapter, nc, log)
	defer handler.Close()
	subs, err := handler.Subscribe("test-q")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() {
		for _, s := range subs {
			s.Drain()
		}
	}()

	// First request: create session.
	req := api.ProviderRequest{
		UpstreamModel: "model-1",
		Request: api.ChatRequest{
			Model:    "model-1",
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(req)
	msg, err := nc.Request("llm.chat.model-1", data, 5*time.Second)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	var resp1 api.ChatResponse
	json.Unmarshal(msg.Data, &resp1)

	// Second request: send system message via session subject (should be rejected).
	req2 := api.ProviderRequest{
		UpstreamModel: "model-1",
		Request: api.ChatRequest{
			Model:     "model-1",
			Messages:  []api.Message{{Role: "system", Content: "new system prompt"}},
			SessionID: resp1.SessionID,
		},
	}
	data2, _ := json.Marshal(req2)
	msg2, err := nc.Request(resp1.SessionSubject, data2, 5*time.Second)
	if err != nil {
		t.Fatalf("session request: %v", err)
	}

	var errResp api.ErrorResponse
	json.Unmarshal(msg2.Data, &errResp)

	if errResp.Error.Code != "invalid_request" {
		t.Errorf("expected invalid_request error, got %q", errResp.Error.Code)
	}
	if errResp.Error.Message == "" {
		t.Error("expected error message about system messages")
	}
}

func TestSessionHandler_ModelSpecificSubscription(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	log := silentLog()

	adapter := &mockProvider{name: "test"}
	handler := NewSessionHandler(adapter, nc, log)
	defer handler.Close()

	// Subscribe to a specific model only.
	subs, err := handler.Subscribe("test-q", "my-model")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() {
		for _, s := range subs {
			s.Drain()
		}
	}()

	// Request to the subscribed model should work.
	req := api.ProviderRequest{
		UpstreamModel: "my-model",
		Request: api.ChatRequest{
			Model:    "my-model",
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(req)

	msg, err := nc.Request("llm.chat.my-model", data, 5*time.Second)
	if err != nil {
		t.Fatalf("request to subscribed model: %v", err)
	}

	var resp api.ChatResponse
	json.Unmarshal(msg.Data, &resp)
	if resp.Choices[0].Message.Content != "echo: hello" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}

	// Request to a different model should timeout (no subscriber).
	_, err = nc.Request("llm.chat.other-model", data, 200*time.Millisecond)
	if err == nil {
		t.Error("expected timeout for non-subscribed model")
	}
}
