---
title: Security Model
sidebar:
  order: 2
---

The AgentCore adapter enforces security at several layers: IAM roles for AWS access, Cedar policies for runtime guardrails, authenticated communication between agents, resource tagging for auditability, and config diagnostics that catch common mistakes before deployment.

## IAM role (`runtime_role_arn`)

Every deployment requires a `runtime_role_arn` -- an IAM role ARN that AgentCore runtimes assume at execution time. This role determines what the runtime can do: invoke models, access memory, call tool gateways, and communicate with other runtimes.

```json
{
  "region": "us-west-2",
  "runtime_role_arn": "arn:aws:iam::123456789012:role/AgentCoreExecutionRole"
}
```

The adapter validates that the ARN matches the IAM role pattern (`arn:aws:iam::<12-digit-account>:role/<name>`). Beyond format validation, the diagnostics layer checks for two common mistakes:

- **User ARN instead of role ARN.** If the ARN contains `:user/` the adapter emits a warning. IAM users cannot be assumed by AgentCore runtimes -- you need an IAM role with a trust policy that allows the Bedrock service to assume it.

- **Root account ARN.** If the ARN contains `:root` the adapter warns that using the root account is a security risk. The recommended approach is a dedicated role with least-privilege permissions scoped to the Bedrock AgentCore actions your pack requires.

The same role is used for:
- `CreateAgentRuntime` and `UpdateAgentRuntime` (the `RoleArn` field)
- `CreateGateway` (the gateway execution role)
- `CreateMemory` (the `MemoryExecutionRoleArn` field)
- A2A authentication in IAM mode (injected as `PROMPTPACK_A2A_AUTH_ROLE`)

This single-role design simplifies configuration but means the role must have permissions for all resource types the pack uses. A future enhancement may support separate roles per resource type.

## Cedar policies

