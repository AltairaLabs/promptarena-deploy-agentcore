---
title: Resource Types
sidebar:
  order: 2
---

The AgentCore adapter manages seven resource types. Each resource has a constant name used in state serialization, a mapping to the PromptPack concept it represents, and defined create/update/delete/health-check behavior.

## Resource type summary

| Constant | String Value | Pack Concept | Create | Update | Delete | Health Check |
|----------|-------------|--------------|--------|--------|--------|--------------|
| `ResTypeMemory` | `memory` | Memory store config | Yes | No | Yes | Status ACTIVE |
| `ResTypeToolGateway` | `tool_gateway` | Pack tools | Yes | No | Yes | Status READY |
| `ResTypeCedarPolicy` | `cedar_policy` | Prompt validators / tool_policy | Yes | No | Yes | Engine ACTIVE |
| `ResTypeAgentRuntime` | `agent_runtime` | Agent members (or pack ID) | Yes | Yes | Yes | Status READY |
| `ResTypeA2AEndpoint` | `a2a_endpoint` | Multi-agent wiring | Yes | No | No-op | Always healthy |
| `ResTypeEvaluator` | `evaluator` | Pack evals (`llm_as_judge` only) | Yes | No | Yes | Status ACTIVE |
| `ResTypeOnlineEvalConfig` | `online_eval_config` | Wires evaluators to agent traces | Yes | No | Yes | Status ACTIVE |

## Resource status values

Resources pass through these status values during their lifecycle:

| Status | Constant | Meaning |
|--------|----------|---------|
| `created` | `ResStatusCreated` | Resource was successfully created during Apply. |
| `updated` | `ResStatusUpdated` | Resource was successfully updated during Apply (redeployment). |
| `failed` | `ResStatusFailed` | Resource creation or update failed. The error is reported via callback. |
| `planned` | `ResStatusPlanned` | Resource would be created (dry-run mode only). |

Health check status values returned by Status:

| Status | Constant | Meaning |
|--------|----------|---------|
| `healthy` | `StatusHealthy` | Resource exists and is in its expected ready/active state. |
| `unhealthy` | `StatusUnhealthy` | Resource exists but is not in the expected state, or the check returned an error. |
| `missing` | `StatusMissing` | Resource was not found (404/NotFound from AWS). |

---

## `memory`

**Constant:** `ResTypeMemory`
**String value:** `"memory"`

### Pack mapping

Created when `memory_store` is set in the deploy config. The resource name is `{pack_id}_memory`. One memory resource is created per pack.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create | `CreateMemory` | Provisions a Bedrock AgentCore memory with the configured strategy (episodic for `"session"`, semantic for `"persistent"`). Sets event expiry to 30 days. |
| Delete | `DeleteMemory` | Deletes the memory resource by ID. Tolerates NotFound (already deleted). |

### Health check

Calls `GetMemory` and checks that `Memory.Status` equals `ACTIVE`.

| Result | Condition |
|--------|-----------|
| `healthy` | Status is `ACTIVE` |
| `unhealthy` | Status is any other value, or API error |
| `missing` | NotFound error |

### Side effects

On successful creation, the memory ARN is injected into `PROMPTPACK_MEMORY_ID` on the runtime config so that agent runtimes can discover the memory resource.

---

## `tool_gateway`

**Constant:** `ResTypeToolGateway`
**String value:** `"tool_gateway"`

### Pack mapping

One `tool_gateway` resource is created per entry in `pack.Tools`. Resources are created in sorted key order.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create (parent) | `CreateGateway` | Lazily creates a shared parent gateway on the first tool. The gateway uses MCP protocol type and no authorizer. Polls until READY. |
| Create (target) | `CreateGatewayTarget` | Creates a gateway target for each tool within the shared gateway. |
| Delete | `DeleteGateway` | Deletes the parent gateway by ID. Tolerates NotFound. |

The parent gateway is created lazily on the first `CreateGatewayTool` call and reused for all subsequent targets within the same Apply invocation. The gateway name is `{first_tool_name}_gw`.

### Health check

Calls `GetGateway` and checks that `Status` equals `READY`.

| Result | Condition |
|--------|-----------|
| `healthy` | Status is `READY` |
| `unhealthy` | Status is any other value, or API error |
| `missing` | NotFound error |

