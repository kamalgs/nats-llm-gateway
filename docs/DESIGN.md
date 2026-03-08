# NATS LLM Gateway — Requirements & Design

## 1. Overview

NATS LLM Gateway is a **NATS-native** LLM gateway. The core gateway has no
HTTP layer — it communicates purely over NATS. Clients integrate via two paths:

1. **HTTP Proxy** (zero migration effort) — a thin `POST /v1/chat/completions`
   endpoint that translates HTTP to NATS. Existing apps just change `baseURL`.
   Works with any OpenAI SDK, LangChain, Vercel AI SDK, or raw `fetch()`.

2. **NATS SDK** (full benefits) — a JS/TS SDK that mirrors the `openai` npm
   package API over NATS directly. Lower latency, native streaming, direct
   pub/sub access.

```
                                NATS Server
                             (TCP + WebSocket)
                                    │
        ┌───────────────────────────┼───────────────────────────┐
        │                           │                           │
 ┌──────┴──────┐            ┌──────┴──────┐            ┌──────┴──────┐
 │  HTTP Proxy │            │   Gateway   │            │   Client    │
 │ (any client │            │   Service   │            │  (JS SDK)   │
 │  via baseURL│────NATS───►│   (Go)      │            │  Node/Bun/  │
 │  change)    │            │             │◄───NATS────│  Browser    │
 └──────┬──────┘            └──────┬──────┘            └─────────────┘
        ▲                          │
  HTTP  │               ┌──────────┼──────────┐
  POST  │               ▼          ▼          ▼
 /v1/.. │        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │        │ Provider │ │ Provider │ │ Provider │
 ┌──────┴──────┐ │ OpenAI   │ │Anthropic │ │ Ollama   │
 │ Existing app│ └──────────┘ └──────────┘ └──────────┘
 │ (OpenAI SDK │
 │  LangChain  │
 │  fetch()    │
 │  curl)      │
 └─────────────┘
```

### Two Integration Tiers

| | HTTP Proxy | NATS SDK |
|---|---|---|
| **Migration effort** | Change `baseURL` — zero code changes | Swap constructor — 1-2 lines |
| **Works with** | Any language, any framework, curl | JS/TS (Node, Deno, Bun, browser) |
| **Streaming** | SSE (`text/event-stream`) | Async iterables over NATS |
| **Latency overhead** | HTTP parse + NATS hop | NATS only |
| **Best for** | Existing apps, frameworks (LangChain, Vercel AI SDK) | New apps, performance-sensitive, advanced NATS patterns |

### Why NATS-native (no HTTP in the gateway)?

| Benefit | Detail |
|---|---|
| **Lower latency** | No HTTP parse/serialize overhead; NATS binary protocol is faster |
| **Built-in auth** | NATS has native user/token/NKey/JWT authentication — no custom auth middleware needed |
| **Built-in streaming** | NATS subjects are natural streaming channels — no SSE/chunked-encoding complexity |
| **WebSocket support** | NATS server natively exposes WebSocket endpoints — browser clients connect directly |
| **Simpler gateway** | The gateway is just a NATS service — no HTTP framework, no middleware stack |
| **Scalability** | Clients, gateway services, and adapters are all equal NATS participants; scale any independently |

---

## 2. Goals

1. **Zero-effort adoption** — HTTP proxy accepts `POST /v1/chat/completions`; existing apps just change `baseURL`.
2. **NATS-native protocol** — core gateway communicates purely over NATS (TCP or WebSocket).
3. **JavaScript SDK with OpenAI-compatible interface** — mirrors the `openai` npm package API for apps that want direct NATS benefits.
4. **Multi-runtime** — SDK works in Node.js, Deno, Bun, and browsers.
5. **Multi-provider routing** — route to OpenAI, Anthropic, Ollama, vLLM, or any provider via pluggable adapters.
6. **Model aliasing & mapping** — expose virtual model names that map to real provider:model pairs.
7. **Streaming first** — SSE for HTTP clients, async iterables over NATS for SDK clients.
8. **Rate limiting** — per-user, per-model, and global rate limits enforced at the gateway service.
9. **Authentication** — leverage NATS native auth (NKeys, JWTs, tokens) + gateway-level API key validation.
10. **Observability** — structured logging, Prometheus metrics, OpenTelemetry traces.
11. **Future SDKs** — Go, Python SDKs can be added later following the same wire protocol.

