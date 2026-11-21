---
title: Resource Types
sidebar:
  order: 2
---

The AgentCore adapter manages eight resource types. Each resource has a constant name used in state serialization, a mapping to the PromptPack concept it represents, and defined create/update/delete/health-check behavior.

## Resource type summary

| Constant | String Value | Pack Concept | Create | Update | Delete | Health Check |
|----------|-------------|--------------|--------|--------|--------|--------------|
| `ResTypeMemory` | `memory` | Memory store config | Yes | No | Yes | Status ACTIVE |
| `ResTypeToolGateway` | `tool_gateway` | Pack tools | Yes | No | Yes | Status READY |
| `ResTypeCedarPolicy` | `cedar_policy` | Prompt validators / tool_policy | Yes | No | Yes | Engine ACTIVE |
| `ResTypeAgentRuntime` | `agent_runtime` | Agent members (or pack ID) | Yes | Yes | Yes | Status READY |
| `ResTypeRuntimeEndpoint` | `runtime_endpoint` | The `live` and `candidate` endpoints of each runtime | Yes | Yes (repoint) | Yes | Status READY |
| `ResTypeA2AEndpoint` | `a2a_endpoint` | Multi-agent wiring | Yes | No | No-op | Always healthy |
| `ResTypeEvaluator` | `evaluator` | Pack evals (`llm_as_judge` only) | Yes | No | Yes | Status ACTIVE |
| `ResTypeOnlineEvalConfig` | `online_eval_config` | Wires evaluators to agent traces | Yes | Yes | Yes | Status ACTIVE |

## Resource status values

Resources pass through these status values during their lifecycle:

| Status | Constant | Meaning |
|--------|----------|---------|
| `created` | `ResStatusCreated` | Resource was successfully created during Apply. |
| `updated` | `ResStatusUpdated` | Resource was successfully updated during Apply (redeployment). |
| `failed` | `ResStatusFailed` | Resource creation or update failed. The error is reported via callback. A failed apply's state never records it; see [Error handling](/explanation/resource-lifecycle/#error-handling). |
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
| Delete | `DeleteGateway` | Deletes the parent gateway by ID. Tolerates NotFound. Destroy deletes a gateway that several tools share once, and reports each tool deleted. |

One parent gateway holds every tool's target. When the prior state's tools already have a gateway, apply keeps that gateway, whatever it is called, and adds to it. Otherwise it creates one named `{pack_id}-gw`. The pack id is made AWS-safe first: leading digits are dropped, any other character that is not a letter or digit becomes a hyphen, the name is cut to fit the 48-character limit, and an empty id gives `default-gw`. Because the name comes from the pack, adding or removing a tool never moves the tools to a different gateway. A prior gateway that is being deleted or failed to create is replaced; the old one is then deleted.

Releases before 0.4.3 named the gateway after the alphabetically first tool. If you added a tool that sorted ahead of it under one of those releases, an earlier gateway may be left in your account that no state records. Once you have confirmed it has no targets, delete it in the AgentCore console.

The resource name of a `tool_gateway` entry is the tool's name, and every entry records the parent gateway's ARN. When a tool is removed, apply deletes only its target. The gateway is deleted when no tool is left on it.

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

One `cedar_policy` resource is created per pack, named `{pack}_policy_engine`, when a prompt's `tool_policy.blocklist` can be enforced at the gateway. Validators, `max_rounds` and `max_tool_calls_per_turn` are enforced by the runtime and produce no Cedar.

#### How a blocklist is enforced

A prompt's `tool_policy.blocklist` is enforced in two places:

1. **The runtime is the primary control.** It enforces each prompt's own `tool_policy`. When the model asks for a tool its prompt blocklists, the runtime refuses the call before it runs and returns `Tool <name> is blocked by policy` to the model. This holds for every tool, wherever it runs.
2. **The gateway is defence in depth.** The pack's Cedar policy engine is a floor for the whole pack. It holds even when something calls the gateway directly, without going through the runtime.

The gateway's floor covers the whole pack because the gateway cannot tell which prompt a call came from. Every prompt of the pack calls its tools through the same gateway, and a gateway takes only one policy engine. So the engine forbids a tool only when **no prompt may use it**: every prompt either blocklists the tool or leaves it out of its `tools` list.

- **A tool blocked by one prompt and allowed by another** is not forbidden at the gateway, because that would refuse it to the prompt that is allowed it. For the prompt that blocks it, only the runtime enforces the blocklist. Apply logs a warning naming each such tool: `tool "<name>" is blocklisted by some prompts but callable by others; the shared gateway cannot tell prompts apart, so only the runtime enforces it`.
- **A prompt with no `tools` list** counts as allowing every tool. A tool it does not blocklist is therefore never forbidden at the gateway. Give each prompt an explicit `tools` list if you want the gateway to forbid tools that only some prompts use.
- **A blocklist entry that names no pack tool** is ignored. That tool cannot be called anyway.

