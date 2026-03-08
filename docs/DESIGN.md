# NATS LLM Gateway — Requirements & Design

## 1. Overview

NATS LLM Gateway is a lightweight, high-performance gateway that sits between
clients and LLM providers. It exposes an **OpenAI-compatible Chat Completion
API** over HTTP and routes requests to downstream LLM providers through
**NATS** as the internal messaging backbone.

```
┌──────────┐   HTTP/OpenAI-compat    ┌─────────────────────┐
│  Client  │ ──────────────────────► │   Ingress (HTTP)    │
└──────────┘                         └────────┬────────────┘
                                              │  publish
                                              ▼
                                     ┌─────────────────────┐
                                     │     NATS Core /     │
                                     │     JetStream       │
                                     └────────┬────────────┘
                                              │  subscribe
                          ┌───────────────────┼───────────────────┐
                          ▼                   ▼                   ▼
                   ┌────────────┐     ┌────────────┐      ┌────────────┐
                   │  Provider  │     │  Provider  │      │  Provider  │
                   │  Adapter:  │     │  Adapter:  │      │  Adapter:  │
                   │  OpenAI    │     │  Anthropic │      │  Ollama    │
                   └────────────┘     └────────────┘      └────────────┘
```

### Why NATS?

| Concern | How NATS helps |
|---|---|
| **Decoupling** | Ingress and provider adapters are independent services; add/remove providers without touching the gateway. |
| **Load distribution** | NATS queue groups give automatic load balancing across adapter replicas. |
| **Scalability** | Horizontally scale any component; NATS cluster handles fan-out. |
| **Resilience** | JetStream provides at-least-once delivery, replay, and persistence. |
| **Streaming** | NATS subjects map naturally to SSE/streaming token delivery. |

---

## 2. Goals

1. **Drop-in OpenAI compatibility** — any client that speaks `POST /v1/chat/completions` works unchanged.
2. **Multi-provider routing** — route to OpenAI, Anthropic, Google, Ollama, vLLM, or any provider via pluggable adapters.
3. **Model aliasing & mapping** — expose virtual model names that map to real provider models.
4. **Streaming first** — SSE streaming by default with non-streaming fallback.
5. **Rate limiting** — per-key, per-model, and global rate limits.
6. **Authentication & authorization** — API-key auth with configurable policies.
7. **Observability** — structured logging, Prometheus metrics, OpenTelemetry traces.
8. **Minimal operational overhead** — single binary, config-file driven, Docker/K8s ready.

---

## 3. High-Level Requirements

### 3.1 Functional Requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-1 | Accept OpenAI Chat Completion requests (`POST /v1/chat/completions`) | P0 |
| FR-2 | Support streaming (`stream: true`) via SSE | P0 |
| FR-3 | Route requests to the correct provider adapter based on `model` field | P0 |
| FR-4 | Provider adapters for OpenAI, Anthropic, Ollama | P0 |
| FR-5 | Model aliasing — map virtual model names to provider:model pairs | P1 |
| FR-6 | API key authentication on inbound requests | P0 |
| FR-7 | Per-key and per-model rate limiting | P0 |
| FR-8 | Return OpenAI-compatible response and error formats | P0 |
| FR-9 | Health check endpoint (`GET /health`) | P1 |
| FR-10 | List available models (`GET /v1/models`) | P1 |
| FR-11 | Request/response logging with redaction of sensitive fields | P1 |
| FR-12 | Graceful shutdown with in-flight request draining | P1 |
| FR-13 | Google Vertex / Gemini provider adapter | P2 |
| FR-14 | Tool/function calling pass-through | P2 |
| FR-15 | Provider failover — retry on a secondary provider if primary fails | P2 |

### 3.2 Non-Functional Requirements

| ID | Requirement | Target |
|----|-------------|--------|
| NFR-1 | P99 gateway-added latency (excluding LLM time) | < 5 ms |
| NFR-2 | Concurrent request capacity | 10 000+ |
| NFR-3 | Configuration hot-reload without restart | Yes |
| NFR-4 | Single statically-linked binary | Yes |
| NFR-5 | Container image size | < 30 MB |

---

## 4. Architecture

### 4.1 Components

```
nats-llm-gateway/
├── cmd/
│   └── gateway/          # main entry point
├── internal/
│   ├── ingress/          # HTTP server, OpenAI-compat handlers
│   ├── auth/             # API-key validation, middleware
│   ├── ratelimit/        # Token-bucket / sliding-window limiter
│   ├── router/           # Model → NATS subject resolver
│   ├── natsbus/          # NATS connection, pub/sub helpers
│   ├── provider/         # Provider adapter interface + implementations
│   │   ├── openai/
│   │   ├── anthropic/
│   │   └── ollama/
│   ├── config/           # Config loading, validation, hot-reload
│   └── observability/    # Metrics, tracing, structured logging
├── configs/
│   └── gateway.yaml      # Reference configuration
├── docs/
│   └── DESIGN.md         # This document
├── Dockerfile
├── go.mod
└── go.sum
```

### 4.2 Request Flow

```
Client ──HTTP──► Ingress ──► AuthMiddleware ──► RateLimiter
                                                    │
                                                    ▼
                                               Router.Resolve(model)
                                                    │
                                                    ▼  NATS Publish
                                           ┌────────────────────┐
                                           │  llm.req.<provider> │
                                           └────────┬───────────┘
                                                    │
                                           Provider Adapter (subscriber)
                                                    │
                                                    ▼  Call upstream LLM API
                                           ┌────────────────────┐
                                           │  LLM Provider API  │
                                           └────────┬───────────┘
                                                    │
                                           Reply via NATS
                                                    │
                                           ◄────────┘
                                           Ingress streams SSE back to client
```