---

## 3. High-Level Requirements

### 3.1 Functional Requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-1 | HTTP proxy: `POST /v1/chat/completions` → NATS (drop-in for any OpenAI client) | P0 |
| FR-2 | HTTP proxy: SSE streaming support (`stream: true`) | P0 |
| FR-3 | HTTP proxy: `GET /v1/models` endpoint | P0 |
| FR-4 | JS SDK: `chat.completions.create(req)` with OpenAI-compatible request/response types | P0 |
| FR-5 | JS SDK: streaming via async iterable (`for await...of`) when `stream: true` | P0 |
| FR-6 | JS SDK: works in Node.js (TCP) and browsers (WebSocket) | P1 |
| FR-7 | Gateway service: accept requests on NATS subjects, route by model | P0 |
| FR-8 | Provider adapters for OpenAI, Anthropic, Ollama | P0 |
| FR-9 | Model aliasing — map virtual model names to provider:model pairs | P1 |
| FR-10 | Authentication via NATS native auth + gateway-level API key check | P0 |
| FR-11 | Per-user and per-model rate limiting at the gateway | P0 |
| FR-12 | Return OpenAI-compatible response and error structures | P0 |
| FR-13 | Request/response logging with redaction of sensitive fields | P1 |
| FR-14 | Graceful shutdown with in-flight request draining | P1 |
| FR-15 | Tool/function calling pass-through | P2 |
| FR-16 | Provider failover — retry on a secondary provider if primary fails | P2 |
| FR-17 | Go SDK | P2 |
| FR-18 | Python SDK | P2 |

### 3.2 Non-Functional Requirements

| ID | Requirement | Target |
|----|-------------|--------|
| NFR-1 | P99 gateway-added latency (excluding LLM time) | < 2 ms |
| NFR-2 | Concurrent request capacity | 10 000+ |
| NFR-3 | Configuration hot-reload without restart | Yes |
| NFR-4 | Single statically-linked binary (gateway) | Yes |
| NFR-5 | Container image size | < 30 MB |
| NFR-6 | JS SDK bundle size (browser, minified+gzipped) | < 20 KB (excl. NATS client) |
| NFR-7 | JS SDK: zero dependencies beyond `nats.ws` / `nats` | Yes |

---

## 4. Architecture

### 4.1 Repository Structure

```
nats-llm-gateway/
├── sdk/
│   └── js/                        # JavaScript/TypeScript SDK (nats-llm-client)
│       ├── src/
│       │   ├── index.ts           # Public API exports
│       │   ├── client.ts          # NATSLLMClient — main entry point
│       │   ├── chat.ts            # chat.completions namespace
│       │   ├── models.ts          # models namespace
│       │   ├── streaming.ts       # Async iterable stream wrapper
│       │   └── types.ts           # OpenAI-compatible types
│       ├── test/
│       ├── package.json
│       └── tsconfig.json
├── cmd/
│   ├── gateway/                   # Gateway service binary (Go)
│   └── proxy/                     # HTTP→NATS proxy binary (Go)
├── internal/                      # Gateway internals (Go)
│   ├── auth/
│   ├── ratelimit/
│   ├── router/
│   ├── provider/
│   │   ├── provider.go
│   │   ├── openai/
│   │   ├── anthropic/
│   │   └── ollama/
│   ├── proxy/                     # HTTP proxy: OpenAI-compat HTTP ↔ NATS translation
│   ├── config/
│   └── middleware/
├── configs/
│   └── gateway.yaml
├── docs/
│   └── DESIGN.md
├── Dockerfile
├── go.mod
└── go.sum
```

### 4.2 HTTP Proxy — Zero-Migration Path

The HTTP proxy is a thin Go binary that translates OpenAI-compatible HTTP
requests to NATS messages and back. It connects to NATS as a client and
publishes to the same subjects the JS SDK uses.

```
Existing App                    HTTP Proxy                  NATS
    │                              │                          │
    │  POST /v1/chat/completions   │                          │
    │  Authorization: Bearer sk-.. │                          │
    │  {model, messages}           │                          │
    │ ────────────────────────────►│                          │
    │                              │  NATS Request            │
    │                              │  llm.chat.complete       │
    │                              │ ────────────────────────►│──► Gateway
    │                              │                          │
    │                              │◄─────────────────────────│◄── Reply
    │   HTTP 200 JSON              │                          │
    │◄─────────────────────────────│                          │
    │                              │                          │
    │  POST (stream: true)         │                          │
    │ ────────────────────────────►│                          │
    │                              │  NATS sub + Request      │
    │                              │  llm.chat.stream         │
    │                              │ ────────────────────────►│──► Gateway
    │   SSE: data: {chunk}         │                          │
    │◄══════════════════════════════◄══════════════════════════◄══ Chunks
    │   SSE: data: [DONE]          │                          │
    │◄══════════════════════════════│                          │
```

