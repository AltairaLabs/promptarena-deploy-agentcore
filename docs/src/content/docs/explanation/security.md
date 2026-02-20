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
- `CreateOnlineEvalConfig` (needs CloudWatch Logs access for eval metric delivery)
- A2A authentication in IAM mode (injected as `PROMPTPACK_A2A_AUTH_ROLE`)

The role must have CloudWatch Logs permissions (`CloudWatchLogsReadOnlyAccess` or equivalent) when the pack includes evals. Online eval configs write metrics to CloudWatch, and creation will fail if the role cannot access log groups.

When the pack uses Cedar policies (tool blocklist), the role also needs `bedrock-agentcore:GetPolicyEngine` and `bedrock-agentcore:ListPolicies` permissions. The gateway calls `GetPolicyEngine` when the policy engine is associated with it, and policy creation will fail if the role cannot read the engine.

This single-role design simplifies configuration but means the role must have permissions for all resource types the pack uses. A future enhancement may support separate roles per resource type.

## Cedar policies

The adapter auto-generates [Cedar](https://www.cedarpolicy.com/) policy statements from the `tool_policy.blocklist` field in the pack manifest. Cedar policies control **which tools can be invoked** at the gateway level.

### What produces Cedar vs. what is runtime-only

| Feature | Enforcement | Notes |
|---------|-------------|-------|
| `tool_policy.blocklist` | **Cedar** (gateway-level) | Forbid blocks prevent tool invocation |
| `tool_policy.max_rounds` | Runtime (PromptKit middleware) | Not supported by AgentCore Cedar schema |
| `tool_policy.max_tool_calls_per_turn` | Runtime (PromptKit middleware) | Not supported by AgentCore Cedar schema |
| Validators (`banned_words`, `max_length`, etc.) | Runtime (PromptKit middleware) | AgentCore Cedar only supports `context.input.*` attributes, not output validation |

### How policies are created

For each prompt that has a `tool_policy.blocklist`, the adapter:

1. Creates a **policy engine** via `CreatePolicyEngine` and polls until it reaches `ACTIVE` status.
2. **Associates the policy engine with the gateway** via `UpdateGateway`, passing the engine ARN in the `PolicyEngineConfiguration` field. This is required so the engine's Cedar schema includes the gateway's registered tool actions. The gateway role must have `bedrock-agentcore:GetPolicyEngine` permission.
3. Filters the blocklist to tools that are registered on the gateway. Unregistered tools are skipped with a warning (they cannot be invoked anyway).
4. Generates one Cedar `forbid` block per blocked tool using the AgentCore action format.
5. Creates a **Cedar policy** within the engine via `CreatePolicy` (one policy per blocked tool).
6. Collects the policy engine ARN and injects it as `PROMPTPACK_POLICY_ENGINE_ARN` into the runtime environment.

### Cedar format

Blocked tools produce Cedar in the AgentCore action format:

```cedar
forbid (
  principal,
  action == AgentCore::Action::"ToolName__ToolName",
  resource == AgentCore::Gateway::"arn:aws:bedrock-agentcore:us-west-2:123456789012:gateway/my-gw"
);
```

The action name follows the convention `AgentCore::Action::"<tool>__<operation>"` from the gateway's auto-generated Cedar schema. The resource **must** be constrained to a specific gateway ARN -- AWS rejects wildcard or type-only resource constraints for action-scoped policies. This also means only tools registered on the gateway can be blocked via Cedar; the adapter filters the blocklist accordingly.

### Policy lifecycle

Policies are created during Step 2 of the apply pipeline (after tool gateways, before runtimes). During teardown, the adapter lists and deletes **all** policies within the engine via `ListPolicies`, then deletes the engine itself. This ensures clean teardown even when the number of policies changes between deploys.

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
