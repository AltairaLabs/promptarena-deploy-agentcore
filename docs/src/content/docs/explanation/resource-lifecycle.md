---
title: Resource Lifecycle
sidebar:
  order: 1
---

The AgentCore adapter manages six resource types across a multi-phase apply pipeline. This page explains the ordering, the reasons behind it, and the behaviours you should expect during deployment, updates, and teardown.

## Resource types

| Type | AWS construct | Notes |
|------|---------------|-------|
| `memory` | Bedrock AgentCore Memory | Session (episodic) or persistent (semantic) store |
| `tool_gateway` | Gateway + Gateway Targets | One parent gateway, one target per pack tool |
| `cedar_policy` | Policy Engine + Cedar Policy | One engine and one policy per prompt with validators or tool_policy |
| `agent_runtime` | AgentCore Runtime | One runtime per agent member (multi-agent) or one per pack (single-agent) |
| `a2a_endpoint` | Logical resource | No AWS API call -- discovery is via env var injection |
| `evaluator` | Placeholder | SDK not yet available; returns a synthetic ARN |

## Apply order

Apply creates resources in strict dependency order. Each phase must complete before the next begins because later resources consume ARNs or IDs produced by earlier ones.

```
Pre-step   Memory
Step 1     Tool Gateways
Step 2     Cedar Policies
Step 3     Agent Runtimes
Post-step  A2A Discovery (env var injection on entry agent)
Step 4     A2A Wiring
Step 5     Evaluators
```

### Why this order matters

1. **Memory before everything.** When `memory_store` is configured the adapter creates the memory resource first and injects the resulting ARN into the runtime environment variables (`PROMPTPACK_MEMORY_ID`). Every runtime created in Step 3 will therefore receive the memory ARN at creation time rather than requiring a second update pass.

2. **Tool gateways before runtimes.** Each gateway target produces a gateway ARN. The adapter caches the parent gateway ID so subsequent tool targets reuse it. Runtimes later reference the gateway ARN through env vars or SDK configuration.

3. **Cedar policies before runtimes.** Policy engines and their Cedar policies are created in Step 2. Once all policies are ready, the adapter collects the policy engine ARNs and injects them as the `PROMPTPACK_POLICY_ENGINE_ARN` environment variable. Runtimes created in Step 3 see the policy ARNs immediately, so guardrails are active from first invocation.

4. **Runtimes before A2A.** In a multi-agent pack each member gets its own runtime. After all runtimes are created, the adapter builds a JSON map of `{memberName: runtimeARN}` and injects it as `PROMPTPACK_AGENTS` on the entry agent by calling `UpdateRuntime`. This is the A2A discovery mechanism -- there is no separate discovery service. The entry agent reads the env var at startup to learn the ARNs of its peers.

5. **A2A wiring after runtimes.** The A2A wiring resources are logical -- no separate AWS API call is made. They exist in state so that `Destroy` and `Status` can track the relationship. They are only created for multi-agent packs.

6. **Evaluators last.** Evaluators have no dependencies on other resources and no other resource depends on them, so they run at the end. The evaluator API is not yet available in the SDK; the adapter returns a placeholder ARN (e.g. `arn:aws:bedrock:us-west-2:evaluator/eval-accuracy`).

### Progress tracking

The five numbered steps divide the progress bar into equal 20% segments. Within each segment, progress advances proportionally to the number of resources in that phase. The memory pre-step and A2A discovery post-step report progress at fixed positions (0% and 50% respectively).

## Destroy order

Destroy reverses the apply order. Resources are grouped by type and deleted in this sequence:

```
1. cedar_policy    (policy + engine per prompt)
2. evaluator       (placeholder -- skip in practice)
3. a2a_endpoint    (logical -- skip in practice)
4. agent_runtime   (delete via DeleteAgentRuntime)
5. tool_gateway    (delete via DeleteGateway)
6. memory          (delete via DeleteMemory)
```

The adapter also handles resources whose type does not appear in the standard ordering. These are cleaned up in a final pass after the ordered groups.

Destroy continues on individual resource failures. A failed deletion is reported as an error event but does not abort the remaining teardown. This is a deliberate choice: in a partially failed deployment, you want to clean up as much as possible rather than leaving orphaned resources.

## Update support

Only `agent_runtime` supports in-place updates. When the adapter detects a prior state entry for a runtime (same type and name), it calls `UpdateAgentRuntime` instead of `CreateAgentRuntime`. The update carries the same payload (role ARN, env vars, authorizer config) and polls until the runtime returns to READY status.

All other resource types are create-only. If you change a tool gateway, policy, or evaluator configuration, you must destroy and redeploy. This is a limitation of the current AWS API surface -- most AgentCore control-plane resources do not expose update operations.

