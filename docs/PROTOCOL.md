# InferMesh Session Protocol v1.0

This document defines the InferMesh wire protocol — message formats, session
lifecycle, and error semantics — independently of any transport mechanism.
Transport bindings (NATS, WebSocket, HTTP/2, gRPC) are defined in separate
documents.

---

## 1. Overview

InferMesh uses a request-reply protocol for LLM chat completions. Clients send
a `ChatRequest`, servers respond with a `ChatResponse` or `ErrorResponse`.
Sessions allow clients to send only new (delta) messages on subsequent turns,
while the server accumulates full conversation history.

**Protocol version:** 1.0

---

## 2. Wire Format

### 2.1 Encoding

- **Serialization:** JSON (RFC 8259)
- **Character encoding:** UTF-8
- **Field naming:** `snake_case`
- **Numeric types:** integers as JSON numbers, no string-encoded numbers

### 2.2 Message Types

| Type | Direction | Description |
|------|-----------|-------------|
| `ChatRequest` | Client → Server | Chat completion request |
| `ChatResponse` | Server → Client | Successful completion response |
| `ErrorResponse` | Server → Client | Error response |

### 2.3 Envelope

All messages on the wire are wrapped in a `ProviderRequest` envelope that
carries routing metadata alongside the client request:

```json
{
  "upstream_model": "gpt-4o",
  "request": { /* ChatRequest */ }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `upstream_model` | string | yes | Model identifier for routing |
| `request` | ChatRequest | yes | The client's chat request |

### 2.4 Field Definitions

#### ChatRequest

```json
{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are helpful."},
    {"role": "user", "content": "Hello!"}
  ],
  "temperature": 0.7,
  "max_tokens": 1024,
  "session_id": "a1b2c3..."
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `model` | string | yes | Model identifier |
| `messages` | Message[] | yes | Conversation messages |
| `temperature` | number | no | Sampling temperature |
| `max_tokens` | integer | no | Maximum tokens to generate |
| `session_id` | string | no | Session identifier for continuations |

#### Message

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `role` | string | yes | One of: `"system"`, `"user"`, `"assistant"` |
| `content` | string | no | Message text content |

#### ChatResponse

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1709900000,
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello!"},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 12,
    "completion_tokens": 8,
    "total_tokens": 20
  },
  "session_id": "a1b2c3...",
  "session_subject": "llm.session.a1b2c3..."
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Unique completion identifier |
| `object` | string | yes | Always `"chat.completion"` |
| `created` | integer | yes | Unix timestamp |
| `model` | string | yes | Model that generated the response |
| `choices` | Choice[] | yes | Completion choices |
| `usage` | Usage | no | Token usage statistics |
| `session_id` | string | no | Session identifier (present if session created) |
| `session_subject` | string | no | Transport-specific session address |

#### Choice

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `index` | integer | yes | Choice index |
| `message` | Message | no | The generated message |
| `finish_reason` | string | yes | `"stop"`, `"length"`, or provider-specific |

#### Usage

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `prompt_tokens` | integer | yes | Tokens in the prompt |
| `completion_tokens` | integer | yes | Tokens generated |
| `total_tokens` | integer | yes | Sum of prompt + completion |

#### ErrorResponse

```json
{
  "error": {
    "message": "session not found or expired",
    "type": "error",
    "code": "session_expired"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `error.message` | string | yes | Human-readable error description |
| `error.type` | string | yes | Error category (always `"error"`) |
| `error.code` | string | yes | Machine-readable error code |

---

## 3. Addressing

### 3.1 Model Address

A **model address** identifies a model endpoint. It is the model name string
(e.g., `"gpt-4o"`, `"qwen2.5:0.5b"`). Transport bindings map model addresses
to concrete transport addresses.

### 3.2 Session Address

A **session address** identifies a session endpoint. It is returned by the
server in `session_subject` on the first response. The value is opaque to the
client — transport bindings define the concrete format.

Clients MUST use the session address exactly as returned. Clients MUST NOT
construct or modify session addresses.

---

## 4. Session Lifecycle

### 4.1 Stateless Mode

Clients MAY send requests without sessions. Each request includes the full
message history. No `session_id` field is sent. The server treats each request
independently.

### 4.2 Session Creation

When the server receives a request without a `session_id`, it:

1. Processes the request
2. Generates a new session ID (see §8 Security)
3. Stores the full conversation (request messages + response)
4. Returns `session_id` and `session_subject` in the response

### 4.3 Delta Continuation

On subsequent turns, the client sends only new messages (the "delta") to the
session address, including the `session_id` in the request.

Example — first request (full history, sent to model address):
```json
{"messages": [{"role": "user", "content": "Hello"}]}
```

Second request (delta only, sent to session address):
```json
{
  "messages": [{"role": "user", "content": "What is 2+2?"}],
  "session_id": "a1b2c3..."
}
```

### 4.4 Server-Side Accumulation

The server maintains the full conversation history for each session. When a
delta request arrives, the server:

1. Validates the `session_id`
2. Appends the new messages to the stored history
3. Sends the full accumulated history to the LLM
4. Appends the LLM response to the stored history
5. Returns the response to the client

### 4.5 Session Expiry

Sessions are ephemeral. The server SHOULD enforce a 30-minute idle timeout
(measured from the last request). When a session expires, the server removes
all stored state. Subsequent requests to the session address receive a
`session_expired` error.

### 4.6 Client Recovery

When a client receives a `session_expired` error (or a transport-level error
that implies session loss), it SHOULD:

1. Discard the session ID and session address
2. Retransmit the full conversation history to the model address
3. Resume using the new session returned in the response

### 4.7 State Diagram

```
INIT ──(send to model address)──► ACTIVE
                                    │
                    ┌───────────────┘
                    │
                    ▼
              ACTIVE ──(send to session address)──► ACTIVE
                │
                │ (session_expired or transport error)
                ▼
            RECOVERY ──(send full history to model address)──► ACTIVE
```

---

## 5. Error Codes

| Code | Meaning |
|------|---------|
| `invalid_request` | Malformed request (parse error, missing fields, validation failure) |
| `provider_error` | Upstream LLM failure (API error, timeout, unavailable) |
| `session_expired` | Session no longer exists (expired, server restarted) |

Transport-specific errors (timeout, connection lost) SHOULD be treated as
`session_expired` for recovery purposes.

---

## 6. Message Ordering & Delivery

- **Request-reply:** Each request produces exactly one response.
- **Sequential sends:** Within a session, clients MUST wait for the response
  before sending the next request. Concurrent requests to the same session
  are not supported.
- **No sequence numbers:** Sequential send discipline guarantees ordering
  without explicit sequence numbers in v1.

---

## 7. Constraints

- **System messages:** `"system"` role messages are only valid in the initial
  request (session creation). The server MUST reject system messages in delta
  continuation requests with an `invalid_request` error.
- **Session affinity:** Sessions are pinned to the server instance that created
  them. Transport bindings ensure continuation requests reach the correct
  instance.
- **Session durability:** Sessions are ephemeral (in-memory). Sessions are NOT
  guaranteed to survive server restarts. Clients MUST implement recovery (§4.6).

---

## 8. Security

- **Session ID format:** 128-bit cryptographic random value, hex-encoded
  (32 characters). Implementations MUST use a cryptographically secure random
  number generator.
- **Authentication:** Delegated to the transport layer. The protocol does not
  define authentication mechanisms.
- **TTL:** Servers SHOULD enforce a 30-minute idle timeout on sessions.