### Update support

Not supported. Redeployment creates new gateway targets.

---

## `cedar_policy`

**Constant:** `ResTypeCedarPolicy`
**String value:** `"cedar_policy"`

### Pack mapping

One `cedar_policy` resource is created per prompt that has `validators` or `tool_policy` defined. The adapter generates Cedar policy statements from these definitions.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create (engine) | `CreatePolicyEngine` | Creates a policy engine per prompt. Polls until engine status is `ACTIVE`. |
| Create (policy) | `CreatePolicy` | Creates a Cedar policy within the engine using the generated statement. |
| Delete (policy) | `DeletePolicy` | Deletes the Cedar policy by engine ID and policy ID. Tolerates NotFound. |
| Delete (engine) | `DeletePolicyEngine` | Deletes the policy engine by ID. Tolerates NotFound. |

### Health check

Calls `GetPolicyEngine` and checks that `Status` equals `ACTIVE`.

| Result | Condition |
|--------|-----------|
| `healthy` | Engine status is `ACTIVE` |
| `unhealthy` | Engine status is any other value, or API error |
| `missing` | NotFound error, or no `policy_engine_id` in metadata |

### Metadata

The resource state stores additional metadata used for deletion and health checks:

| Key | Description |
|-----|-------------|
| `policy_engine_id` | The policy engine identifier. |
| `policy_engine_arn` | The policy engine ARN. Used to populate `PROMPTPACK_POLICY_ENGINE_ARN`. |
| `policy_id` | The Cedar policy identifier within the engine. |

### Side effects

After all policy resources are created, the adapter injects `PROMPTPACK_POLICY_ENGINE_ARN` into the runtime config as a comma-separated list of engine ARNs.

### Update support

Not supported.

---

## `agent_runtime`

**Constant:** `ResTypeAgentRuntime`
**String value:** `"agent_runtime"`

### Pack mapping

For multi-agent packs, one runtime is created per agent member (using the agent name). For single-agent packs, one runtime is created using the pack ID.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create | `CreateAgentRuntime` | Provisions an AgentCore runtime with the configured role, environment variables, authorizer, and tags. Polls until status is `READY`. |
| Update | `UpdateAgentRuntime` | Updates an existing runtime with new environment variables and authorizer config. Polls until status is `READY`. Triggered on redeployment when the resource exists in prior state. |
| Delete | `DeleteAgentRuntime` | Deletes the runtime by ID. Tolerates NotFound. |

This is the only resource type that supports update. On redeployment, if a runtime with the same type and name exists in the prior state, the adapter calls `UpdateAgentRuntime` instead of `CreateAgentRuntime`.

### Health check

Calls `GetAgentRuntime` and checks that `Status` equals `READY`.

| Result | Condition |
|--------|-----------|
| `healthy` | Status is `READY` |
| `unhealthy` | Status is `CREATE_FAILED`, `UPDATE_FAILED`, or any non-READY value |
| `missing` | NotFound error |

### Polling behavior

After creation or update, the adapter polls `GetAgentRuntime` every 5 seconds for up to 60 attempts (5 minutes). Terminal failure states (`CREATE_FAILED`, `UPDATE_FAILED`) abort polling immediately. The runtime ARN is returned even if polling fails, allowing the state to record a partial result.

### Side effects

For multi-agent packs, after all runtimes are created, the adapter builds a JSON map of `{agentName: runtimeARN}` and injects it as `PROMPTPACK_AGENTS` on the entry agent via an `UpdateAgentRuntime` call.

---

## `a2a_endpoint`

**Constant:** `ResTypeA2AEndpoint`
**String value:** `"a2a_endpoint"`

### Pack mapping

One `a2a_endpoint` resource is created per agent member in multi-agent packs. The resource name is `{agent_name}_a2a`. Not created for single-agent packs.

### AWS API calls

This is a **logical resource**. No separate AWS API call is made. The AgentCore runtime exposes A2A endpoints when configured with the appropriate environment variables.

| Operation | API Call | Details |
|-----------|----------|---------|
| Create | None | Returns a placeholder ARN: `arn:aws:bedrock:{region}:a2a-endpoint/{name}` |
| Delete | No-op | Logged and skipped. |

### Health check

Always returns `healthy`. No AWS API call is made.