When no tool meets that rule, the pack gets no policy engine, and the runtime alone enforces the blocklists.

For example, suppose `billing` uses `[lookup]` and blocklists `refund`, and `support` uses `[lookup, refund]`. The gateway does not forbid `refund`, because `support` may call it. The runtime still refuses `refund` to `billing`. If `support` also blocklisted `refund`, or did not list it, the gateway would forbid it as well.

Each blocklisted tool that is on the gateway becomes one forbid policy on the action `<target>___<tool>`. The target is the tool's gateway target name, which has the tool's underscores as hyphens, so `search_d` is the action `search-d___search_d`. A policy engine denies every call no policy permits, so the forbids are followed by one permit for every action on the gateway. A forbid overrides a permit, so the blocklisted tools are denied and the rest are allowed.

The policies are created with the `IGNORE_ALL_FINDINGS` validation mode. The Cedar analyzer reports every blocklist forbid as "Overly Restrictive", because it denies every call to that tool, which is the point of a blocklist.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Create (engine) | `CreatePolicyEngine` | Creates the pack's policy engine and associates it with the pack's gateway. Polls until engine status is `ACTIVE`. |
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

After the policy engine is created, the adapter injects its ARN into the runtime config as `PROMPTPACK_POLICY_ENGINE_ARN`.

A deployment made before v0.4.5 has one engine per prompt, named `{prompt}_policy_engine`. The next apply creates the pack's engine, associates it with the gateway in their place, and then deletes them.

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
| Create | `CreateAgentRuntime` | Provisions an AgentCore runtime with the configured role, environment variables, authorizer, and tags. Its description is `pack_version=<pack version>`. Polls until status is `READY`. |
| Update | `UpdateAgentRuntime` | Updates an existing runtime with new environment variables and authorizer config, which makes a new runtime version. The description keeps the current text and ends with `pack_version=<pack version>`, replacing any earlier token. Polls until status is `READY`. Triggered on redeployment when the resource exists in prior state. |
| Delete | `DeleteAgentRuntime` | Deletes the runtime by ID. Tolerates NotFound. |

On redeployment, if a runtime with the same type and name exists in the prior state, the adapter calls `UpdateAgentRuntime` instead of `CreateAgentRuntime`.

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

### Metadata

| Key | Value |
|-----|-------|
| `latest_version` | The runtime version this apply ended on. `DEFAULT` serves it. |
| `pack_version` | The pack version that version was deployed from. |

---

## `runtime_endpoint`

**Constant:** `ResTypeRuntimeEndpoint`
**String value:** `"runtime_endpoint"`

### Pack mapping

