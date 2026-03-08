# NATS LLM Gateway — Requirements & Design

## 1. Overview

NATS LLM Gateway uses **NATS global clustering** to dynamically distribute
LLM inference across geographies. GPU nodes, cloud API adapters, and clients
connect to a NATS cluster (self-hosted, multi-region, or managed) from
anywhere — the routing layer shifts load between regions based on capacity,
latency, cost, and availability.

The **OpenAI-compatible chat completion API** is the onramp — existing apps
integrate with zero code changes. But the core value is underneath: a
globally distributed inference mesh where adding GPU capacity in a new
region is as simple as starting a NATS subscriber.

```
  São Paulo             US-East              Frankfurt             Tokyo
 ┌──────────┐                                                 ┌──────────┐
 │  Client  ├─┐        ┌──────────┐         ┌──────────┐  ┌──┤  Client  │
 └──────────┘ │        │ Gateway  │         │ Gateway  │  │  └──────────┘
              │    ┌───►│ (route + │◄───┐    │ (route + ├──┘
 ┌──────────┐ │    │    │  shift)  │    │    │  shift)  │     ┌──────────┐
 │ GPU Node ├─┤    │    └────┬─────┘    │    └────┬─────┘  ┌──┤ GPU Node │
 │ (Ollama) │ │    │         │          │         │        │  │ (vLLM)   │
 └──────────┘ │    │   ┌─────┴─────┐    │   ┌─────┴─────┐  │  └──────────┘
              ├────┼──►│   NATS    │◄───┼──►│   NATS    │◄─┤
 ┌──────────┐ │    │   │  Cluster  ├────┼──►│  Cluster  │  │  ┌──────────┐
 │HTTP Proxy├─┘    │   │  Node    │    │   │  Node    │  └──┤HTTP Proxy│
 └──────────┘      │   └───────────┘    │   └───────────┘     └──────────┘
                   │         │          │         │
                   │   ┌─────┴─────┐    │   ┌─────┴─────┐
                   └───┤ HTTP      │    └───┤ GPU Node  │
                       │ Adapter   │        │ (local)   │
                       │→ OpenAI   │        └───────────┘
                       └───────────┘
```

### Core Idea

Every participant — clients, gateways, model servers, cloud API adapters —
is a **NATS subscriber**. NATS handles:

- **Geographic routing** — requests flow to the nearest available inference node
- **Load balancing** — queue groups distribute across GPU nodes automatically
- **Failover** — if a region goes down, NATS routes to the next available region
- **Elastic scaling** — adding capacity = starting a subscriber; removing = stopping it
- **Multi-tenancy** — NATS accounts provide hard isolation between tenants

The gateway adds:

- **Dynamic load shifting** — move traffic between regions based on real-time capacity, cost, and latency signals
- **Model routing** — map model names to providers/regions
- **OpenAI API compatibility** — HTTP proxy and JS SDK as onramps

### Why NATS for Global Inference Routing?

| Capability | How NATS enables it |
|---|---|
| **Multi-region clustering** | NATS superclusters span data centers; subjects route globally |
| **Leaf nodes** | On-prem GPU clusters connect to the global mesh via outbound leaf node connections — no public IPs, no VPNs |
| **Queue groups** | Automatic load distribution across inference nodes, zero configuration |
| **Subject hierarchy** | `llm.provider.<name>.<region>` enables geographic routing at the subject level |
| **Latency-aware routing** | NATS routes to the topologically nearest subscriber by default |
| **Account isolation** | Built-in multi-tenancy with NATS accounts and JWTs |
| **JetStream** | Persistent queues for backpressure when GPUs are saturated |
| **Managed option** | Synadia Cloud provides a global NATS supercluster as a service — zero infrastructure to manage |

### Client Onramps

The OpenAI-compatible API makes adoption frictionless:

| Onramp | Migration effort | Who it's for |
|---|---|---|
| **HTTP Proxy** | Change `baseURL` — zero code changes | Existing apps, any language, any framework |
| **JS/TS SDK** | Swap constructor — 1-2 lines | Node.js/browser apps wanting direct NATS benefits |
| **Raw NATS** | Publish JSON to a subject | Advanced users, other languages, custom integrations |

---

## 2. Goals

