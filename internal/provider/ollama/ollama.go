package ollama

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

const QueueGroup = "provider-ollama"

// Adapter implements provider.Provider for Ollama's OpenAI-compatible API.
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
			Timeout: 120 * time.Second, // longer timeout for local inference
		},
		log: log,
	}
}

func (a *Adapter) Name() string { return "ollama" }

// ChatCompletion calls the upstream Ollama API (OpenAI-compatible endpoint).
func (a *Adapter) ChatCompletion(ctx context.Context, req *api.ProviderRequest) (*api.ChatResponse, error) {
	body := map[string]any{
		"model":    req.UpstreamModel,
		"messages": req.Request.Messages,
	}
	if req.Request.Temperature != nil {
		body["temperature"] = *req.Request.Temperature
	}
	if req.Request.MaxTokens != nil {
		body["max_tokens"] = *req.Request.MaxTokens
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := a.cfg.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Ollama does not require auth headers

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

	var chatResp api.ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &chatResp, nil
}

// Subscribe registers this adapter as a NATS subscriber on llm.provider.ollama.
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
