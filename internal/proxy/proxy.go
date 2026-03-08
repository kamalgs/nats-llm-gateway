package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kamalgs/infermesh/api"
	"github.com/nats-io/nats.go"
)

const RequestTimeout = 30 * time.Second

// Proxy translates OpenAI-compatible HTTP requests to NATS messages.
// Model names use the convention "provider.model" (e.g., "openai.gpt-4o").
// The proxy splits the name and publishes directly to llm.provider.<provider>.
type Proxy struct {
	nc     *nats.Conn
	server *http.Server
	log    *slog.Logger
}

func New(nc *nats.Conn, addr string, log *slog.Logger) *Proxy {
	p := &Proxy{nc: nc, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", p.handleChatCompletion)
	mux.HandleFunc("GET /health", p.handleHealth)

	p.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return p
}

func (p *Proxy) Start() error {
	p.log.Info("http proxy listening", "addr", p.server.Addr)
	return p.server.ListenAndServe()
}

func (p *Proxy) Stop(ctx context.Context) error {
	return p.server.Shutdown(ctx)
}

// parseModel splits "provider.model" into provider and upstream model name.
// Returns an error if the model name doesn't contain a dot.
func parseModel(model string) (provider, upstream string, err error) {
	i := strings.IndexByte(model, '.')
	if i <= 0 || i == len(model)-1 {
		return "", "", fmt.Errorf("model %q must be in the form provider.model (e.g., openai.gpt-4o)", model)
	}
	return model[:i], model[i+1:], nil
}

func (p *Proxy) handleChatCompletion(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}

	var req api.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}

	provider, upstream, err := parseModel(req.Model)
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Build provider request with the upstream model name
	provReq := api.ProviderRequest{
		UpstreamModel: upstream,
		Request:       req,
	}
	data, err := json.Marshal(provReq)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "failed to marshal request")
		return
	}

	subject := "llm.provider." + provider
	p.log.Info("proxying request", "provider", provider, "upstream_model", upstream, "subject", subject)

	msg, err := p.nc.Request(subject, data, RequestTimeout)
	if err != nil {
		p.log.Error("nats request failed", "error", err)
		p.writeError(w, http.StatusBadGateway, "provider_error", "provider request failed: "+err.Error())
		return
	}

	// Check if the response is an error
	var errResp api.ErrorResponse
	if err := json.Unmarshal(msg.Data, &errResp); err == nil && errResp.Error.Code != "" {
		status := http.StatusInternalServerError
		switch errResp.Error.Code {
		case "model_not_found":
			status = http.StatusNotFound
		case "rate_limit_exceeded":
			status = http.StatusTooManyRequests
		case "invalid_request":
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(msg.Data)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(msg.Data)
}

func (p *Proxy) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (p *Proxy) writeError(w http.ResponseWriter, status int, code, message string) {
	p.log.Error("proxy error", "status", status, "code", code, "message", message)
	errResp := api.ErrorResponse{
		Error: api.APIError{
			Message: message,
			Type:    "error",
			Code:    code,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp)
}
