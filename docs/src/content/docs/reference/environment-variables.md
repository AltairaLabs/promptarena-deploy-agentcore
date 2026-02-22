---
title: Environment Variables
sidebar:
  order: 3
---

The AgentCore adapter injects environment variables into agent runtimes via the `EnvironmentVariables` field on `CreateAgentRuntime` and `UpdateAgentRuntime` API calls. These variables allow runtime code to discover configuration, peer agents, and deployed resources.

## Variable reference

| Variable | Source | When Set | Description |
|----------|--------|----------|-------------|
| `PROMPTPACK_PROVIDER_TYPE` | Arena config `deploy.agentcore` | Always (code deploy) | LLM provider type (e.g. `"bedrock"`). Used by the runtime to select the correct provider. |
| `PROMPTPACK_PROVIDER_MODEL` | Arena config `deploy.agentcore.model` | Always (code deploy) | Bedrock model ID (e.g. `"claude-3-5-haiku-20241022"`). Used by the runtime to configure the LLM. |
| `PROMPTPACK_PACK_JSON` | Pack file contents | Always (code deploy) | The full pack JSON, injected so the runtime can load the pack without a separate file. |
| `PROMPTPACK_LOG_GROUP` | `observability.cloudwatch_log_group` | When `cloudwatch_log_group` is a non-empty string | CloudWatch log group name for structured logging. |
| `PROMPTPACK_TRACING_ENABLED` | `observability.tracing_enabled` | When `tracing_enabled` is `true` | Enables AWS X-Ray tracing. Value is the string `"true"`. |
| `PROMPTPACK_MEMORY_STORE` | `memory_store` config field | When `memory_store` is set | Memory store type: `"session"` or `"persistent"`. |
| `PROMPTPACK_MEMORY_ID` | Memory resource ARN | After memory resource creation during Apply | The ARN of the created memory resource. Allows runtimes to connect to the memory store. |
| `PROMPTPACK_AGENTS` | Runtime resource ARNs | Multi-agent packs only, after all runtimes are created | JSON object mapping agent member names to their runtime ARNs. Injected on the entry agent only. |
| `PROMPTPACK_A2A_AUTH_MODE` | `a2a_auth.mode` | When `a2a_auth` is configured with a non-empty `mode` | A2A authentication mode: `"iam"` or `"jwt"`. |
| `PROMPTPACK_A2A_AUTH_ROLE` | `runtime_role_arn` | When `a2a_auth.mode` is `"iam"` | The IAM role ARN used for A2A authentication between agents. |
| `PROMPTPACK_POLICY_ENGINE_ARN` | Cedar policy resource ARNs | After Cedar policy creation during Apply | Comma-separated list of policy engine ARNs. Set when prompts define validators or tool_policy. |
| `PROMPTPACK_METRICS_CONFIG` | Pack evals with metrics | When at least one eval defines a `metric` | JSON `MetricsConfig` object describing CloudWatch metrics for eval reporting. |
| `PROMPTPACK_DASHBOARD_CONFIG` | Pack structure (agents + evals) | When the pack has agents or eval metrics | JSON `DashboardConfig` object describing a CloudWatch dashboard layout. |
| `PROMPTPACK_PROTOCOL` | `protocol` config field | When `protocol` is set to a non-empty value | Server protocol mode: `"http"`, `"a2a"`, or `"both"`. Controls which servers the runtime starts. See [Runtime Protocols](/reference/runtime-protocols/). |
| `PROMPTPACK_AGENT` | Pack prompt/agent name | Multi-agent packs; set per-runtime | The agent name this runtime serves. Omitted for single-agent packs to allow auto-discovery. |

## Variable details

### PROMPTPACK_PROVIDER_TYPE

Set from the arena config's `deploy.agentcore` section. Tells the runtime which LLM provider to use. For AgentCore deployments, this is always `"bedrock"`.

```
PROMPTPACK_PROVIDER_TYPE=bedrock
```

### PROMPTPACK_PROVIDER_MODEL

Set from the arena config's `deploy.agentcore.model` field. Specifies the Bedrock model ID the runtime should use for LLM invocations.

```
PROMPTPACK_PROVIDER_MODEL=claude-3-5-haiku-20241022
```

### PROMPTPACK_PACK_JSON

Injected during code deploy. Contains the entire compiled pack JSON so the runtime can load the pack directly from the environment without needing a separate file on disk.

```
PROMPTPACK_PACK_JSON={"id":"my-agent","version":"v1.0.0","prompts":{...}}
```

### PROMPTPACK_LOG_GROUP

Set from `observability.cloudwatch_log_group`. Runtimes use this to direct structured logs to the specified CloudWatch log group.

```
PROMPTPACK_LOG_GROUP=/aws/agentcore/my-pack
```

### PROMPTPACK_TRACING_ENABLED

