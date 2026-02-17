---
title: Use Dry-Run Mode
sidebar:
  order: 2
---

Dry-run mode lets you preview what the adapter would deploy without creating any AWS resources or even establishing an AWS connection. This is useful for validating configuration, previewing resource plans in CI, and testing changes to your pack definition.

## Prerequisites

- A valid adapter configuration (at minimum `region` and `runtime_role_arn`). These fields are still validated even in dry-run mode.
- A pack definition (the prompt pack YAML/JSON that PromptKit passes to the adapter).

## Goal

Run a deployment simulation that shows every resource the adapter would create, without making any AWS API calls.

## Steps

### 1. Set `dry_run: true` in your deploy config

Add the `dry_run` field to your adapter configuration:

```yaml
region: us-west-2
runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreExecutionRole
memory_store: session
dry_run: true

tags:
  environment: staging
```

### 2. Run the deployment

Invoke the adapter through PromptKit as you normally would. PromptKit calls the adapter's `Apply` method over JSON-RPC. When `dry_run` is `true`, the adapter:

1. Parses and validates the config (validation errors still fire normally).
2. Parses the pack definition to determine desired resources.
3. Skips AWS client creation entirely -- no credentials are needed beyond config validation.
4. Emits a progress event and resource event for each planned resource.
5. Returns a state object with all resources in `"planned"` status.

### 3. Review the output

Each resource event in the output has:

- **type** -- the resource type (`agent_runtime`, `tool_gateway`, `memory`, `a2a_endpoint`, `evaluator`, `cedar_policy`).
- **name** -- the resource name derived from the pack.
- **action** -- always `create` in dry-run (no prior state diffing).
- **status** -- always `"planned"`.
- **detail** -- a description of what would be created.

No ARNs appear in the output because no AWS resources were created.

## Example output

For a multi-agent pack with two agents, one tool, and one eval, the dry-run state looks like:

```json
{
  "resources": [
    { "type": "memory", "name": "my-pack_memory", "status": "planned" },
    { "type": "agent_runtime", "name": "coordinator", "status": "planned" },
    { "type": "agent_runtime", "name": "researcher", "status": "planned" },
    { "type": "tool_gateway", "name": "search_tool_gw", "status": "planned" },
    { "type": "a2a_endpoint", "name": "coordinator_a2a", "status": "planned" },
    { "type": "a2a_endpoint", "name": "researcher_a2a", "status": "planned" },
    { "type": "evaluator", "name": "quality_eval", "status": "planned" }
  ],
  "pack_id": "my-pack",
  "version": "1.0.0"
}
```

Progress events are also emitted during dry-run, showing messages like `"Planned agent_runtime: coordinator"` with incrementing percentage values.

## When to use dry-run

| Scenario | Why dry-run helps |
|----------|-------------------|
| **CI validation** | Verify that a pack definition produces the expected resource set without needing AWS credentials in the CI environment. |
| **Previewing changes** | See what resources a config change would add or modify before committing to a real deployment. |
| **Testing config** | Confirm that config validation passes and review diagnostic warnings without side effects. |
| **Cost estimation** | Count the resources that would be created to estimate AWS costs before deploying. |

## How dry-run differs from Plan

The adapter also exposes a `Plan` method that generates a diff against prior state (showing creates, updates, and deletes). Plan does not call AWS APIs either, but it requires valid config and does not emit progress/resource events -- it returns a structured plan response.

Dry-run, by contrast, runs through the full `Apply` code path (skipping only the AWS client) and emits the same progress and resource callback events that a real deployment would. This makes it a more complete simulation of the deploy flow.

## Troubleshooting

**Config validation still fails** -- Dry-run does not skip validation. If `region` or `runtime_role_arn` are missing or malformed, you will get the same errors as a real deployment. Fix the config and retry.

**No resources in output** -- If the pack has no agents, tools, or evals, the adapter has nothing to plan. Verify that your pack definition is correct and contains at least one agent or prompt.

**Unexpected resource count** -- Dry-run reflects the current adapter logic. If you expect a resource that does not appear, check that the relevant config option is set (for example, `memory_store` must be set for a memory resource to appear).
