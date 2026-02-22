---
title: Runtime Protocols
sidebar:
  order: 4
---

The AgentCore runtime serves three communication protocols on port 8080 (HTTP bridge). Clients can choose between blocking JSON, Server-Sent Events (SSE) streaming, or WebSocket depending on their latency and interactivity requirements.

The runtime also serves the A2A protocol on port 9000 for agent-to-agent communication. The `protocol` config field controls which servers are started.

## Protocol mode

The `protocol` field in the deploy config controls which servers the runtime starts:

| Value | Port 8080 (HTTP bridge) | Port 9000 (A2A server) | Use case |
|-------|------------------------|----------------------|----------|
| `"both"` (default) | Started | Started | Standard deployment. Supports external HTTP clients and inter-agent A2A calls. |
| `"http"` | Started | Skipped | External-facing agents that do not participate in multi-agent A2A networks. |
| `"a2a"` | Skipped | Started | Internal agents that are only called by other agents via A2A. |

When omitted, the runtime defaults to `"both"`.

## Endpoints

All HTTP bridge endpoints are served on port 8080.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `POST /invocations` | POST | Agent invocation (blocking JSON or SSE streaming) |
| `/ws` | GET (upgrade) | WebSocket bidirectional messaging |
| `/ping` | GET | Health check |

## POST /invocations (blocking)

Sends a message to the agent and waits for the complete response. This is the default mode when no `Accept: text/event-stream` header is present.

### Request

```http
POST /invocations HTTP/1.1
Content-Type: application/json
X-Amzn-Bedrock-AgentCore-Runtime-Session-Id: session-123  (optional)

{
  "prompt": "What is the capital of France?",
  "metadata": {
    "user_id": "u-abc",
    "trace_id": "t-xyz"
  }
}
```

**Request fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `prompt` | string | Yes (or `input`) | The user's message. Takes priority over `input`. |
| `input` | string | Yes (or `prompt`) | Alternative field name for the user's message. Used when `prompt` is empty. |
| `metadata` | object | No | Arbitrary metadata forwarded to the A2A server as message-level metadata. |

Any additional top-level fields beyond `prompt`, `input`, and `metadata` are captured and forwarded under `metadata.payload` to avoid collisions with explicit metadata.

**Headers:**

| Header | Required | Description |
|--------|----------|-------------|
| `Content-Type` | Yes | Must be `application/json`. |
| `X-Amzn-Bedrock-AgentCore-Runtime-Session-Id` | No | Session ID for multi-turn conversation continuity. Maps to the A2A `contextId`. |

### Response

```json
{
  "response": "The capital of France is Paris.",
  "status": "success",
  "task_id": "task-001",
  "context_id": "session-123",
  "usage": {
    "input_tokens": 12,
    "output_tokens": 8
  }
}
```

**Response fields:**

| Field | Type | Always present | Description |
|-------|------|---------------|-------------|
| `response` | string | Yes | The agent's text response. Concatenated from all artifact parts. |
| `status` | string | Yes | `"success"` or `"error"`. |
| `task_id` | string | No | The A2A task ID. Omitted when empty. |
| `context_id` | string | No | The A2A context ID (session). Omitted when empty. |
| `usage` | object | No | Token usage from the LLM. Omitted when not available. |
| `usage.input_tokens` | integer | No | Number of input tokens consumed. |
| `usage.output_tokens` | integer | No | Number of output tokens generated. |

### Error response

On failure, the response has `status: "error"` and the error message in `response`:

```json
{
  "response": "rate limit exceeded",
  "status": "error"
}
```

**HTTP status codes:**

| Code | Cause |
|------|-------|
| 200 | Success (check `status` field for application-level errors) |
| 400 | Missing or invalid JSON body, or missing `prompt`/`input` |
| 502 | A2A server unavailable |
| 500 | Internal error |

## POST /invocations (SSE streaming)

When the client sends `Accept: text/event-stream`, the bridge switches to streaming mode. Instead of waiting for the full response, it relays individual events as they arrive from the A2A server.

### Request

Same as blocking mode, with the addition of the `Accept` header:

```http
POST /invocations HTTP/1.1
Content-Type: application/json
Accept: text/event-stream
X-Amzn-Bedrock-AgentCore-Runtime-Session-Id: session-123  (optional)

{
  "prompt": "Write a short poem about clouds."
}
```

### Response

The response is a standard SSE stream. Each event is a `data:` line containing a JSON object:

```
data: {"type":"status","state":"working","task_id":"task-001","context_id":"session-123"}

data: {"type":"text","content":"Soft pillows ","task_id":"task-001","context_id":"session-123"}

data: {"type":"text","content":"drift across ","task_id":"task-001","context_id":"session-123"}

data: {"type":"text","content":"the azure sky.","task_id":"task-001","context_id":"session-123"}

data: {"type":"status","state":"completed","task_id":"task-001","context_id":"session-123"}

data: {"type":"done"}
```

**SSE event fields:**

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Event type: `"status"`, `"text"`, `"error"`, or `"done"`. |
| `content` | string | Text content (for `"text"` and `"error"` events). |
| `state` | string | Task state (for `"status"` events): `"working"`, `"completed"`, `"failed"`, `"canceled"`, `"rejected"`. |
| `task_id` | string | The A2A task ID. |
| `context_id` | string | The A2A context ID (session). |

