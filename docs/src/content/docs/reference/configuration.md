---
title: Configuration Reference
sidebar:
  order: 1
---

The AgentCore adapter is configured from one place: the **`spec.deploy.config`** block of your arena config. PromptKit treats that block as opaque — it is serialized and handed to the adapter as the JSON-RPC `deploy_config` in every request, so the fields on this page are exactly the fields you write in YAML.

```yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: Arena
metadata:
  name: my-agent
spec:
  providers:
    - file: providers/sonnet.provider.yaml

  defaults:
    temperature: 0.1
    max_tokens: 512

  deploy:
    provider: agentcore     # selects this adapter
    config:                 # <- everything documented below goes here
      region: us-west-2
      runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreRuntime
      runtime_binary_path: /path/to/promptkit-runtime
```

:::caution[`deploy.agentcore` is not a valid key]
PromptKit's arena schema declares `deploy` with `additionalProperties: false` and only three keys — `provider`, `config` and `environments`. An adapter config written under `deploy.agentcore` fails validation:

```
spec.deploy: unknown property 'agentcore'
    Valid keys: config, environments, provider
```
:::

This page documents every field, its type, constraints, and validation behavior.

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
| `protocol` | string | No | `"both"` | Server protocol mode. Controls which servers the runtime starts. See [protocol](#protocol). |
| `providers` | object[] | No | -- | Provider bindings declaring what the runtime uses and in what role. See [providers](#providers). |
| `tool_targets` | map[string]object | No | -- | Per-tool AWS target config (`lambda_arn`, `api_gateway`, `openapi`, `smithy`, `credential`), merged into the arena tool specs. |

## `providers`

Each entry binds one provider to one capability role in the deployed runtime.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Logical binding name, unique within the config. The binding named `default` is the primary provider. |
| `role` | string | No | Capability served. One of `llm`, `embedding`, `tts`, `stt`, `image`, `inference`. Defaults to `llm`. |
| `arena_provider` | string | No | Name of a provider in the arena config to inherit `type` and `model` from. |
| `type` | string | Required unless `arena_provider` is set | Provider type, overriding anything inherited from `arena_provider`. |
| `model` | string | No | Model identifier, overriding anything inherited from `arena_provider`. |

```yaml
spec:
  providers:
    - file: providers/sonnet.provider.yaml   # declares id: sonnet

  deploy:
    provider: agentcore
    config:
      region: us-west-2
      runtime_role_arn: arn:aws:iam::123456789012:role/my-agent-role

      providers:
        - name: default              # "default" is the primary provider
          role: llm
          arena_provider: sonnet     # id of a provider in spec.providers
        - name: fast
          role: llm
          type: claude               # or declare inline
          model: claude-3-5-haiku-20241022
```

A binding resolves either by naming an arena provider (deploy what you tested) or by declaring `type`/`model` inline (keeping the deploy config self-contained). When both are given, the inline fields win field by field.

Exactly one binding is the primary — the runtime's main LLM. A binding named `default` always wins. Otherwise the first `llm`-role binding in declaration order is used and the adapter logs a warning naming it.

### Which roles actually work today

:::caution[Only `llm` is usable on AgentCore right now]
The schema accepts all six roles, but as of **PromptKit v1.5.10** only `llm` has a Bedrock-native provider:

| Role | Status on AgentCore |
|------|---------------------|
| `llm` | Works — served through Bedrock with the runtime role's credential chain. |
| `embedding` | Not available. PromptKit rejects the bedrock platform for embeddings outright ("requires a platform-native embedding provider type… not implemented"). |
| `tts`, `stt`, `image` | No Bedrock-hosted types (`polly`, `transcribe`, `titan-image` are all unregistered). They construct only for direct-API types such as `openai`, which need an API key this adapter does not inject. |
| `inference` | No Bedrock-hosted types registered. |

This matters because the runtime applies provider options **when it opens a conversation**, not at startup. A binding that cannot be constructed therefore fails *every request* rather than failing the deploy. `promptarena deploy validate` emits a warning for any non-`llm` binding so you find out before shipping.
:::

**Omitting `providers` is deprecated.** With no bindings the adapter derives a single LLM provider from the arena config and logs a deprecation warning naming the provider it chose. Arena configs routinely declare several providers — comparing them is the point of an arena — so relying on this fallback means the deploy config does not state which model actually ships.

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

## `protocol`

Controls which servers the runtime starts. Accepted values:

| Value | HTTP bridge (port 8080) | A2A server (port 9000) | Use case |
|-------|------------------------|----------------------|----------|
| `"both"` | Started | Started | Standard deployment (default). |
| `"http"` | Started | Skipped | External-facing agents not using A2A. |
| `"a2a"` | Skipped | Started | Internal agents called only via A2A. |

When omitted, defaults to `"both"`. The value is injected as `PROMPTPACK_PROTOCOL` and mapped to the AWS SDK `ProtocolConfiguration.ServerProtocol` field on the runtime.

For details on the HTTP bridge endpoints and payload formats, see [Runtime Protocols](/reference/runtime-protocols/).

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
6. If `protocol` is set, it must be `"http"`, `"a2a"`, or `"both"`.
7. Tag count must not exceed 50; individual key and value lengths are checked.

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

Invalid protocol:

```json
{
  "valid": false,
  "errors": [
    "protocol \"websocket\" must be \"http\", \"a2a\", or \"both\""
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
  "required": [
    "region",
    "runtime_role_arn"
  ],
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
      "oneOf": [
        {
          "type": "string"
        },
        {
          "type": "array",
          "items": {
            "type": "string"
          }
        },
        {
          "type": "object",
          "properties": {
            "strategies": {
              "type": "array",
              "items": {
                "type": "string"
              }
            },
            "event_expiry_days": {
              "type": "integer",
              "minimum": 3,
              "maximum": 365
            },
            "encryption_key_arn": {
              "type": "string"
            }
          },
          "required": [
            "strategies"
          ]
        }
      ],
      "description": "Memory config: string, array, or object with strategies"
    },
    "tools": {
      "type": "object",
      "properties": {
        "code_interpreter": {
          "type": "boolean"
        }
      }
    },
    "observability": {
      "type": "object",
      "properties": {
        "cloudwatch_log_group": {
          "type": "string"
        },
        "tracing_enabled": {
          "type": "boolean"
        }
      }
    },
    "tags": {
      "type": "object",
      "additionalProperties": {
        "type": "string"
      },
      "description": "User-defined tags to apply to all created AWS resources"
    },
    "dry_run": {
      "type": "boolean",
      "description": "When true, Apply simulates resource creation without calling AWS APIs"
    },
    "a2a_auth": {
      "type": "object",
      "required": [
        "mode"
      ],
      "properties": {
        "mode": {
          "type": "string",
          "enum": [
            "iam",
            "jwt"
          ],
          "description": "A2A authentication mode"
        },
        "discovery_url": {
          "type": "string",
          "description": "OIDC discovery URL (required for jwt mode)"
        },
        "allowed_audience": {
          "type": "array",
          "items": {
            "type": "string"
          },
          "description": "Allowed JWT audiences"
        },
        "allowed_clients": {
          "type": "array",
          "items": {
            "type": "string"
          },
          "description": "Allowed JWT client IDs"
        }
      }
    },
    "runtime_binary_path": {
      "type": "string",
      "description": "Path to the pre-compiled Go runtime binary for code deploy"
    },
    "protocol": {
      "type": "string",
      "enum": [
        "http",
        "a2a",
        "both"
      ],
      "description": "Server protocol mode: http (port 8080), a2a (port 9000), or both (default)"
    },
    "tool_targets": {
      "type": "object",
      "additionalProperties": {
        "type": "object"
      },
      "description": "Per-tool provider-specific target config (lambda_arn, api_gateway, openapi, smithy, credential)"
    },
    "providers": {
      "type": "array",
      "description": "What the runtime uses, in what role. Omit to derive one from the arena config (deprecated).",
      "items": {
        "type": "object",
        "required": [
          "name"
        ],
        "properties": {
          "name": {
            "type": "string",
            "description": "Unique binding name. The binding named \"default\" is the primary provider."
          },
          "role": {
            "type": "string",
            "enum": [
              "llm",
              "embedding",
              "tts",
              "stt",
              "image",
              "inference"
            ],
            "description": "Capability this provider serves (default: llm)"
          },
          "arena_provider": {
            "type": "string",
            "description": "Name of a provider in the arena config to inherit type and model from"
          },
          "type": {
            "type": "string",
            "description": "Provider type, overriding anything inherited from arena_provider"
          },
          "model": {
            "type": "string",
            "description": "Model identifier, overriding anything inherited from arena_provider"
          }
        },
        "additionalProperties": false
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
  "runtime_binary_path": "/path/to/promptkit-runtime",
  "memory_store": "session",
  "protocol": "both",
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
  },
  "providers": [
    {
      "name": "default",
      "role": "llm",
      "arena_provider": "sonnet"
    }
  ]
}
```

A minimal configuration with only required fields:

```json
{
  "region": "us-east-1",
  "runtime_role_arn": "arn:aws:iam::123456789012:role/MyAgentRole"
}
```
