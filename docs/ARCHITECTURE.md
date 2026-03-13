# InferMesh Architecture

## What InferMesh Does

InferMesh is a unified LLM API. You send one HTTP request to one endpoint, and
it routes to the right LLM provider based on the model name. Geographic
distribution happens through NATS leaf nodes — pure infrastructure, no
application code.

## The Two Application Components

There are exactly two application components. Everything else is NATS
infrastructure.

```
┌──────────────────────────────────────────────────────────┐
│                  Application Components                   │
│                                                          │
│   ┌───────────┐                       ┌──────────────┐   │
│   │   Proxy   │───── NATS ──────────► │   Provider   │   │
│   │  (HTTP →  │  llm.chat.{model}   │  (NATS →     │   │
│   │   NATS)   │                       │   HTTP LLM)  │   │
│   └───────────┘                       └──────────────┘   │
│                                                          │
│   cmd/proxy                            cmd/provider      │
│   internal/proxy                       internal/provider  │
└──────────────────────────────────────────────────────────┘
```

There is no router, orchestrator, or gateway code. The model name determines
the NATS subject, so the proxy publishes directly.

---

### 1. Proxy (`cmd/proxy`, `internal/proxy`)

**Responsibility:** HTTP-to-NATS bridge. Nothing else.

- Exposes `POST /v1/chat/completions` (OpenAI-compatible)
- Exposes `GET /health`
- Uses the model name directly (e.g., `gpt-4o`, `qwen2.5:0.5b`)
- Publishes a `ProviderRequest` to NATS subject `llm.chat.{model}`
- Waits for NATS reply, writes it back as HTTP response
- Maps error codes to HTTP status codes (404, 429, 400, 500)

**NATS interaction:**
```
Publishes to:  llm.chat.{model}  (request-reply)
Examples:      llm.chat.gpt-4o
               llm.chat.qwen2.5:0.5b
```

**Why it exists:** So existing OpenAI SDKs and `curl` work unchanged — just
point `base_url` at the proxy.

---

### 2. Provider (`cmd/provider`, `internal/provider/*`)

**Responsibility:** NATS consumer that calls an upstream LLM API.

Each provider is a NATS queue subscriber that:
- Subscribes to `llm.chat.>` (all models) or `llm.chat.{model}` (specific model)
- Receives a `ProviderRequest` from the proxy (or SDK)
- Translates it into the provider's native HTTP API format
- Calls the upstream LLM API over HTTP
- Translates the response back into the unified `ChatResponse` format
- Replies on NATS

**NATS interaction (per provider):**
```
openai:    subscribes to llm.chat.>  (queue: "provider-openai")
anthropic: subscribes to llm.chat.>  (queue: "provider-anthropic")
ollama:    subscribes to llm.chat.>  (queue: "provider-ollama")
llamacpp:  subscribes to llm.chat.>  (queue: "provider-llamacpp")
```

The `>` wildcard matches all models.

**What each adapter does differently:**

| Adapter   | Upstream endpoint          | Auth header         | Translate? | Timeout |
|-----------|----------------------------|---------------------|------------|---------|
| OpenAI    | `{base}/chat/completions`  | `Authorization: Bearer` | No — native format | 60s |
| Anthropic | `{base}/v1/messages`       | `x-api-key` + `anthropic-version` | Yes — system messages, max_tokens, response mapping | 60s |
| Ollama    | `{base}/chat/completions`  | None                | No — OpenAI-compatible | 120s |
| llama.cpp | `{base}/v1/chat/completions` | None              | No — OpenAI-compatible | 120s |

**Anthropic translation details:**
- Extracts `system` role messages → Anthropic `system` top-level field
- Defaults `max_tokens` to 4096 (Anthropic requires it)
- Maps response: `content[0].text` → `choices[0].message.content`
- Maps `stop_reason` → `finish_reason` ("max_tokens" → "length")
- Maps `input_tokens`/`output_tokens` → `prompt_tokens`/`completion_tokens`

---

## Model Naming

Models are named directly — no provider prefix needed. The model name is used
as-is in the NATS subject and passed to the upstream API.

```
gpt-4o                      → subject: llm.chat.gpt-4o
claude-sonnet-4-20250514    → subject: llm.chat.claude-sonnet-4-20250514
qwen2.5:0.5b               → subject: llm.chat.qwen2.5:0.5b
llama3:8b                   → subject: llm.chat.llama3:8b
```

This means:
- No routing table needed. The model name IS the routing key.
- Providers subscribe with `>` wildcard to handle all models.
- Adding a new provider is just deploying a new adapter — no config changes
  anywhere else.

---

## The Complete Request Flow

```
curl POST /v1/chat/completions
     {model: "claude-sonnet-4-20250514", messages: [...]}
  │
  ▼
┌──────────┐  HTTP
│  Proxy   │  model: "claude-sonnet-4-20250514"
│          │  → subject: "llm.chat.claude-sonnet-4-20250514"
└────┬─────┘
     │  NATS publish: "llm.chat.claude-sonnet-4-20250514"
     │  payload: ProviderRequest{UpstreamModel: "claude-sonnet-4-20250514", ...}
     ▼
┌──────────┐
│ Provider │  Translates to Anthropic Messages API format
│(anthropic)│  POST https://api.anthropic.com/v1/messages
└────┬─────┘
     │  HTTP response from Anthropic
     │  Translates back to unified ChatResponse
     │
     ▼  (NATS reply)
Proxy receives reply → HTTP 200 with ChatResponse JSON
```