**Event sequence:**

1. `status` with `state: "working"` -- the agent has started processing.
2. Zero or more `text` events -- incremental response chunks.
3. `status` with a terminal state (`completed`, `failed`, `canceled`, or `rejected`).
4. `done` -- signals the end of the stream. Always the last event.

**Error during stream:**

If the A2A server returns a JSON-RPC error mid-stream, it appears as an `error` event:

```
data: {"type":"error","content":"model overloaded"}
```

**Response headers:**

| Header | Value |
|--------|-------|
| `Content-Type` | `text/event-stream` |
| `Cache-Control` | `no-cache` |
| `Connection` | `keep-alive` |

## WebSocket /ws

The `/ws` endpoint provides bidirectional messaging over a persistent WebSocket connection. Each message sent by the client triggers a blocking A2A invocation, and the response is written back to the same connection.

### Connection

```
ws://host:8080/ws
```

The WebSocket upgrade uses permissive origin checks (the bridge is only reachable from within the AgentCore VPC). Maximum message size is 1 MiB.

### Client message (request)

```json
{
  "prompt": "What is 2 + 2?",
  "metadata": {
    "user_id": "u-abc"
  }
}
```

**Client message fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `prompt` | string | Yes (or `input`) | The user's message. Takes priority over `input`. |
| `input` | string | Yes (or `prompt`) | Alternative field name for the user's message. |
| `metadata` | object | No | Arbitrary metadata forwarded to the A2A server. |

### Server messages (response)

For each client message, the server sends two messages:

**Success:**

```json
{"type":"text","content":"2 + 2 = 4","task_id":"task-001","context_id":"ctx-abc","usage":{"input_tokens":8,"output_tokens":6}}
```
```json
{"type":"done"}
```

**Error:**

```json
{"type":"error","content":"agent unavailable"}
```

**Server message fields:**

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `"text"`, `"error"`, or `"done"`. |
| `content` | string | Response text (for `"text"`) or error message (for `"error"`). |
| `task_id` | string | The A2A task ID (present on `"text"` responses). |
| `context_id` | string | The A2A context ID (present on `"text"` responses). |
| `usage` | object | Token usage (present on `"text"` responses when available). |

### Connection lifecycle

- The connection stays open after each request/response exchange.
- Multiple messages can be sent sequentially on the same connection.
- If an error occurs (invalid JSON, missing prompt, A2A failure), the server sends an `error` message but keeps the connection open for subsequent messages.
- The connection closes when the client disconnects or sends a close frame.

## GET /ping

Health check endpoint. Returns the runtime's readiness status.

### Response (healthy)

```json
{"status": "healthy"}
```

HTTP 200.

### Response (draining)

```json
{"status": "draining"}
```

HTTP 503. Returned during graceful shutdown after SIGTERM/SIGINT.

## Protocol selection guide

| Scenario | Recommended protocol | Why |
|----------|---------------------|-----|
| Simple request/response | Blocking `/invocations` | Simplest integration. Single HTTP call. |
| Real-time token streaming | SSE `/invocations` | Low-latency incremental output. Standard SSE client libraries. |
| Interactive chat UI | WebSocket `/ws` | Persistent connection avoids per-message overhead. Supports multi-turn without reconnecting. |
| Agent-to-agent calls | A2A (port 9000) | Native A2A protocol with task lifecycle management. |
| Health monitoring | `GET /ping` | Lightweight liveness probe for load balancers. |

## Example: curl

**Blocking:**

```bash
curl -X POST http://localhost:8080/invocations \
  -H "Content-Type: application/json" \
  -d '{"prompt": "Hello"}'
```

**SSE streaming:**

```bash
curl -X POST http://localhost:8080/invocations \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"prompt": "Tell me a story"}'
```

**Health check:**

```bash
curl http://localhost:8080/ping
```

## Example: JavaScript (SSE)

```javascript
const response = await fetch("http://localhost:8080/invocations", {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    "Accept": "text/event-stream",
  },
  body: JSON.stringify({ prompt: "Tell me a story" }),
});

const reader = response.body.getReader();
const decoder = new TextDecoder();
let buffer = "";

while (true) {
  const { done, value } = await reader.read();
  if (done) break;

  buffer += decoder.decode(value, { stream: true });
  const lines = buffer.split("\n");
  buffer = lines.pop(); // keep incomplete line

  for (const line of lines) {
    if (!line.startsWith("data: ")) continue;
    const event = JSON.parse(line.slice(6));

    if (event.type === "text") process.stdout.write(event.content);
    if (event.type === "done") console.log("\n[done]");
  }
}
```

## Example: JavaScript (WebSocket)

```javascript
const ws = new WebSocket("ws://localhost:8080/ws");

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === "text") console.log("Agent:", msg.content);
  if (msg.type === "error") console.error("Error:", msg.content);
  if (msg.type === "done") console.log("[done]");
};

ws.onopen = () => {
  ws.send(JSON.stringify({ prompt: "What is the meaning of life?" }));
};
```
