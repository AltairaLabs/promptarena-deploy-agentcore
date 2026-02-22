---
title: Configuration Reference
sidebar:
  order: 1
---

The AgentCore adapter accepts configuration from two sources:

1. **Arena config** (`deploy.agentcore` section in `arena.yaml`) — deployment settings like `region`, `runtime_binary_path`, and `model`.
2. **JSON-RPC `deploy_config`** — adapter-specific settings passed in every JSON-RPC request.

This page documents every field, its type, constraints, and validation behavior.

## Arena config fields (`deploy.agentcore`)

These fields are set in the `deploy.agentcore` section of your arena config:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `region` | string | Yes | AWS region for the AgentCore deployment (e.g. `us-west-2`). |
| `runtime_binary_path` | string | Yes | Path to the cross-compiled PromptKit runtime binary (Linux ARM64). Built with `make build-runtime-arm64`. |
| `model` | string | Yes | Bedrock model ID (e.g. `claude-3-5-haiku-20241022`, `claude-3-5-sonnet-20241022`). |

## Top-level fields (deploy_config)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `region` | string | Yes | -- | AWS region for the AgentCore deployment. Must match `^[a-z]{2}-[a-z]+-\d+$` (e.g. `us-west-2`). |
| `runtime_role_arn` | string | Yes | -- | IAM role ARN assumed by the AgentCore runtime. Must match `^arn:aws:iam::\d{12}:role/.+$`. The role needs `AmazonBedrockFullAccess` and `CloudWatchLogsReadOnlyAccess` (required when the pack includes evals). |
| `memory_store` | string | No | -- | Memory store type. Allowed values: `"session"`, `"persistent"`, or compound/object forms. See [memory_store config](/how-to/configure#memory_store). |
| `dry_run` | boolean | No | `false` | When `true`, Apply simulates resource creation without calling AWS APIs. Resources are emitted with status `"planned"`. |
| `tags` | map[string]string | No | -- | User-defined tags applied to all created AWS resources. Maximum 50 tags. Keys max 128 characters, values max 256 characters. |
| `tools` | object | No | -- | Tool-related settings. See [tools](#tools). |
| `observability` | object | No | -- | Observability settings. See [observability](#observability). |
| `a2a_auth` | object | No | -- | Agent-to-agent authentication settings. See [a2a_auth](#a2a_auth). |

## `observability`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cloudwatch_log_group` | string | No | CloudWatch log group name for runtime logs. Injected as `PROMPTPACK_LOG_GROUP`. |
| `tracing_enabled` | boolean | No | When `true`, enables X-Ray tracing. Injected as `PROMPTPACK_TRACING_ENABLED`. |

## `a2a_auth`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `mode` | string | Yes (when `a2a_auth` is present) | Authentication mode. Must be `"iam"` or `"jwt"`. |
| `discovery_url` | string | Required when mode is `"jwt"` | OIDC discovery URL for JWT validation. |
| `allowed_audience` | string[] | No | List of allowed JWT audience values. |
| `allowed_clients` | string[] | No | List of allowed JWT client IDs. |

When `mode` is `"iam"`, no additional fields are required. The adapter injects the `runtime_role_arn` as `PROMPTPACK_A2A_AUTH_ROLE`.

When `mode` is `"jwt"`, the adapter configures a `CustomJWTAuthorizer` on the AgentCore runtime with the discovery URL, audiences, and clients.

## `tools`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `code_interpreter` | boolean | No | Enables the built-in code interpreter tool on the runtime. |

## `tags`

Tags are a flat `map[string]string` with the following constraints:

| Constraint | Limit |
|------------|-------|
| Maximum number of tags | 50 |
| Maximum key length | 128 characters |
| Maximum value length | 256 characters |
| Empty keys | Not allowed |

The adapter automatically adds metadata tags (`pack_id`, `pack_version`, `agent`) to all resources. User-defined tags are merged with these defaults; user tags do not override metadata tags.

## Validation rules

The adapter validates the config in `ValidateConfig` before any Plan or Apply call. Validation checks run in order:

1. `region` must be present and match the regex `^[a-z]{2}-[a-z]+-\d+$`.
2. `runtime_role_arn` must be present and match the regex `^arn:aws:iam::\d{12}:role/.+$`.
3. If `memory_store` is set, it must be `"session"` or `"persistent"`.
4. If `a2a_auth` is present, `mode` must be `"iam"` or `"jwt"`.
5. If `a2a_auth.mode` is `"jwt"`, `discovery_url` is required.
6. Tag count must not exceed 50; individual key and value lengths are checked.

In addition to hard validation errors, the adapter runs diagnostic checks that emit non-fatal warnings (prefixed with `warning:`).

## Validation error examples

Missing required fields:

```json
{
  "valid": false,
  "errors": [
    "region is required",
    "runtime_role_arn is required"
  ]
}
```

Invalid region format:

```json
{
  "valid": false,
  "errors": [
    "region \"us_west_2\" does not match expected format (e.g. us-west-2)"
  ]
}
```

Invalid IAM role ARN:

```json
{
  "valid": false,
  "errors": [
    "runtime_role_arn \"not-an-arn\" is not a valid IAM role ARN"
  ]
}
```

Invalid memory store:

```json
{
  "valid": false,
  "errors": [
    "memory_store \"ephemeral\" must be \"session\" or \"persistent\""
  ]
}
```

Missing JWT discovery URL:

```json
{
  "valid": false,
  "errors": [
    "a2a_auth.discovery_url is required when mode is \"jwt\""
  ]
}
```

Tag limit exceeded:

```json
{
  "valid": false,
  "errors": [
    "tags: at most 50 tags allowed, got 51"
  ]
}
```

## Full JSON Schema

<details>
<summary>Expand JSON Schema (draft-07)</summary>

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["region", "runtime_role_arn"],
  "properties": {
    "region": {
      "type": "string",
      "pattern": "^[a-z]{2}-[a-z]+-\\d+$",
      "description": "AWS region for AgentCore deployment"
    },
    "runtime_role_arn": {
      "type": "string",
      "pattern": "^arn:aws:iam::\\d{12}:role/.+$",
      "description": "IAM role ARN for the AgentCore runtime"
    },
    "memory_store": {
      "type": "string",
      "enum": ["session", "persistent"],
      "description": "Memory store type for the agent"
    },
    "tools": {
      "type": "object",
      "properties": {
        "code_interpreter": { "type": "boolean" }
      }
    },
    "observability": {
      "type": "object",
      "properties": {
        "cloudwatch_log_group": { "type": "string" },
        "tracing_enabled": { "type": "boolean" }
      }
    },
    "tags": {
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "User-defined tags to apply to all created AWS resources"
    },
    "dry_run": {
      "type": "boolean",
      "description": "When true, Apply simulates resource creation without calling AWS APIs"
    },
    "a2a_auth": {
      "type": "object",
      "required": ["mode"],
      "properties": {
        "mode": {
          "type": "string",
          "enum": ["iam", "jwt"],
          "description": "A2A authentication mode"
        },
        "discovery_url": {
          "type": "string",
          "description": "OIDC discovery URL (required for jwt mode)"
        },
        "allowed_audience": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Allowed JWT audiences"
        },
        "allowed_clients": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Allowed JWT client IDs"
        }
      }
    }
  },
  "additionalProperties": false
}
```

</details>

## Example configuration

A complete configuration with all optional fields:

```json
{
  "region": "us-west-2",
  "runtime_role_arn": "arn:aws:iam::123456789012:role/AgentCoreRuntime",
  "memory_store": "session",
  "dry_run": false,
  "tags": {
    "env": "production",
    "team": "platform"
  },
  "observability": {
    "cloudwatch_log_group": "/aws/agentcore/my-pack",
    "tracing_enabled": true
  },
  "a2a_auth": {
    "mode": "jwt",
    "discovery_url": "https://cognito-idp.us-west-2.amazonaws.com/us-west-2_abc123/.well-known/openid-configuration",
    "allowed_audience": ["my-api"],
    "allowed_clients": ["client-id-1", "client-id-2"]
  },
  "tools": {
    "code_interpreter": true
  }
}
```

A minimal configuration with only required fields:

```json
{
  "region": "us-east-1",
  "runtime_role_arn": "arn:aws:iam::123456789012:role/MyAgentRole"
}
```