1. **Dynamic geographic load shifting** — route inference requests across regions based on capacity, latency, cost, and availability in real time.
2. **NATS global cluster as the backbone** — all components communicate over NATS; the cluster topology defines the inference network.
3. **Elastic GPU scaling** — adding inference capacity in any region = starting a NATS subscriber. No reconfiguration.
4. **Zero-effort client adoption** — OpenAI-compatible HTTP proxy and JS SDK as onramps.
5. **Mixed infrastructure** — self-hosted GPUs, cloud APIs (OpenAI, Anthropic), and managed services coexist on the same NATS mesh.
6. **Multi-tenancy** — NATS accounts provide hard tenant isolation with per-account rate limits and permissions.
7. **Edge-to-cloud** — leaf nodes bridge on-prem GPU clusters, edge locations, and cloud regions into one global mesh.
8. **Streaming first** — token-by-token streaming over NATS subjects from inference node directly to client.
9. **Observability** — capacity metrics, routing decisions, and inference latency visible across the global mesh.
10. **Deployment flexibility** — works with self-hosted NATS clusters, managed Synadia Cloud, or hybrid leaf-node setups.

---

## 3. High-Level Requirements

### 3.1 Functional Requirements

**Global Routing & Load Shifting**

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-1 | Gateway routes requests to inference nodes based on model + region | P0 |
| FR-2 | Geographic subject hierarchy: `llm.provider.<name>.<region>` | P0 |
| FR-3 | Dynamic load shifting — move traffic between regions based on capacity signals | P0 |
| FR-4 | Inference node health reporting — GPU utilization, queue depth, latency published to status subjects | P0 |
| FR-5 | Region failover — if a region's inference nodes are unavailable, route to next-best region | P0 |
| FR-6 | Weighted routing — distribute across regions by configurable weights (cost, latency, capacity) | P1 |
| FR-7 | NATS leaf node support for on-prem GPU clusters connecting to global mesh | P1 |
| FR-8 | Multi-tenancy via NATS accounts with per-account isolation and limits | P1 |

**Client Onramps (OpenAI Compatibility)**

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-10 | HTTP proxy: `POST /v1/chat/completions` → NATS (drop-in for any OpenAI client) | P0 |
| FR-11 | HTTP proxy: SSE streaming support (`stream: true`) | P0 |
| FR-12 | JS SDK: `chat.completions.create(req)` with OpenAI-compatible types | P0 |
| FR-13 | JS SDK: streaming via async iterable (`for await...of`) | P0 |
| FR-14 | Return OpenAI-compatible response and error structures | P0 |
| FR-15 | HTTP proxy: `GET /v1/models` (returns models available across all regions) | P1 |

**Inference & Providers**

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-20 | NATS-native model server: inference engine subscribes directly to NATS | P0 |
| FR-21 | HTTP adapter: bridge to cloud APIs (OpenAI, Anthropic) via NATS→HTTP | P0 |
| FR-22 | Model aliasing — virtual model names map to provider:region pairs | P1 |
| FR-23 | Provider failover — retry on a different provider/region on failure | P1 |
| FR-24 | Tool/function calling pass-through | P2 |

**Platform**

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-30 | Authentication via NATS native auth (NKeys/JWTs) + gateway API key | P0 |
| FR-31 | Per-tenant and per-model rate limiting | P0 |
| FR-32 | Request/response logging with redaction of sensitive fields | P1 |
| FR-33 | Graceful shutdown with in-flight request draining | P1 |

### 3.2 Non-Functional Requirements

| ID | Requirement | Target |
|----|-------------|--------|
| NFR-1 | P99 gateway-added latency (excluding LLM + network time) | < 2 ms |
| NFR-2 | Concurrent request capacity per gateway instance | 10 000+ |
| NFR-3 | Region failover time (detect + reroute) | < 5 s |
| NFR-4 | Configuration hot-reload without restart | Yes |
| NFR-5 | Single statically-linked binary (gateway) | Yes |
| NFR-6 | Container image size | < 30 MB |
| NFR-7 | JS SDK bundle size (browser, minified+gzipped) | < 20 KB (excl. NATS client) |

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

### 4.10 NATS-Native Inference (HTTP at the Edge Only)

The same NATS subject contract (`llm.provider.<name>`) works for both
cloud API adapters (which bridge NATS→HTTP outbound) and self-hosted
inference servers (which subscribe to NATS directly). This means HTTP
can be eliminated from the entire path except at the client edge.

#### Deployment Topologies