Up to two per agent runtime, named `{runtime}/{endpoint}`: the live endpoint (`live_endpoint`, default `live`) and the candidate endpoint (`candidate_endpoint`, default `candidate`). Which one a deploy moves depends on [`rollout_stage`](/how-to/configure#rollout_stage-live_endpoint-candidate_endpoint). The other one stays where it is, and plan shows it as unchanged, with the version it serves.

### AWS API calls

| Operation | API Call | Details |
|-----------|----------|---------|
| Read version | `GetAgentRuntime` | The runtime's latest version, read after the apply's last update to the runtime. |
| Create | `CreateAgentRuntimeEndpoint` | Creates the endpoint on that version. Polls `GetAgentRuntimeEndpoint` until it is `READY` on the version. |
| Update | `UpdateAgentRuntimeEndpoint` | Repoints the endpoint. Skipped when it is already `READY` on the version. Polls as for create. |
| Promote | `GetAgentRuntimeEndpoint` | Reads the version the candidate endpoint serves, then points live at it. |
| Delete | `DeleteAgentRuntimeEndpoint` | Deletes the endpoint and polls until it is gone, so that the runtime can be deleted after it. Tolerates NotFound. |

An apply that has already failed points no endpoint, so a version from an unfinished apply is never served on `live`. The adapter never deletes runtime versions. It cannot remove one that an endpoint points at.

### Health check

Calls `GetAgentRuntimeEndpoint`. Status reports the version the endpoint serves, read from AgentCore.

| Result | Condition |
|--------|-----------|
| `healthy` | Status is `READY` |
| `unhealthy` | Any other status, or the call fails |
| `missing` | NotFound, or status `DELETING` |

### Metadata

| Key | Value |
|-----|-------|
| `runtime_arn` | ARN of the runtime the endpoint belongs to |
| `endpoint` | Endpoint name, for example `live` |
| `version` | The runtime version the endpoint serves. For the candidate endpoint, this is the staged version. |

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

One `evaluator` resource is created per `llm_as_judge` eval in `pack.Evals`. Other eval types (regex, contains, etc.) are local-only and do not create AWS resources. The resource name is `{pack}_{eval id}`, or `{pack}_eval_{index}` if the ID is empty, where `{pack}` is the pack id with every character AWS does not accept replaced by `_`. A name over 48 characters keeps its start and ends in a short hash of the whole.

Evaluator names are account-wide, so the pack prefix keeps two packs with an eval of the same id apart. Before v0.4.5 the name was the eval id alone; the next apply creates the prefixed evaluator, points the online eval config at it, and then deletes the old one.

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
| `model` | The first model the region can serve (see below) | Bedrock foundation model ID, inference profile ID, or either's ARN. |
| `rating_scale_size` | `5` | Number of levels in the numerical 1–N rating scale. |

`CreateEvaluator` takes a foundation model that the region runs on demand, or an inference profile. Current Claude models are offered only through inference profiles, so in us-east-1 `anthropic.claude-haiku-4-5-20251001-v1:0` is refused and `us.anthropic.claude-haiku-4-5-20251001-v1:0` works.

Before creating anything, Apply lists the region's foundation models (`ListFoundationModels`) and inference profiles (`ListInferenceProfiles`) and refuses an eval whose model the region cannot serve to an evaluator. A base model ID that the region serves only through a profile is refused with the profile ID to use instead. If the lookup fails, Apply logs it and carries on.

When `model` is not set, the default is the first of Claude Sonnet 4.5, Claude Sonnet 4, Claude Haiku 4.5, Amazon Nova Pro and Amazon Nova Lite that the region can serve: the model ID itself if it runs on demand, otherwise its inference profile, preferring a geographic profile (`us.`, `eu.`, ...) over a `global.` one. In us-east-1 that is `us.anthropic.claude-sonnet-4-5-20250929-v1:0`. If the region's models cannot be listed, the default is Claude Sonnet 4.5's profile for the region's geography: `us.` for `us-*` and `ca-*`, `eu.` for `eu-*`, and `global.` elsewhere.

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
| Update | `UpdateOnlineEvaluationConfig` | On a later apply, replaces the config's evaluator list, data source and sampling rule with the current ones, so it never references an evaluator that is being removed. Polls until status is `ACTIVE`. A config that no longer exists is created again. |
| Delete | `DeleteOnlineEvaluationConfig` | Deletes the config by ID. Tolerates NotFound (already deleted). |

The CloudWatch log group is resolved from `observability.cloudwatch_log_group` if configured, otherwise defaults to `aws/spans`, the group AgentCore writes spans to when [CloudWatch Transaction Search](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Enable-TransactionSearch.html) is enabled. AWS creates `aws/spans` when Transaction Search is enabled, and it cannot be created by hand, so Apply checks it exists before creating anything and refuses the deploy if it does not: enable Transaction Search in the region, or set `observability.cloudwatch_log_group`. The sampling percentage defaults to 100% but can be overridden via the `sample_percentage` eval param.

The config reads the traces of the live endpoint, service name `<pack-id>.<live_endpoint>` (by default `<pack-id>.live`). AgentCore names a runtime's traces `<runtime>.<endpoint>` and accepts only one service name per config, so traffic sent to `DEFAULT` or to the candidate endpoint is not evaluated.

A configured `cloudwatch_log_group` that does not exist is created by Apply, tagged `promptpack:online-eval-config={pack}_online_eval`, and its name is recorded in the resource's state. Destroy deletes it after the config, but only when it carries that tag with this config's name, so a group that existed before the deploy, or that another pack created, is left alone. A group created by a release before v0.4.5 has no tag and is left in place.

### What destroy leaves behind

AgentCore itself creates log groups that this adapter does not manage, and destroy does not delete them:

- `/aws/bedrock-agentcore/runtimes/<runtime-id>-<endpoint>` for each endpoint of each agent runtime that has been invoked (`DEFAULT`, `live`, `candidate`).
- `/aws/bedrock-agentcore/evaluations/results/<online-eval-config-id>` for the online eval config.
- `aws/spans`, which belongs to Transaction Search and is shared by everything in the region.

They keep the deployment's logs and evaluation results after it is gone. Delete them with `aws logs delete-log-group` when they are no longer needed.

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