The adapter resolves create-vs-update per resource by looking up the resource key (`type + name`) in the prior state map. The prior state is the opaque JSON string returned by the previous `Apply` call and passed back through `PlanRequest.PriorState`.

## Placeholder and logical resources

Two resource types do not make real AWS API calls:

- **Evaluators** return a synthetic ARN formatted as `arn:aws:bedrock:<region>:evaluator/<name>`. The adapter logs a message noting that evaluator creation is not yet supported by the SDK. When AWS ships the evaluator API, the adapter will replace the placeholder with a real `CreateEvaluator` call.

- **A2A endpoints** return a synthetic ARN formatted as `arn:aws:bedrock:<region>:a2a-endpoint/<name>`. The A2A wiring is expressed entirely through the `PROMPTPACK_AGENTS` env var injected on the entry agent runtime. No separate AWS resource is created for peer discovery.

Both resource types appear in state so that `Status` can report on them (they always return `healthy`) and `Destroy` can track them (they are silently skipped during deletion).

## Polling behaviour

Three resource types require polling after creation or update:

| Resource | Status field | Ready state | Terminal failure states |
|----------|-------------|-------------|------------------------|
| Agent Runtime | `AgentRuntimeStatus` | `READY` | `CREATE_FAILED`, `UPDATE_FAILED` |
| Gateway | `GatewayStatus` | `READY` | `FAILED` |
| Policy Engine | `PolicyEngineStatus` | `ACTIVE` | Any state other than `CREATING` or `ACTIVE` |

Polling uses a fixed interval of **5 seconds** with a maximum of **60 attempts**, giving a timeout window of approximately **5 minutes**. If the resource enters a terminal failure state, polling stops immediately and returns the failure reason (when available from the API response). If the resource is still in a transitional state (`CREATING`, `UPDATING`, `DELETING`) after 60 attempts, the adapter returns a timeout error.

The adapter returns the resource ARN even when polling fails. This means the state will contain the ARN with a `failed` status, which is useful for debugging -- you can look up the resource in the AWS console using the ARN.

## Error handling

Apply does **not** abort on the first error. Each phase processes all its resources, collecting failures as it goes. Individual resource failures are:

1. Wrapped in a `DeployError` struct that includes:
   - **Category**: `permission`, `network`, `timeout`, `configuration`, or `resource`
   - **Operation**: `create`, `update`, or `delete`
   - **Resource type and name**
   - **Remediation hint**: a human-readable suggestion (e.g. "verify the runtime_role_arn has required permissions")
   - **Cause**: the underlying AWS SDK error

2. Reported via the progress callback as an error event, so the caller can display or log it in real time.

3. Recorded in state with `status: "failed"` (no ARN).

4. Accumulated into a combined error that is returned alongside the state JSON. The caller receives both the partial state (with successful resources) and the combined error.

Error classification is automatic. The adapter inspects the AWS error message for keywords like "access denied" (permission), "connection refused" (network), "did not become ready" (timeout), and "validation" (configuration). Unrecognised errors default to the `resource` category.

The only errors that abort the entire apply are callback errors -- if the progress callback itself returns an error (e.g. the caller disconnected), the phase stops immediately and returns.

## State format

The adapter returns and accepts state as a JSON string with this shape:

```json
{
  "resources": [
    {
      "type": "agent_runtime",
      "name": "coordinator",
      "arn": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/abc123",
      "status": "created",
      "metadata": {}
    },
    {
      "type": "cedar_policy",
      "name": "main_prompt",
      "arn": "arn:aws:bedrock:us-west-2:123456789012:policy/xyz789",
      "status": "created",
      "metadata": {
        "policy_engine_id": "engine-001",
        "policy_engine_arn": "arn:aws:bedrock:us-west-2:123456789012:policy-engine/engine-001",
        "policy_id": "policy-001"
      }
    }
  ],
  "pack_id": "my-chatbot",
  "version": "1.2.0"
}
```

Key points:

- **`resources`** is an ordered list matching the creation sequence. Each entry records the type, name, ARN (if creation succeeded), status (`created`, `updated`, `failed`, or `planned` for dry-run), and optional metadata.
- **`pack_id`** and **`version`** are copied from the pack manifest for traceability.
- **`metadata`** is type-specific. Cedar policies store their engine ID, engine ARN, and policy ID so that `Destroy` can delete both the policy and its engine.
- The state is opaque to PromptKit -- only this adapter reads and writes it. It is passed verbatim between `Apply`, `Plan`, `Destroy`, and `Status` calls via `PriorState`.

### Dry-run mode

When `dry_run: true` is set in the deploy config, Apply skips AWS client creation entirely and emits resource events with `status: "planned"`. The returned state contains the same structure but with no ARNs, allowing the caller to preview the deployment plan without side effects.