**Cloud APIs (HTTP adapter bridges to external API):**
```
Client ──► HTTP Proxy ──► NATS ──► Gateway ──► NATS ──► HTTP Adapter ──HTTP──► OpenAI API
           (edge)                                       (outbound bridge)
           1 HTTP hop                                   1 HTTP hop
```

**Self-hosted models (zero internal HTTP):**
```
Client ──► HTTP Proxy ──► NATS ──► Gateway ──► NATS ──► Model Server (vLLM/Ollama)
           (edge)                                       (NATS subscriber, local GPU)
           1 HTTP hop                                   0 HTTP hops
```

**SDK client + self-hosted model (zero HTTP anywhere):**
```
Client (JS SDK) ──► NATS ──► Gateway ──► NATS ──► Model Server
Browser (NATS WS) ──► NATS ──► Gateway ──► NATS ──► Model Server
                     0 HTTP hops end-to-end
```

#### NATS-Native Model Server

A NATS-native model server is a thin wrapper around an inference engine
(vLLM, Ollama, llama.cpp, TGI) that subscribes to `llm.provider.<name>`
and runs inference directly — no HTTP server in the inference process.

```
┌──────────────────────────────────────┐
│         NATS-Native Model Server     │
│                                      │
│  NATS subscriber                     │
│  subject: llm.provider.local-llama   │
│  queue group: inference              │
│                                      │
│  ┌──────────────────────────────┐    │
│  │  Inference Engine            │    │
│  │  (vLLM / Ollama / llama.cpp) │    │
│  │  GPU 0..N                    │    │
│  └──────────────────────────────┘    │
└──────────────────────────────────────┘
```

The model server implements the same `ProviderRequest` → `ChatResponse`
wire format. The gateway routes to it identically — it doesn't know or
care whether the subscriber is an HTTP adapter or a bare-metal GPU box.

**Scaling:** Multiple model server instances subscribe to the same
subject with a shared queue group. NATS distributes requests across
GPUs automatically. Adding a GPU node = starting a new subscriber.
No load balancer, no service mesh, no configuration change.

```yaml
# Config: same provider syntax, different model names
models:
  "gpt-4o":
    provider: openai           # → HTTP adapter → OpenAI API
  "llama3-local":
    provider: local-llama      # → NATS-native model server (GPU)
  "codellama":
    provider: local-llama      # → same GPU cluster, different model
```

#### Benefits of NATS-Native Inference

| Benefit | Detail |
|---|---|
| **Zero internal HTTP** | No HTTP parse/serialize between gateway and inference |
| **Automatic GPU load balancing** | NATS queue groups distribute across GPU nodes |
| **Elastic scaling** | Add/remove GPU nodes by starting/stopping subscribers |
| **Mixed deployments** | Some models on local GPUs, some on cloud APIs — same gateway config |
| **Edge inference** | Run models close to users, connect via NATS leaf nodes |
| **Multi-cluster** | NATS super-clusters span data centers; inference can run anywhere |

### 4.11 Rate Limiting

Sliding window algorithm enforced at the gateway service before routing:

1. **Per-key limits** — configured per API key (e.g., `100/min`).
2. **Per-model limits** — global limit across all keys for a given model.
3. **Global limit** — overall gateway request cap.

State is stored in **NATS KV** for distributed consistency across gateway
replicas. Falls back to in-memory for single-instance deployments.

Rate limit errors are returned as standard error responses on the NATS reply
subject.

### 4.12 Global Deployment via Synadia Cloud (NGS)