**Usage — existing apps change one line:**

```typescript
// Before
const client = new OpenAI({ apiKey: 'sk-...' });

// After — point to the proxy, everything else unchanged
const client = new OpenAI({
  baseURL: 'http://localhost:8080/v1',
  apiKey: 'sk-...',
});
```

```bash
# Works with curl, Python openai SDK, LangChain, Vercel AI SDK, anything
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-my-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello"}]}'
```

The proxy can run as a sidecar, a standalone service, or be embedded in the
gateway binary itself (single binary mode).

### 4.3 SDK — JavaScript Client Interface

The SDK mirrors the OpenAI JS SDK (`openai` npm package) interface:

```typescript
import { NATSLLMClient } from 'nats-llm-client';

// Connect to NATS — Node.js (TCP) or browser (WebSocket)
const client = new NATSLLMClient({
  natsUrl: 'wss://nats.example.com:443',  // or 'nats://localhost:4222'
  apiKey: 'sk-my-key',
});

// Non-streaming — same shape as OpenAI SDK
const response = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});
console.log(response.choices[0].message.content);

// Streaming — async iterable, just like OpenAI SDK
const stream = await client.chat.completions.create({
  model: 'claude-sonnet',
  messages: [{ role: 'user', content: 'Write a poem' }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content || '');
}

// List models
const models = await client.models.list();

// Cleanup
await client.close();
```

**Migration from OpenAI SDK:**

```typescript
// Before (OpenAI SDK)
import OpenAI from 'openai';
const client = new OpenAI({ apiKey: 'sk-...' });

// After (NATS LLM Gateway SDK)
import { NATSLLMClient } from 'nats-llm-client';
const client = new NATSLLMClient({ natsUrl: 'nats://localhost:4222', apiKey: 'sk-...' });

// Everything below stays IDENTICAL:
const resp = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});
```

### 4.4 Request Flow

#### Non-Streaming (NATS Request/Reply)

```
JS SDK                        Gateway Service              Provider Adapter
    │                              │                              │
    │  NATS Request                │                              │
    │  subject: llm.chat.complete  │                              │
    │  payload: {model, messages}  │                              │
    │  reply-to: _INBOX.xxx        │                              │
    │ ────────────────────────────►│                              │
    │                              │  Authenticate + Rate Check   │
    │                              │  Resolve model → provider    │
    │                              │                              │
    │                              │  NATS Request                │
    │                              │  subject: llm.provider.openai│
    │                              │ ────────────────────────────►│
    │                              │                              │  HTTP call to
    │                              │                              │  OpenAI API
    │                              │          NATS Reply          │◄────────────
    │                              │◄─────────────────────────────│
    │        NATS Reply            │                              │
    │◄─────────────────────────────│                              │
```

#### Streaming (NATS Pub/Sub)

```
JS SDK                        Gateway Service              Provider Adapter
    │                              │                              │
    │  NATS Request                │                              │
    │  subject: llm.chat.stream    │                              │
    │  payload: {model, messages,  │                              │
    │   stream_subject:            │                              │
    │   _INBOX.stream.xxx}         │                              │
    │ ────────────────────────────►│                              │
    │                              │  Authenticate + Rate Check   │
    │                              │  Resolve model → provider    │
    │                              │                              │
    │                              │  NATS Request                │
    │                              │  subject: llm.provider.openai│
    │                              │  stream_reply:               │
    │                              │   _INBOX.stream.xxx          │
    │                              │ ────────────────────────────►│
    │                              │                              │
    │   NATS Pub (chunk 1)         │                              │
    │◄═══════════════════════════════════════════════════════════ │
    │   NATS Pub (chunk 2)         │                              │
    │◄═══════════════════════════════════════════════════════════ │
    │   NATS Pub (chunk N)         │                              │
    │◄═══════════════════════════════════════════════════════════ │
    │   NATS Pub ([DONE])          │                              │
    │◄═══════════════════════════════════════════════════════════ │
```

