package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kamalgs/infermesh/api"
	"github.com/kamalgs/infermesh/internal/config"
	"github.com/kamalgs/infermesh/internal/provider"
	"github.com/nats-io/nats.go"
)

const QueueGroup = "provider-anthropic"

// Adapter implements provider.Provider for the Anthropic Messages API.
type Adapter struct {
	cfg    config.ProviderConfig
	client *http.Client
	log    *slog.Logger
}

var _ provider.Provider = (*Adapter)(nil)

func NewAdapter(cfg config.ProviderConfig, log *slog.Logger) *Adapter {
	return &Adapter{
		cfg: cfg,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		log: log,
	}
}

func (a *Adapter) Name() string { return "anthropic" }

// anthropicRequest is the Anthropic Messages API request format.
type anthropicRequest struct {
	Model     string        `json:"model"`
	Messages  []api.Message `json:"messages"`
	System    string        `json:"system,omitempty"`
	MaxTokens int           `json:"max_tokens"`
}

// anthropicResponse is the Anthropic Messages API response format.
type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Model      string             `json:"model"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ChatCompletion calls the upstream Anthropic Messages API and translates
// the response into the unified ChatResponse format.
func (a *Adapter) ChatCompletion(ctx context.Context, req *api.ProviderRequest) (*api.ChatResponse, error) {
	// Extract system messages from the messages array
	var system string
	var messages []api.Message
	for _, m := range req.Request.Messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			messages = append(messages, m)
		}
	}

	maxTokens := 4096
	if req.Request.MaxTokens != nil {
		maxTokens = *req.Request.MaxTokens
	}

	anthReq := anthropicRequest{
		Model:     req.UpstreamModel,
		Messages:  messages,
		System:    system,
		MaxTokens: maxTokens,
	}

	data, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := a.cfg.BaseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if a.cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	}

	a.log.Info("calling upstream", "url", url, "model", req.UpstreamModel)

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d: %s", httpResp.StatusCode, string(respBody))
	}

	var anthResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	// Map Anthropic response to unified ChatResponse format
	content := ""
	if len(anthResp.Content) > 0 {
		content = anthResp.Content[0].Text
	}

	finishReason := "stop"
	if anthResp.StopReason == "max_tokens" {
		finishReason = "length"
	}

	totalTokens := anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens

	return &api.ChatResponse{
		ID:      anthResp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   anthResp.Model,
		Choices: []api.Choice{{
			Index:        0,
			Message:      &api.Message{Role: "assistant", Content: content},
			FinishReason: finishReason,
		}},
		Usage: &api.Usage{
			PromptTokens:     anthResp.Usage.InputTokens,
			CompletionTokens: anthResp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
		},
	}, nil
}

// Subscribe registers this adapter as a NATS subscriber on llm.provider.anthropic.
func (a *Adapter) Subscribe(nc *nats.Conn) (*nats.Subscription, error) {
	subject := "llm.provider." + a.Name() + ".>"
	sub, err := nc.QueueSubscribe(subject, QueueGroup, func(msg *nats.Msg) {
		a.handleMessage(msg)
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe %s: %w", subject, err)
	}
	a.log.Info("provider adapter listening", "subject", subject, "queue", QueueGroup)
	return sub, nil
}

func (a *Adapter) handleMessage(msg *nats.Msg) {
	var req api.ProviderRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		a.replyError(msg, "invalid_request", "failed to parse provider request: "+err.Error())
		return
	}

	resp, err := a.ChatCompletion(context.Background(), &req)
	if err != nil {
		a.replyError(msg, "provider_error", err.Error())
		return
	}

	data, err := json.Marshal(resp)
	if err != nil {
		a.replyError(msg, "internal_error", "failed to marshal response")
		return
	}
	_ = msg.Respond(data)
}

func (a *Adapter) replyError(msg *nats.Msg, code, message string) {
	a.log.Error("provider error", "code", code, "message", message)
	errResp := api.ErrorResponse{
		Error: api.APIError{
			Message: message,
			Type:    "error",
			Code:    code,
		},
	}
	data, _ := json.Marshal(errResp)
	_ = msg.Respond(data)
}
