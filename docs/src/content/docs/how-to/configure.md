---
title: Configure the Adapter
sidebar:
  order: 1
---

This guide covers every configuration option accepted by the AgentCore deploy adapter, with YAML examples and a reference for validation errors and diagnostic warnings.

## Prerequisites

- AWS credentials configured (environment variables, shared credentials file, or instance profile).
- An IAM role ARN that the AgentCore runtime will assume at execution time.
- The target AWS region must support Bedrock AgentCore (currently `us-east-1`, `us-west-2`, `eu-west-1`).

## Required fields

### `region`

The AWS region where all resources will be created. Must match the pattern `^[a-z]{2}-[a-z]+-\d+$`.

```yaml
region: us-west-2
```

### `runtime_role_arn`

The IAM role ARN that the AgentCore runtime assumes. Must match `^arn:aws:iam::\d{12}:role/.+$`. This role needs permissions for Bedrock AgentCore operations, and must have a trust policy that allows the AgentCore service to assume it.

```yaml
runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreExecutionRole
```

## Optional fields

### `memory_store`

Controls the type of memory store created for the agent. Accepted values are `"session"` (ephemeral, per-conversation) or `"persistent"` (durable across conversations). When set, the adapter creates a memory resource and injects its ARN into the runtime via the `PROMPTPACK_MEMORY_ID` environment variable.

```yaml
memory_store: session
```

### `dry_run`

When `true`, the adapter simulates the deployment without creating an AWS client or calling any AWS APIs. All resources are emitted with status `"planned"`. See [Use Dry-Run Mode](../dry-run/) for details.

```yaml
dry_run: true
```

### `tags`

A map of user-defined tags applied to every AWS resource the adapter creates (runtimes, gateways, memory stores). Keys must be non-empty and at most 128 characters. Values must be at most 256 characters. A maximum of 50 user-defined tags are allowed.

User tags are merged with the adapter's default tags. If a user tag key collides with a default key, the user tag wins.

```yaml
tags:
  environment: production
  team: ml-platform
  cost-center: CC-1234
```

### `tools`

Tool-related settings for the AgentCore runtime.

| Field | Type | Description |
|-------|------|-------------|
| `code_interpreter` | `bool` | Enable the built-in code interpreter tool on the runtime. |

```yaml
tools:
  code_interpreter: true
```

### `observability`

Observability settings for logging and tracing. See [Set Up Observability](../observability/) for a full walkthrough.

| Field | Type | Description |
|-------|------|-------------|
| `cloudwatch_log_group` | `string` | CloudWatch Logs group name for agent runtime logs. Injected as `PROMPTPACK_LOG_GROUP`. |
| `tracing_enabled` | `bool` | Enable AWS X-Ray tracing. Injected as `PROMPTPACK_TRACING_ENABLED`. |

```yaml
observability:
  cloudwatch_log_group: /aws/agentcore/my-agent
  tracing_enabled: true
```

### `a2a_auth`

Authentication configuration for Agent-to-Agent (A2A) communication in multi-agent packs. The `mode` field is required when this object is present.

| Field | Type | Description |
|-------|------|-------------|
| `mode` | `string` | **Required.** Either `"iam"` or `"jwt"`. |
| `discovery_url` | `string` | OIDC discovery URL. Required when `mode` is `"jwt"`. |
| `allowed_audience` | `string[]` | JWT audiences to accept. Recommended for `"jwt"` mode. |
| `allowed_clients` | `string[]` | JWT client IDs to accept. |

**IAM mode** -- agents authenticate using the runtime role's AWS credentials. No extra fields are needed.

```yaml
a2a_auth:
  mode: iam
```

**JWT mode** -- agents authenticate using JWT tokens validated against an OIDC provider.

```yaml
a2a_auth:
  mode: jwt
  discovery_url: https://cognito-idp.us-west-2.amazonaws.com/us-west-2_abc123/.well-known/openid-configuration
  allowed_audience:
    - my-agent-audience
  allowed_clients:
    - client-id-1
    - client-id-2
```

## Complete example

```yaml
region: us-west-2
runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreExecutionRole
memory_store: persistent
dry_run: false

tags:
  environment: staging
  team: ml-platform

tools:
  code_interpreter: true

observability:
  cloudwatch_log_group: /aws/agentcore/my-agent
  tracing_enabled: true

a2a_auth:
  mode: jwt
  discovery_url: https://cognito-idp.us-west-2.amazonaws.com/us-west-2_abc123/.well-known/openid-configuration
  allowed_audience:
    - my-agent-audience
  allowed_clients:
    - client-id-1
```

## Validation errors

When `ValidateConfig` runs, hard errors prevent the deployment from proceeding. These are the possible validation error messages:

| Error message | Cause |
|---------------|-------|
| `region is required` | The `region` field is missing. |
| `region "xyz" does not match expected format (e.g. us-west-2)` | The value does not match the regex `^[a-z]{2}-[a-z]+-\d+$`. |
| `runtime_role_arn is required` | The `runtime_role_arn` field is missing. |
| `runtime_role_arn "..." is not a valid IAM role ARN` | The value does not match `^arn:aws:iam::\d{12}:role/.+$`. |
| `memory_store "xyz" must be "session" or "persistent"` | An unsupported memory store value was provided. |
| `a2a_auth.mode is required ("iam" or "jwt")` | The `a2a_auth` object is present but `mode` is empty. |
| `a2a_auth.mode "xyz" must be "iam" or "jwt"` | An unsupported mode value was provided. |
| `a2a_auth.discovery_url is required when mode is "jwt"` | JWT mode requires a discovery URL. |
| `tags: at most 50 tags allowed, got N` | Too many user tags. |
| `tags: key must not be empty` | A tag has an empty string as its key. |
| `tags: key "..." exceeds max length 128` | A tag key is too long. |
| `tags: value for key "..." exceeds max length 256` | A tag value is too long. |

## Diagnostic warnings

After validation passes, the adapter runs diagnostic checks that produce non-fatal warnings. These appear prefixed with `warning:` in the validation response. They do not prevent deployment but highlight likely issues.

| Warning | Category | When it fires |
|---------|----------|---------------|
| Region may not support Bedrock AgentCore | `configuration` | The region is valid but not in the known supported set (`us-east-1`, `us-west-2`, `eu-west-1`). |
| `runtime_role_arn` appears to be an IAM user | `permission` | The ARN contains `:user/` instead of `:role/`. |
| `runtime_role_arn` references the root account | `permission` | The ARN contains `:root`. |
| JWT mode without `allowed_audience` | `configuration` | A2A auth is set to JWT but `allowed_audience` is empty. |

## Troubleshooting

**"invalid config JSON"** -- The config string is not valid JSON. Check for trailing commas, unquoted keys, or encoding issues. The adapter receives config as a JSON string over JSON-RPC; YAML is converted to JSON by PromptKit before it reaches the adapter.

**Warnings about unsupported region** -- If your region is newly supported by AgentCore but the adapter has not been updated, the warning is safe to ignore. The deployment will still proceed.

**User ARN instead of role ARN** -- The adapter requires a role ARN because AgentCore assumes this role at execution time. IAM users cannot be assumed. Create a role with the necessary permissions and update `runtime_role_arn`.