One hop. Proxy → Provider. No intermediary.

---

## NATS Infrastructure (No Application Code)

NATS leaf nodes handle geographic distribution. This is pure infrastructure —
config files, no Go code.

```
                    ┌─────────────────────┐
                    │      NATS Hub       │
                    │   :4222  leaf:7422  │
                    └──┬──────────────┬───┘
          leaf connect │              │ leaf connect
    ┌──────────────────┘              └───────────────────┐
    │                                                     │
┌───▼──────────────┐                          ┌───────────▼────────┐
│  Leaf: Client    │                          │  Leaf: Provider    │
│  (US-East)       │                          │  (EU-West)         │
│  :4225           │                          │  :4223             │
└──────────────────┘                          └────────────────────┘
 Proxy connects here                          Providers connect here
```

**What leaf nodes do:** Transparently extend the NATS subject space across
geographic regions. When the proxy publishes `llm.chat.gpt-4o` on the client leaf,
NATS automatically routes it to whichever leaf has a matching subscriber.

**What leaf nodes DON'T do:** No application logic. No model resolution. No
request transformation. They're NATS servers with a `leafnodes.remotes` config.

**Config files** (in `demo/`):
- `nats-hub.conf` — central hub, accepts leaf connections on port 7422
- `nats-leaf-client.conf` — client-side leaf, connects to hub
- `nats-leaf-cloud.conf` — provider-side leaf for cloud APIs
- `nats-leaf-edge.conf` — provider-side leaf for local models (Ollama)

---

## Code Organization

```
cmd/                          # Entrypoints — thin main() wrappers
├── proxy/main.go             #   Starts the HTTP proxy
├── provider/main.go          #   Starts a single provider adapter
└── mockllm/main.go           #   Starts the mock LLM server for testing

internal/                     # Core logic — libraries used by cmd/
├── proxy/proxy.go            #   HTTP server, NATS publishing
├── provider/
│   ├── provider.go           #   Provider interface definition
│   ├── session_handler.go    #   Session management
│   ├── openai/openai.go      #   OpenAI adapter
│   ├── anthropic/anthropic.go#   Anthropic adapter (with format translation)
│   ├── ollama/ollama.go      #   Ollama adapter
│   └── llamacpp/llamacpp.go  #   llama.cpp adapter
├── config/config.go          #   YAML config loader
├── session/store.go          #   Session store with TTL
└── testutil/nats.go          #   Embedded NATS server for tests

api/                          # Wire format types shared by all components
└── types.go                  #   ChatRequest, ChatResponse, ProviderRequest, etc.

sdk/js/                       # TypeScript/JavaScript client SDK
├── src/
│   ├── index.ts              #   InferMeshClient entry point
│   ├── chat.ts               #   ChatCompletions + ChatSession classes
│   └── types.ts              #   Wire format types
└── test/                     #   Vitest tests
```

**Why `cmd/` vs `internal/`?** Standard Go project layout:
- `cmd/` — each subdirectory is a `main` package that produces a binary.
  Contains only startup/wiring code: parse env vars, create dependencies,
  call into `internal/`.
- `internal/` — the actual logic. Can only be imported by code in this module
  (Go enforces this). This is where the real work happens.
- `api/` — shared types. Not in `internal/` because external code (SDKs, tests)
  may need these types.

---

## Configuration

Providers need config for their upstream API credentials. The proxy needs no
config — just a NATS URL.

**Provider config** (`configs/provider.yaml` or `demo/demo.yaml`):
```yaml
nats:
  url: "${NATS_URL:-nats://localhost:4222}"

providers:
  openai:
    base_url: "${OPENAI_BASE_URL:-https://api.openai.com/v1}"
    api_key: "${OPENAI_API_KEY}"
  anthropic:
    base_url: "${ANTHROPIC_BASE_URL:-https://api.anthropic.com}"
    api_key: "${ANTHROPIC_API_KEY}"
  ollama:
    base_url: "${OLLAMA_BASE_URL:-http://localhost:11434/v1}"
```

Environment variables are expanded at load time (`${VAR}` syntax).

---

## Scaling

Because providers use NATS queue groups, you scale by running more instances:

- **Multiple proxies:** All connect to NATS, all route by model name.
  Put them behind a load balancer.
- **Multiple providers:** Queue group `"provider-openai"` distributes requests
  across OpenAI adapter instances. Run 5 instances to handle 5 concurrent
  OpenAI requests.

No coordination needed. NATS handles it.

---

## NATS Subjects Reference

| Subject pattern                  | Publisher | Subscriber           | Payload          |
|----------------------------------|-----------|----------------------|------------------|
| `llm.chat.{model}`              | Proxy/SDK | Provider adapters    | `ProviderRequest`|
| `llm.session.{session_id}`      | SDK       | Session owner provider | `ProviderRequest`|

Providers subscribe with `>` wildcard (e.g., `llm.chat.>`) to handle all
models.

All use NATS request-reply pattern. Replies carry `ChatResponse` or
`ErrorResponse`.