Set from `observability.tracing_enabled`. Only injected when the value is `true`. Runtimes use this to enable X-Ray trace instrumentation.

```
PROMPTPACK_TRACING_ENABLED=true
```

### PROMPTPACK_MEMORY_STORE

Set from the top-level `memory_store` config field. Tells the runtime which memory strategy to use.

```
PROMPTPACK_MEMORY_STORE=session
```

### PROMPTPACK_MEMORY_ID

Injected at Apply time after the memory resource is successfully created. Contains the full ARN of the memory resource.

```
PROMPTPACK_MEMORY_ID=arn:aws:bedrock:us-west-2:123456789012:memory/abc123
```

### PROMPTPACK_AGENTS

Injected on the **entry agent only** in multi-agent packs, after all agent runtimes are created. Contains a JSON object mapping each agent member name to its runtime ARN.

```json
{
  "planner": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-001",
  "researcher": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-002",
  "writer": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-003"
}
```

Only runtimes with status `"created"` or `"updated"` are included in the map. If a member runtime failed to create, it is omitted.

### PROMPTPACK_A2A_AUTH_MODE

Set when `a2a_auth` is configured. Indicates the authentication mechanism used for agent-to-agent communication.

```
PROMPTPACK_A2A_AUTH_MODE=iam
```

### PROMPTPACK_A2A_AUTH_ROLE

Set when `a2a_auth.mode` is `"iam"` and `runtime_role_arn` is non-empty. Provides the IAM role ARN that agents use to authenticate with each other.

```
PROMPTPACK_A2A_AUTH_ROLE=arn:aws:iam::123456789012:role/AgentCoreRuntime
```

### PROMPTPACK_POLICY_ENGINE_ARN

Injected after all Cedar policy resources are created. Contains a comma-separated list of policy engine ARNs, one per prompt that has validators or tool_policy.

```
PROMPTPACK_POLICY_ENGINE_ARN=arn:aws:bedrock:us-west-2:123456789012:policy-engine/pe-001,arn:aws:bedrock:us-west-2:123456789012:policy-engine/pe-002
```

### PROMPTPACK_METRICS_CONFIG

Injected when at least one eval in the pack defines a `metric`. Contains a JSON `MetricsConfig` object that describes the CloudWatch metrics the runtime should emit.

**Example payload:**

```json
{
  "namespace": "PromptPack/Evals",
  "dimensions": {
    "pack_id": "my-pack",
    "agent": "multi"
  },
  "metrics": [
    {
      "eval_id": "accuracy_eval",
      "metric_name": "accuracy_score",
      "metric_type": "gauge",
      "unit": "None"
    },
    {
      "eval_id": "latency_eval",
      "metric_name": "response_latency",
      "metric_type": "histogram",
      "unit": "Milliseconds"
    },
    {
      "eval_id": "error_eval",
      "metric_name": "error_count",
      "metric_type": "counter",
      "unit": "Count"
    }
  ],
  "alarms": [
    {
      "metric_name": "accuracy_score",
      "min": 0.8
    },
    {
      "metric_name": "response_latency",
      "max": 5000.0
    }
  ]
}
```

**MetricsConfig fields:**

| Field | Type | Description |
|-------|------|-------------|
| `namespace` | string | CloudWatch namespace. Always `"PromptPack/Evals"`. |
| `dimensions` | map[string]string | Dimension key-value pairs. Always includes `pack_id`. Includes `"agent": "multi"` for multi-agent packs. |
| `metrics` | MetricEntry[] | One entry per eval metric. |
| `alarms` | AlarmEntry[] | Optional. One entry per metric that defines a range. |

**MetricEntry fields:**

| Field | Type | Description |
|-------|------|-------------|
| `eval_id` | string | The eval identifier from the pack definition. |
| `metric_name` | string | CloudWatch metric name. |
| `metric_type` | string | Metric type from the eval definition (e.g. `"gauge"`, `"counter"`, `"histogram"`, `"boolean"`). |
| `unit` | string | CloudWatch unit. Mapped from metric type: `counter` -> `"Count"`, `histogram` -> `"Milliseconds"`, `gauge`/`boolean` -> `"None"`. |

**AlarmEntry fields:**

| Field | Type | Description |
|-------|------|-------------|
| `metric_name` | string | The metric this alarm applies to. |
| `min` | float64 (optional) | Minimum acceptable value. |
| `max` | float64 (optional) | Maximum acceptable value. |

### PROMPTPACK_DASHBOARD_CONFIG

Injected when the pack has agents or eval metrics. Contains a JSON `DashboardConfig` object that describes a CloudWatch dashboard layout with widgets for agent runtime metrics, A2A latency (multi-agent), and eval metrics.

**Example payload (multi-agent pack with one eval metric):**