[Synadia Cloud](https://www.synadia.com/cloud) (formerly NGS) is a globally
distributed, managed NATS supercluster. Instead of running your own NATS
servers, all components — clients, gateway, model servers — connect to
Synadia Cloud from anywhere in the world.

This turns the LLM gateway into a **globally distributed service with zero
infrastructure management**:

```
  São Paulo           US-East             Frankfurt            Tokyo
 ┌──────────┐                                              ┌──────────┐
 │  Client   │─WS─┐                                  ┌─WS─│  Client   │
 │ (browser) │    │                                  │    │ (browser) │
 └──────────┘    │    ┌───────────────────────┐    │    └──────────┘
                  ├───►│                       │◄───┤
 ┌──────────┐    │    │    Synadia Cloud      │    │    ┌──────────┐
 │ GPU Node │─TCP─┤    │    (global NATS       │    ├─TCP─│ GPU Node │
 │ (Ollama) │    │    │     supercluster)     │    │    │ (vLLM)   │
 └──────────┘    │    │                       │    │    └──────────┘
                  │    └───────┬───────┬───────┘    │
 ┌──────────┐    │            │       │            │    ┌──────────┐
 │ Gateway  │─TCP─┘            │       │            └─TCP─│ HTTP     │
 │ Service  │              (global    (global           │ Adapter  │─► OpenAI
 └──────────┘               routing)  (routing)         └──────────┘
```

**Nothing to run.** No NATS servers, no load balancers, no service mesh.
Synadia handles global routing, TLS, and availability. You just connect.

#### Why Synadia Cloud for an LLM Gateway

| Benefit | Detail |
|---|---|
| **Zero NATS ops** | No servers to provision, patch, or scale — Synadia manages the supercluster |
| **Global low-latency** | Clients connect to the nearest Synadia POP; requests route intelligently to the best available model server |
| **Multi-region inference** | GPU nodes in different regions subscribe to the same subjects — NATS routes to the nearest/fastest |
| **Built-in multi-tenancy** | NATS accounts provide hard isolation between tenants; each tenant gets its own account with separate subjects, limits, and JWTs |
| **Edge + cloud hybrid** | Leaf nodes extend Synadia Cloud to on-prem GPU clusters or edge locations |
| **Security** | JWT-based auth, NKeys, and account-level permissions — no secrets in the gateway config |

#### Multi-Tenancy with NATS Accounts

Synadia Cloud's account model maps naturally to LLM gateway tenancy:

```
Operator (you)
├── Account: "team-alpha"     (JWT-authenticated)
│   ├── User: "alpha-app-1"  → can publish to llm.chat.*, llm.provider.openai
│   ├── User: "alpha-app-2"  → can publish to llm.chat.* only
│   └── Rate limit: 1000 msg/min
│
├── Account: "team-beta"      (JWT-authenticated)
│   ├── User: "beta-app-1"   → can publish to llm.chat.*
│   └── Rate limit: 500 msg/min
│
└── Account: "infra"          (internal)
    ├── User: "gateway-svc"   → subscribes to llm.chat.*, publishes to llm.provider.*
    ├── User: "openai-adapter"→ subscribes to llm.provider.openai
    └── User: "gpu-node-1"   → subscribes to llm.provider.local-llama
```

Each account is fully isolated — `team-alpha` cannot see `team-beta`'s
messages. Cross-account communication (e.g., both teams routing to the
shared gateway account) is done via explicit exports/imports.

This replaces the gateway-level API key auth with NATS-native account
auth — stronger isolation, centrally managed via JWTs, no custom code.

#### Leaf Nodes for Hybrid Deployment

For organizations that want some infrastructure on-prem (e.g., GPU nodes
behind a firewall), NATS leaf nodes bridge private infrastructure to
Synadia Cloud:

```
┌─────────────────────────────┐        ┌──────────────────┐
│  On-Prem Data Center        │        │  Synadia Cloud   │
│                             │        │                  │
│  ┌────────┐  ┌────────┐    │  leaf   │                  │
│  │GPU Node│  │GPU Node│    ├────────►│  (global NATS)   │◄── Clients
│  └───┬────┘  └───┬────┘    │  node   │                  │
│      │           │         │        │                  │
│  ┌───┴───────────┴───┐     │        └──────────────────┘
│  │  Local NATS       │     │
│  │  (leaf node)      │     │
│  └───────────────────┘     │
└─────────────────────────────┘
```

GPU nodes never need public IPs. The leaf node makes an outbound
connection to Synadia Cloud, and NATS routes requests to the on-prem
GPUs transparently.

#### Deployment Options Summary

| Option | Run NATS? | Best for |
|---|---|---|
| **Self-hosted NATS** | Yes (single server or cluster) | Development, simple deployments, full control |
| **Synadia Cloud** | No | Production, global distribution, multi-tenancy |
| **Hybrid (leaf nodes)** | Yes (leaf nodes only) | On-prem GPUs + global client access |

The gateway code is identical across all three — only the NATS connection
URL changes.

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

The milestones are ordered to build toward the central goal: **dynamic
geographic load shifting for LLM inference**. Early milestones establish
the onramp (OpenAI compatibility) and single-region plumbing. Later
milestones add multi-region routing, capacity-aware load shifting, and
global deployment.

### M0 — Tracer Bullet ✅
- [x] End-to-end: HTTP proxy → NATS → Gateway → NATS → OpenAI adapter
- [x] End-to-end: JS SDK → NATS → Gateway → OpenAI adapter
- [x] Module boundaries proven (loosely coupled via NATS messages)
- [x] Unit, integration, and benchmark tests

### M1 — Single-Region Foundations
- [ ] Streaming: HTTP proxy SSE + JS SDK async iterables + NATS pub/sub
- [ ] Anthropic and Ollama provider adapters
- [ ] Model aliasing and routing
- [ ] Auth: API key validation + NATS native auth examples
- [ ] Per-key and per-model rate limiting (NATS KV backed)
- [ ] docker-compose: NATS + gateway + proxy + Ollama for local dev

### M2 — NATS-Native Inference
- [ ] Reference NATS-native model server wrapping Ollama (subscribes directly to NATS)
- [ ] NATS-native model server wrapping vLLM (Python NATS subscriber)
- [ ] Multi-GPU load balancing via NATS queue groups
- [ ] Streaming inference: tokens published directly from model server to client inbox
- [ ] Benchmark: NATS-native vs HTTP-based inference overhead
- [ ] Mixed deployment: local Ollama (NATS-native) + cloud OpenAI (HTTP adapter)

### M3 — Geographic Routing
- [ ] Geographic subject hierarchy: `llm.provider.<name>.<region>`
- [ ] Inference node registration: nodes announce region, models, and capacity on connect
- [ ] Region-aware routing: gateway routes to the nearest region with available capacity
- [ ] Multi-region NATS cluster setup (3-node example across regions)
- [ ] Leaf node configuration for on-prem GPU clusters bridging into the global mesh
- [ ] Region failover: detect unavailable region, reroute within <5s
- [ ] Benchmark: cross-region latency through NATS cluster vs direct API calls

### M4 — Dynamic Load Shifting
- [ ] Capacity signaling: model servers publish GPU utilization, queue depth, and inference latency to `llm.status.<provider>.<region>`
- [ ] Gateway aggregates capacity signals from all regions into a real-time routing table
- [ ] Weighted routing: distribute requests across regions by configurable weights (latency, cost, utilization)
- [ ] Automatic load shifting: when a region becomes saturated (GPU util > threshold), shift traffic to regions with spare capacity
- [ ] Cost-aware routing: prefer cheaper regions when latency is comparable
- [ ] Routing dashboard: real-time visibility into per-region capacity, request distribution, and routing decisions
- [ ] Drain region: admin command to gracefully shift all traffic away from a region (for maintenance)

### M5 — Multi-Tenancy & Production
- [ ] NATS account-based multi-tenancy — hard isolation between tenants
- [ ] Per-tenant rate limits and model permissions via NATS account config
- [ ] Prometheus metrics: per-region, per-model, per-tenant request rates and latencies
- [ ] Health check: `GET /health` on proxy + `llm.health` NATS subject
- [ ] Graceful shutdown with in-flight draining
- [ ] Config hot-reload via NATS signal
- [ ] Dockerfile & docker-compose for multi-region simulation

### M6 — Global Deployment
- [ ] NATS supercluster deployment guide (self-hosted, multi-region)
- [ ] Synadia Cloud deployment option — connect to managed global NATS
- [ ] Leaf node hybrid: on-prem GPU clusters + cloud regions on one mesh
- [ ] Example: global LLM service — GPU nodes in 3 regions, clients worldwide
- [ ] Benchmark: global routing latency, failover time, load shifting responsiveness

### M7 — Advanced Features
- [ ] Tool/function calling pass-through
- [ ] NATS JetStream persistence for request replay and audit
- [ ] WebSocket provider adapters (OpenAI Realtime API, Gemini Live API)
- [ ] Go SDK, Python SDK
- [ ] Client-side offloading: token counting, prompt dedup, prefix caching hints

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

6. **NATS-native inference: wrapper approach?**
   For Ollama, a Go wrapper that imports the Ollama library directly (no HTTP)
   is cleanest. For vLLM, a Python NATS subscriber calling vLLM's Python API
   avoids the HTTP server entirely. For llama.cpp, a CGo wrapper or a
   subprocess with stdin/stdout piping. Each has trade-offs in complexity
   vs. performance gain.

7. **GPU health and backpressure?**
   NATS queue groups distribute evenly, but GPUs have variable load. Model
   servers could publish utilization metrics to a status subject, and the
   gateway could use weighted routing. Alternatively, NATS JetStream with
   ack-wait provides natural backpressure — slow consumers get fewer messages.