The adapter auto-generates [Cedar](https://www.cedarpolicy.com/) policy statements from two sources in the pack manifest: **validators** on individual prompts and **tool_policy** rules.

### How policies are created

For each prompt that has validators or a `tool_policy`, the adapter:

1. Creates a **policy engine** via `CreatePolicyEngine` and polls until it reaches `ACTIVE` status.
2. Generates a Cedar statement by combining all rules for that prompt.
3. Creates a **Cedar policy** within the engine via `CreatePolicy`.
4. Collects the policy engine ARN and injects it as `PROMPTPACK_POLICY_ENGINE_ARN` into the runtime environment, so runtimes enforce the policies at invocation time.

### Validator-to-Cedar mapping

| Validator type | Cedar rule | Example |
|----------------|-----------|---------|
| `banned_words` | One `forbid` block per word, matching `context.output like "*word*"` | Blocks responses containing specific terms |
| `max_length` | `forbid` when `context.output_length > N` | Limits response length to N characters |
| `regex_match` | `forbid` when `!context.output.matches("pattern")` | Requires output to match a regex pattern |
| `json_schema` | Comment-only placeholder | Cedar has no native JSON Schema support; enforced at runtime |

### Tool policy-to-Cedar mapping

| Tool policy field | Cedar rule |
|-------------------|-----------|
| `blocklist` | `forbid` with `action == Action::"invoke_tool"` when `resource.tool_name == "blocked_tool"` |
| `max_rounds` | `forbid` with `action == Action::"tool_loop_continue"` when `context.round_count > N` |
| `max_tool_calls_per_turn` | `forbid` with `action == Action::"invoke_tool"` when `context.tool_calls_this_turn > N` |

### Observe-only mode

Each validator in the pack manifest has an optional `failOnViolation` field. When set to `false`, the generated Cedar rule is prefixed with a `// observe-only` comment annotation. This signals the runtime to log the violation without blocking the response. When `failOnViolation` is `true` (or not set), the rule is enforced -- a matching request is denied.

This distinction lets you roll out new guardrails gradually: start in observe-only mode to measure how often the rule would fire, then switch to enforcement once you are confident in the policy.

### Policy lifecycle

Policies are created during Step 2 of the apply pipeline (after tool gateways, before runtimes). They are the **first** resources destroyed during teardown -- the adapter deletes the Cedar policy within the engine, then deletes the engine itself. This ordering prevents runtimes from referencing a deleted policy engine during the brief window between policy deletion and runtime deletion.

## A2A authentication

Multi-agent packs use Agent-to-Agent (A2A) communication where the entry agent routes requests to member agents. The adapter supports two authentication modes configured via the `a2a_auth` block.

### IAM mode (SigV4)

```json
{
  "a2a_auth": {
    "mode": "iam"
  }
}
```

In IAM mode, runtimes use AWS SigV4 request signing to authenticate with each other. The `runtime_role_arn` is injected as `PROMPTPACK_A2A_AUTH_ROLE` so each runtime knows which role to use for signing. This is the simplest mode -- no additional infrastructure is needed beyond the IAM role.

The trade-off is that all agents in the pack share the same role, so you cannot restrict which agents can call which. Any agent that can assume the role can call any other agent in the pack.

### JWT mode

```json
{
  "a2a_auth": {
    "mode": "jwt",
    "discovery_url": "https://cognito-idp.us-west-2.amazonaws.com/us-west-2_abc123/.well-known/openid-configuration",
    "allowed_audience": ["my-agent-pool"],
    "allowed_clients": ["client-id-1", "client-id-2"]
  }
}
```

In JWT mode, the adapter configures a `CustomJWTAuthorizer` on each runtime using the provided OIDC discovery URL. The runtime validates incoming JWT tokens against the discovery endpoint and checks the audience and client ID claims.

- **`discovery_url`** (required): The OIDC discovery endpoint. Typically a Cognito user pool or any compliant identity provider.
- **`allowed_audience`** (optional but recommended): Restricts which audience values are accepted in the token's `aud` claim.
- **`allowed_clients`** (optional): Restricts which client IDs are accepted.

The adapter injects `PROMPTPACK_A2A_AUTH_MODE=jwt` into the runtime environment. The authorizer configuration is set directly on the `CreateAgentRuntime` / `UpdateAgentRuntime` API call.

### Diagnostics

The adapter warns if JWT mode is used without `allowed_audience`. While the deployment will succeed, an empty audience list means any valid token from the discovery URL is accepted, which may be overly permissive.

The auth mode is also injected as `PROMPTPACK_A2A_AUTH_MODE` so the runtime code can adapt its behavior (e.g. including tokens in outbound requests to peer agents).

## Resource tagging

All AWS resources created by the adapter are tagged with pack metadata for traceability and cost allocation. Tags are built from two sources:

### Default tags

| Tag key | Value | Example |
|---------|-------|---------|
| `promptpack:pack-id` | Pack ID from the manifest | `my-chatbot` |
| `promptpack:version` | Pack version | `1.2.0` |
| `promptpack:agent` | Agent member name (multi-agent only) | `coordinator` |

### User-defined tags

The `tags` field in the deploy config accepts up to 50 key-value pairs. User tags are merged with the defaults, and user tags take precedence when keys overlap. This means you can override the default `promptpack:pack-id` tag if needed, though that is not recommended.

Tag validation enforces:
- Key length: 1 to 128 characters
- Value length: 0 to 256 characters
- Maximum count: 50 tags

Tags are applied to runtimes, gateways, and memory resources. Per-runtime tags include the `promptpack:agent` key so you can distinguish resources belonging to different members of a multi-agent pack.

## Config diagnostics

Beyond strict validation (which rejects invalid configs), the adapter runs a diagnostics pass that produces non-fatal warnings. These appear as `warning:` entries in the `ValidateConfig` response. Warnings do not prevent deployment but highlight issues likely to cause failures.

| Check | Warning | Hint |
|-------|---------|------|
| Region not in known AgentCore regions | "region X may not support Bedrock AgentCore" | Lists supported regions (us-east-1, us-west-2, eu-west-1) |
| ARN contains `:user/` | "runtime_role_arn appears to be an IAM user, not a role" | Use an IAM role ARN |
| ARN contains `:root` | "runtime_role_arn references the root account" | Create a dedicated role with least-privilege permissions |
| JWT mode without `allowed_audience` | "a2a_auth uses JWT mode but allowed_audience is empty" | Specify allowed_audience to restrict token validation |

The diagnostics are run during `ValidateConfig` and appended to the error list with a `warning:` prefix. Only hard validation errors affect the `valid` flag in the response -- warnings are informational.
