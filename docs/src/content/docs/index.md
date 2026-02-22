---
title: AWS Bedrock AgentCore Adapter
description: Deploy prompt packs to AWS Bedrock AgentCore
sidebar:
  order: 0
---

**Deploy prompt packs to AWS Bedrock AgentCore with a single command.**

---

## What is the AgentCore Adapter?

The AgentCore adapter is a deploy provider plugin for PromptKit. It translates your compiled `.pack.json` into AWS Bedrock AgentCore resources — runtimes, tool gateways, A2A wiring, Cedar policies, memory, and observability configuration.

### What It Creates

| Pack Concept | AWS Resource | Adapter Resource Type |
|---|---|---|
| Agent prompt | AgentCore Runtime | `agent_runtime` |
| Pack tool | Gateway Tool target (MCP) | `tool_gateway` |
| Validators / tool policy | Cedar Policy Engine + Policy | `cedar_policy` |
| Agent member (multi-agent) | A2A endpoint wiring | `a2a_endpoint` |
| Memory config | AgentCore Memory | `memory` |
| Eval with metric | CloudWatch metrics config | *(env var injection)* |

### Key Features

- **Multi-agent support** — One runtime per agent member, with A2A discovery wired automatically
- **Dry-run mode** — Preview what would be deployed without calling AWS APIs
- **Resource tagging** — Pack metadata and custom tags on all created resources
- **Cedar policies** — Auto-generated from validators and tool policy definitions
- **CloudWatch integration** — Metrics config and dashboard generated from eval definitions
- **Structured errors** — Classified errors with remediation hints

---

## Quick Start

```yaml
# arena.yaml
deploy:
  provider: agentcore
  agentcore:
    region: us-west-2
    runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreRuntime
    runtime_binary_path: /path/to/promptkit-runtime
    model: claude-3-5-haiku-20241022
```

```bash
# Preview the deployment plan
promptarena deploy plan

# Deploy
promptarena deploy

# Check health
promptarena deploy status

# Tear down
promptarena deploy destroy
```

---

## How It Works

```d2
direction: right

pack: .pack.json {
  shape: rectangle
  label: ".pack.json\n(compiled pack)"
}

adapter: AgentCore Adapter {
  shape: rectangle
  label: "promptarena-deploy-agentcore\n(JSON-RPC over stdio)"
}

aws: AWS Bedrock AgentCore {
  shape: rectangle
  label: "Runtimes + Gateway\n+ Memory + Policies"
}

state: Adapter State {
  shape: rectangle
  label: "Resource ARNs\n+ metadata"
}

pack -> adapter: plan / apply
adapter -> aws: AWS SDK calls
adapter -> state: returns state
```

The adapter runs as a subprocess, receiving JSON-RPC requests from the PromptKit CLI. It calls the AWS Bedrock AgentCore SDK to create and manage resources, then returns an opaque state blob containing ARNs and metadata for subsequent operations.

### Runtime Architecture

The deployed runtime serves two protocols simultaneously:

| Port | Protocol | Purpose |
|------|----------|---------|
| 8080 | HTTP | External callers — `POST /invocations` accepts `{"prompt": "..."}` payloads |
| 9000 | A2A | Agent-to-agent communication — full A2A JSON-RPC protocol |

The HTTP bridge on port 8080 translates simple invocation requests into A2A `message/send` calls on port 9000. For multi-agent deployments, agents communicate directly via A2A.

---

## Documentation

### Tutorials

- [First AgentCore Deployment](tutorials/01-first-deployment) — Deploy a single-agent pack (15 minutes)
- [Multi-Agent Deployment](tutorials/02-multi-agent) — Deploy a multi-agent pack with A2A wiring (20 minutes)

### How-To Guides

- [Configure the Adapter](how-to/configure) — All configuration options explained
- [Use Dry-Run Mode](how-to/dry-run) — Preview deployments without side effects
- [Add Resource Tags](how-to/tagging) — Tag resources with custom metadata
- [Set Up Observability](how-to/observability) — CloudWatch metrics, dashboards, and tracing

### Explanation

- [Resource Lifecycle](explanation/resource-lifecycle) — How resources are created, updated, and destroyed
- [Security Model](explanation/security) — IAM roles, Cedar policies, and A2A authentication

### Reference

- [Configuration](reference/configuration) — Complete config schema and validation rules
- [Resource Types](reference/resource-types) — All managed resource types and their behavior
- [Environment Variables](reference/environment-variables) — Variables injected into runtimes

---

## Requirements

- AWS account with Bedrock AgentCore access
- IAM role with permissions to manage AgentCore resources
- PromptKit CLI with the deploy framework
- A compiled `.pack.json` (via `packc`)

## See Also

- [Deploy Overview](/deploy/) — PromptKit's deploy framework
- [Adapter Architecture](/deploy/explanation/adapter-architecture/) — How adapters work
- [Protocol Reference](/deploy/reference/protocol/) — JSON-RPC protocol spec