```json
{
  "widgets": [
    {
      "type": "metric",
      "x": 0,
      "y": 0,
      "width": 12,
      "height": 6,
      "properties": {
        "title": "Agent: planner",
        "region": "us-west-2",
        "period": 300,
        "metrics": [
          ["PromptPack/Evals", "Invocations", "agent", "planner"],
          ["PromptPack/Evals", "Errors", "agent", "planner"],
          ["PromptPack/Evals", "Duration", "agent", "planner"]
        ]
      }
    },
    {
      "type": "metric",
      "x": 12,
      "y": 0,
      "width": 12,
      "height": 6,
      "properties": {
        "title": "Agent: researcher",
        "region": "us-west-2",
        "period": 300,
        "metrics": [
          ["PromptPack/Evals", "Invocations", "agent", "researcher"],
          ["PromptPack/Evals", "Errors", "agent", "researcher"],
          ["PromptPack/Evals", "Duration", "agent", "researcher"]
        ]
      }
    },
    {
      "type": "metric",
      "x": 0,
      "y": 6,
      "width": 24,
      "height": 6,
      "properties": {
        "title": "Inter-Agent A2A Call Latency",
        "region": "us-west-2",
        "period": 300,
        "metrics": [
          ["PromptPack/Evals", "A2ALatency", "agent", "planner"],
          ["PromptPack/Evals", "A2ALatency", "agent", "researcher"]
        ]
      }
    },
    {
      "type": "metric",
      "x": 0,
      "y": 12,
      "width": 12,
      "height": 6,
      "properties": {
        "title": "Eval: accuracy_score",
        "region": "us-west-2",
        "period": 300,
        "metrics": [
          ["PromptPack/Evals", "accuracy_score", "pack_id", "my-pack"]
        ],
        "annotations": {
          "horizontal": [
            { "label": "min", "value": 0.8, "color": "#2ca02c" },
            { "label": "max", "value": 1.0, "color": "#d62728" }
          ]
        }
      }
    }
  ]
}
```

**DashboardConfig fields:**

| Field | Type | Description |
|-------|------|-------------|
| `widgets` | DashboardWidget[] | Ordered list of dashboard widgets. |

**DashboardWidget fields:**

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Always `"metric"`. |
| `x` | int | Horizontal grid position (0 or 12 for two-column layout). |
| `y` | int | Vertical grid position. Increments by 6 (widget height) per row. |
| `width` | int | Widget width. 12 for standard widgets, 24 for full-width (A2A latency). |
| `height` | int | Widget height. Always 6. |
| `properties` | object | Widget display properties including title, region, period, metrics, and optional annotations. |

**Widget types generated:**

| Widget | When Generated | Layout |
|--------|---------------|--------|
| Agent runtime metrics (Invocations, Errors, Duration) | One per agent member (or pack ID for single-agent) | Two-column, 12 units wide |
| A2A call latency | Multi-agent packs only | Full-width, 24 units wide |
| Eval metric | One per eval with a `metric` definition | Two-column, 12 units wide, with optional min/max threshold annotations |

### PROMPTPACK_PROTOCOL

Set from the `protocol` config field. Controls which servers the runtime starts: the HTTP bridge (port 8080), the A2A server (port 9000), or both.

```
PROMPTPACK_PROTOCOL=both
```

See [Runtime Protocols](/reference/runtime-protocols/) for details on the HTTP bridge endpoints and payload formats.

### PROMPTPACK_AGENT

Set per-runtime in multi-agent packs. Contains the agent/prompt name this runtime serves. Omitted for single-agent packs to allow the runtime to auto-discover the single prompt from the pack.

```
PROMPTPACK_AGENT=coordinator
```

## Injection timing

Environment variables are built and injected at different points during the Apply lifecycle:

| Timing | Variables |
|--------|-----------|
| Before any resource creation | `PROMPTPACK_PROVIDER_TYPE`, `PROMPTPACK_PROVIDER_MODEL`, `PROMPTPACK_PACK_JSON`, `PROMPTPACK_LOG_GROUP`, `PROMPTPACK_TRACING_ENABLED`, `PROMPTPACK_MEMORY_STORE`, `PROMPTPACK_A2A_AUTH_MODE`, `PROMPTPACK_A2A_AUTH_ROLE`, `PROMPTPACK_METRICS_CONFIG`, `PROMPTPACK_DASHBOARD_CONFIG`, `PROMPTPACK_PROTOCOL`, `PROMPTPACK_AGENT` |
| After memory creation (pre-step) | `PROMPTPACK_MEMORY_ID` |
| After Cedar policy creation (phase 2) | `PROMPTPACK_POLICY_ENGINE_ARN` |
| After runtime creation (phase 3) | `PROMPTPACK_AGENTS` (injected via UpdateRuntime on entry agent) |

Variables injected before resource creation are available to all runtimes at creation time. Variables injected after a phase require a subsequent `UpdateAgentRuntime` call to propagate to already-created runtimes.
