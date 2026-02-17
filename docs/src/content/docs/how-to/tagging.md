---
title: Add Resource Tags
sidebar:
  order: 3
---

This guide explains how the adapter tags AWS resources and how to add your own custom tags for cost allocation, access control, and organizational purposes.

## Prerequisites

- A working adapter configuration with `region` and `runtime_role_arn`.
- Familiarity with [AWS resource tagging](https://docs.aws.amazon.com/general/latest/gr/aws_tagging.html) concepts.

## Goal

Apply consistent tags to every AWS resource created by the adapter, combining automatic pack metadata tags with your own custom tags.

## Default tags

The adapter automatically applies three metadata tags to every resource it creates. These are derived from the prompt pack definition:

| Tag key | Value source | Example |
|---------|-------------|---------|
| `promptpack:pack-id` | The pack's `id` field | `my-assistant` |
| `promptpack:version` | The pack's `version` field | `1.2.0` |
| `promptpack:agent` | The agent member name (multi-agent packs only) | `coordinator` |

The `promptpack:agent` tag is set per-resource. For multi-agent packs, each runtime and its associated resources receive the tag with the corresponding agent member name. For single-agent packs, this tag is omitted.

You do not need to configure anything to get these tags -- they are always applied.

## Adding user tags

Add a `tags` map to your deploy config:

```yaml
region: us-west-2
runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreExecutionRole

tags:
  environment: production
  team: ml-platform
  cost-center: CC-1234
  project: customer-support-bot
```

These tags are merged with the default tags and applied to all resources.

## Tag validation limits

The adapter enforces the following limits on user-defined tags:

| Constraint | Limit |
|-----------|-------|
| Maximum number of user tags | 50 |
| Maximum key length | 128 characters |
| Maximum value length | 256 characters |
| Empty keys | Not allowed |

If any limit is exceeded, validation fails with a descriptive error before the deployment starts. For example:

```
tags: at most 50 tags allowed, got 53
tags: key "..." exceeds max length 128
tags: value for key "..." exceeds max length 256
```

## Overriding default tags

User tags take precedence over default tags when keys collide. This means you can override the automatic pack metadata if needed:

```yaml
tags:
  promptpack:version: custom-build-42
```

In this example, the `promptpack:version` tag on all resources will be `custom-build-42` instead of the pack's actual version. Use this sparingly -- overriding default tags can make it harder to trace resources back to their source pack.

## Which resources get tagged

Tags are applied to all AWS resources created by the adapter:

| Resource type | Tag behavior |
|---------------|-------------|
| `agent_runtime` | Default tags + user tags. Multi-agent packs also set `promptpack:agent`. |
| `tool_gateway` | Default tags + user tags. |
| `memory` | Default tags + user tags. |
| `a2a_endpoint` | Default tags + user tags. |
| `evaluator` | Default tags + user tags. |
| `cedar_policy` | Default tags + user tags. |

## Example: complete tagged deployment

Given this config:

```yaml
region: us-west-2
runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreExecutionRole
memory_store: persistent

tags:
  environment: staging
  team: ml-ops
```

And a pack with `id: support-bot`, `version: 2.0.0`, and a single agent, every created resource will have these tags:

| Tag key | Tag value |
|---------|-----------|
| `promptpack:pack-id` | `support-bot` |
| `promptpack:version` | `2.0.0` |
| `environment` | `staging` |
| `team` | `ml-ops` |

## Troubleshooting

**Tags not appearing on resources** -- Verify that the `tags` field is at the top level of the deploy config, not nested inside another object. Tags must be a flat `map[string]string`.

**Validation errors about tag limits** -- Reduce the number of tags or shorten long keys/values. The 50-tag limit applies only to user-defined tags; default tags do not count toward it.

**Tag key conflicts** -- If you intentionally override a default tag, the adapter does not warn you. Double-check your tag keys if you see unexpected values on deployed resources.