### Update support

Not supported.

---

## `evaluator`

**Constant:** `ResTypeEvaluator`
**String value:** `"evaluator"`

### Pack mapping

One `evaluator` resource is created per `llm_as_judge` eval in `pack.Evals`. Other eval types (regex, contains, etc.) are local-only and do not create AWS resources. The resource name is the eval's `ID` field, or `eval_{index}` if the ID is empty.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create | `CreateEvaluator` | Provisions an LLM-as-a-Judge evaluator with instructions, model config, and a numerical rating scale. Polls until status is `ACTIVE`. |
| Delete | `DeleteEvaluator` | Deletes the evaluator by ID. Tolerates NotFound (already deleted). |

The eval definition's `trigger` field maps to the SDK evaluator level: `every_turn` and `sample_turns` map to `TRACE`, while `on_session_complete` and `sample_sessions` map to `SESSION`.

The `params` map supports the following keys:

| Key | Default | Description |
|-----|---------|-------------|
| `instructions` | `"Evaluate the agent response quality."` | Evaluation instructions for the LLM judge. |
| `model` | `anthropic.claude-sonnet-4-20250514-v1:0` | Bedrock model ID for evaluation. |
| `rating_scale_size` | `5` | Number of levels in the numerical 1–N rating scale. |

### Health check

Calls `GetEvaluator` and checks that `Status` equals `ACTIVE`.

| Result | Condition |
|--------|-----------|
| `healthy` | Status is `ACTIVE` |
| `unhealthy` | Status is any other value, or API error |
| `missing` | NotFound error |

### Polling behavior

After creation, the adapter polls `GetEvaluator` every 5 seconds for up to 60 attempts (5 minutes). Terminal failure states (`CREATE_FAILED`, `UPDATE_FAILED`) abort polling immediately.

### Update support

Not supported.

---

## `online_eval_config`

**Constant:** `ResTypeOnlineEvalConfig`
**String value:** `"online_eval_config"`

### Pack mapping

One `online_eval_config` resource is created per pack when the pack has any `llm_as_judge` evals. The resource name is `{pack_id}_online_eval`. It wires the evaluators created in the previous phase to agent runtime traces via CloudWatch logs.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create | `CreateOnlineEvaluationConfig` | Creates an online evaluation config referencing all evaluator IDs, a CloudWatch data source, and a sampling rule. Polls until status is `ACTIVE`. |
| Delete | `DeleteOnlineEvaluationConfig` | Deletes the config by ID. Tolerates NotFound (already deleted). |

The CloudWatch log group is resolved from `observability.cloudwatch_log_group` if configured, otherwise defaults to `/aws/bedrock/agentcore/{pack_id}`. The sampling percentage defaults to 100% but can be overridden via the `sample_percentage` eval param.

### Health check

Calls `GetOnlineEvaluationConfig` and checks that `Status` equals `ACTIVE`.

| Result | Condition |
|--------|-----------|
| `healthy` | Status is `ACTIVE` |
| `unhealthy` | Status is any other value, or API error |
| `missing` | NotFound error |

### Polling behavior

After creation, the adapter polls `GetOnlineEvaluationConfig` every 5 seconds for up to 60 attempts (5 minutes). Terminal failure states (`CREATE_FAILED`, `UPDATE_FAILED`) abort polling immediately.

### Update support

Not supported.

---

## Deploy phase ordering

Resources are created during Apply in dependency order across six phases:

| Phase | Step Index | Resource Type | Progress Range |
|-------|-----------|---------------|----------------|
| Pre-step | -- | `memory` | 0% |
| 1 | 0 | `tool_gateway` | 0--17% |
| 2 | 1 | `cedar_policy` | 17--33% |
| 3 | 2 | `agent_runtime` | 33--50% |
| 4 | 3 | `a2a_endpoint` | 50--67% |
| 5 | 4 | `evaluator` | 67--83% |
| 6 | 5 | `online_eval_config` | 83--100% |

## Destroy ordering

Resources are destroyed in reverse dependency order:

1. `online_eval_config`
2. `cedar_policy`
3. `evaluator`
4. `a2a_endpoint`
5. `agent_runtime`
6. `tool_gateway`
7. `memory`

Any resource types not in this list are destroyed last, after the ordered groups.