### 4.3 NATS Subject Design

| Subject Pattern | Purpose |
|---|---|
| `llm.req.<provider>` | Request queue — provider adapters subscribe via queue group |
| `llm.resp.<request_id>` | Ephemeral reply subject for streaming chunks back to ingress |
| `llm.admin.reload` | Publish to trigger config hot-reload across all components |
| `llm.metrics.<component>` | Internal metrics events (optional) |

- Provider adapters subscribe to `llm.req.<provider>` using **queue groups** so
  multiple replicas of the same adapter share load automatically.
- For streaming, the ingress creates a unique inbox (`llm.resp.<req_id>`) and
  the adapter publishes token chunks to it. The ingress relays these as SSE
  events to the HTTP client.

### 4.4 Configuration

```yaml
# configs/gateway.yaml
server:
  listen: ":8080"
  read_timeout: 30s
  write_timeout: 120s

nats:
  url: "nats://localhost:4222"
  # Optional JetStream for persistence
  jetstream:
    enabled: false
    stream_name: "LLM_REQUESTS"

auth:
  # API keys with optional metadata
  keys:
    - key: "sk-proj-abc123"
      name: "frontend-app"
      rate_limit: "100/min"
      allowed_models: ["gpt-4o", "claude-sonnet"]
    - key: "sk-proj-def456"
      name: "batch-service"
      rate_limit: "1000/min"

rate_limit:
  global: "5000/min"
  per_model:
    "gpt-4o": "500/min"
    "claude-sonnet": "1000/min"

models:
  # Virtual model name → provider routing
  "gpt-4o":
    provider: openai
    upstream_model: "gpt-4o"
  "claude-sonnet":
    provider: anthropic
    upstream_model: "claude-sonnet-4-6-20250514"
  "llama3":
    provider: ollama
    upstream_model: "llama3:70b"

providers:
  openai:
    base_url: "https://api.openai.com/v1"
    api_key: "${OPENAI_API_KEY}"
  anthropic:
    base_url: "https://api.anthropic.com"
    api_key: "${ANTHROPIC_API_KEY}"
  ollama:
    base_url: "http://localhost:11434"
```

### 4.5 Authentication

- Inbound requests must include `Authorization: Bearer <api-key>`.
- The auth middleware validates the key against the configured key store.
- Each key can have:
  - **Allowed models** — restrict which models the key can access.
  - **Rate limit override** — per-key rate limits.
  - **Metadata** — name, team, tags for logging/metrics.
- Future: pluggable auth backends (database, OIDC, NATS-based auth).

### 4.6 Rate Limiting

Two-tier rate limiting using a **sliding window** algorithm:

1. **Per-key limits** — configured per API key (e.g., `100/min`).
2. **Per-model limits** — global limit across all keys for a given model.
3. **Global limit** — overall gateway request cap.

Rate limit state is stored in-memory by default. For multi-instance
deployments, state can be shared via NATS KV store.

When a limit is exceeded, the gateway returns:
```json
{
  "error": {
    "message": "Rate limit exceeded. Retry after 2s.",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  }
}
```
With headers: `Retry-After`, `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`.

---

## 5. Technology Choices

| Component | Choice | Rationale |
|---|---|---|
| Language | **Go** | Single binary, excellent concurrency, NATS has first-class Go client |
| HTTP framework | `net/http` (stdlib) | Minimal dependencies, good enough for this use case |
| NATS client | `github.com/nats-io/nats.go` | Official client |
| Config | `github.com/knadh/koanf` | Hot-reload, env var substitution, YAML |
| Logging | `log/slog` (stdlib) | Structured, zero-dependency |
| Metrics | `github.com/prometheus/client_golang` | Industry standard |
| Rate limiting | Custom (sliding window over NATS KV) | Distributed-friendly |

---

## 6. Milestones

### M1 — Walking Skeleton
- [ ] Project scaffolding (Go module, directory structure)
- [ ] NATS connection management
- [ ] HTTP ingress with `/v1/chat/completions` endpoint
- [ ] Single provider adapter (OpenAI pass-through)
- [ ] End-to-end non-streaming request/response

### M2 — Streaming & Multi-Provider
- [ ] SSE streaming support
- [ ] Anthropic provider adapter (Messages API → OpenAI format translation)
- [ ] Ollama provider adapter
- [ ] Model aliasing and routing

### M3 — Auth & Rate Limiting
- [ ] API key authentication middleware
- [ ] Per-key and per-model rate limiting
- [ ] Rate limit headers in responses

### M4 — Production Readiness
- [ ] Prometheus metrics endpoint (`/metrics`)
- [ ] Health check endpoint
- [ ] Graceful shutdown
- [ ] Config hot-reload
- [ ] Dockerfile & docker-compose (gateway + NATS)
- [ ] Integration tests

### M5 — Advanced Features
- [ ] Tool/function calling pass-through
- [ ] Provider failover
- [ ] NATS JetStream persistence mode
- [ ] Additional provider adapters

---

## 7. Open Questions

1. **Should adapters run in-process or as separate binaries?**
   Starting in-process for simplicity; the NATS-based architecture allows
   splitting them out later with zero code changes to the ingress.

2. **Token counting for rate limiting?**
   Initial rate limiting is request-count based. Token-based limits (using
   tiktoken or provider-reported usage) is a future enhancement.

3. **Multi-tenancy?**
   The API-key model provides basic tenancy. Full multi-tenant isolation
   (separate NATS accounts per tenant) is out of scope for v1.