For streaming, the provider adapter publishes chunks **directly** to the
client's inbox subject — the gateway service doesn't sit in the data path for
every token. This minimizes latency. The gateway only handles the initial
request (auth, rate limit, routing).

### 4.5 NATS Subject Design

| Subject | Purpose | Pattern |
|---|---|---|
| `llm.chat.complete` | Non-streaming chat completion requests | Request/Reply |
| `llm.chat.stream` | Streaming chat completion requests | Request triggers pub/sub |
| `llm.models` | List available models | Request/Reply |
| `llm.provider.<name>` | Internal: gateway → provider adapter | Request/Reply + queue group |
| `llm.admin.reload` | Config hot-reload signal | Pub/Sub |

- The gateway service subscribes to `llm.chat.complete` and `llm.chat.stream`
  using **queue groups** for horizontal scaling.
- Provider adapters subscribe to `llm.provider.<name>` using **queue groups**
  so multiple replicas share load.
- Streaming chunks flow directly from adapter to client inbox — no gateway hop.

### 4.6 Wire Format

All messages are JSON-encoded. The wire types match OpenAI's API schema:

**Request** (published by SDK to `llm.chat.complete` or `llm.chat.stream`):
```json
{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are helpful."},
    {"role": "user", "content": "Hello!"}
  ],
  "temperature": 0.7,
  "max_tokens": 1024,
  "stream_subject": "_INBOX.stream.abc123",
  "api_key": "sk-my-key"
}
```

`stream_subject` is only present for streaming requests. `api_key` is used
for gateway-level auth (complementing NATS-level auth).

**Response** (non-streaming reply):
```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1709900000,
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello! How can I help?"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20}
}
```

**Streaming chunk** (published to client's `stream_subject`):
```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion.chunk",
  "choices": [{
    "index": 0,
    "delta": {"content": "Hello"},
    "finish_reason": null
  }]
}
```

**Error**:
```json
{
  "error": {
    "message": "Rate limit exceeded. Retry after 2s.",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  }
}
```

### 4.7 Configuration

```yaml
# configs/gateway.yaml
nats:
  url: "nats://localhost:4222"

auth:
  enabled: true
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

### 4.8 Authentication (Two Layers)

**Layer 1 — NATS native auth:**
- Clients authenticate to the NATS server using tokens, NKeys, or JWTs.
- NATS accounts and user permissions control which subjects a client can
  publish/subscribe to.
- This is standard NATS server configuration — the gateway doesn't implement it.

**Layer 2 — Gateway API key auth:**
- The gateway validates the `api_key` field in the request payload against its
  configured key store.
- Each key has associated permissions (allowed models, rate limits, metadata).
- This enables application-level identity and policy enforcement on top of
  NATS transport-level auth.

### 4.9 Rate Limiting

Sliding window algorithm enforced at the gateway service before routing:

1. **Per-key limits** — configured per API key (e.g., `100/min`).
2. **Per-model limits** — global limit across all keys for a given model.
3. **Global limit** — overall gateway request cap.

State is stored in **NATS KV** for distributed consistency across gateway
replicas. Falls back to in-memory for single-instance deployments.

Rate limit errors are returned as standard error responses on the NATS reply
subject.

---

## 5. Technology Choices

### Gateway Service

| Component | Choice | Rationale |
|---|---|---|
| Language | **Go** | Single binary, excellent concurrency, NATS has first-class Go client |
| NATS client | `github.com/nats-io/nats.go` | Official client |
| Config | `github.com/knadh/koanf` | Hot-reload, env var substitution, YAML |
| Logging | `log/slog` (stdlib) | Structured, zero-dependency |
| Metrics | `github.com/prometheus/client_golang` | Industry standard |
| Rate limiting | Custom (sliding window over NATS KV) | Distributed-friendly, no external deps |

### JavaScript SDK

| Component | Choice | Rationale |
|---|---|---|
| Language | **TypeScript** | Type safety, great DX, matches OpenAI SDK conventions |
| NATS client | `nats` / `nats.ws` | Official NATS.js client — `nats` for Node/Deno/Bun, `nats.ws` for browsers |
| Build | `tsup` | Fast, zero-config bundler for libraries |
| Test | `vitest` | Fast, TypeScript-native |
| Package | `nats-llm-client` | Published to npm |

---

## 6. Milestones

### M1 — Walking Skeleton (HTTP Proxy + Gateway + One Provider)
- [ ] HTTP proxy: `POST /v1/chat/completions` → NATS translation (non-streaming)
- [ ] HTTP proxy: `GET /v1/models` endpoint
- [ ] Gateway service: subscribe to `llm.chat.complete`, route to provider
- [ ] OpenAI provider adapter (pass-through)
- [ ] JS SDK: `NATSLLMClient` with NATS connection (Node.js TCP)
- [ ] JS SDK: `chat.completions.create()` — non-streaming request/reply
- [ ] JS SDK: OpenAI-compatible types (TypeScript)
- [ ] End-to-end: existing OpenAI SDK client → HTTP proxy → NATS → Gateway → OpenAI → response
- [ ] End-to-end: JS SDK → NATS → Gateway → OpenAI → response
- [ ] docker-compose: NATS server + gateway + proxy for local dev

### M2 — Streaming & Multi-Provider
- [ ] HTTP proxy: SSE streaming (`stream: true` → `text/event-stream`)
- [ ] JS SDK: streaming via async iterable (`for await...of`)
- [ ] Gateway + adapter streaming via NATS pub/sub
- [ ] Anthropic provider adapter (Messages API → OpenAI format translation)
- [ ] Ollama provider adapter
- [ ] Model aliasing and routing

### M3 — Auth & Rate Limiting
- [ ] Gateway API key authentication (validated from HTTP `Authorization` header and NATS payload)
- [ ] Per-key and per-model rate limiting (NATS KV backed)
- [ ] NATS server auth configuration examples (NKeys, JWTs)
- [ ] HTTP proxy: rate limit headers (`X-RateLimit-*`, `Retry-After`)

### M4 — Production Readiness
- [ ] Prometheus metrics (exposed via HTTP endpoint on gateway)
- [ ] Health check: `GET /health` on proxy + `llm.health` NATS subject
- [ ] Graceful shutdown with in-flight draining
- [ ] Config hot-reload via NATS signal
- [ ] JS SDK: browser support via `nats.ws` (WebSocket)
- [ ] Dockerfile & docker-compose (gateway + proxy + NATS server with WS enabled)
- [ ] Integration tests (HTTP proxy + JS SDK ↔ gateway ↔ mock provider)

### M5 — Advanced Features
- [ ] Tool/function calling pass-through
- [ ] Provider failover
- [ ] NATS JetStream persistence mode
- [ ] Go SDK
- [ ] Python SDK
- [ ] Additional provider adapters (Google Vertex, vLLM)
- [ ] WebSocket provider adapters (OpenAI Realtime API, Gemini Live API)

### M6 — Client-Side Offloading
- [ ] Client-side token counting (`js-tiktoken` WASM) — budget enforcement and prompt truncation before requests hit NATS
- [ ] Prompt hash deduplication — SDK hashes prompt content, gateway deduplicates identical in-flight requests to avoid redundant inference
- [ ] Client-side RAG assembly — SDK helpers for local embedding (via `transformers.js`) + retrieval, sending only the final assembled prompt
- [ ] Prefix caching hints — SDK signals reusable prompt prefixes so inference servers can skip KV cache recomputation

---

## 7. Open Questions

1. **Should adapters run in-process or as separate binaries?**
   Starting in-process for simplicity; the NATS subject-based architecture
   allows splitting them out later with zero changes to the gateway or SDK.

2. **Token counting for rate limiting?**
   Initial rate limiting is request-count based. Token-based limits (using
   tiktoken or provider-reported usage) is a future enhancement.

3. **Multi-tenancy?**
   NATS accounts provide natural tenant isolation. The gateway API key model
   provides basic tenancy. Full multi-tenant isolation (separate NATS
   accounts per tenant) can be layered on.

4. **Should streaming chunks route through the gateway or go direct?**
   Current design: direct from adapter to client inbox for minimum latency.
   Trade-off: gateway can't observe/meter individual chunks. If per-token
   metering is needed, chunks can be routed through the gateway with a
   subject rewrite.

5. **NATS.js client choice for SDK?**
   The official `nats` package (nats.js v2+) supports Node.js, Deno, and Bun
   natively. For browsers, `nats.ws` provides WebSocket transport. The SDK
   should accept either a pre-connected NATS connection or auto-detect the
   runtime and pick the right transport.
