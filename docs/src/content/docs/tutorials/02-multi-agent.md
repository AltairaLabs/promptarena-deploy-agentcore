---
title: "02: Multi-Agent Deployment"
sidebar:
  order: 2
---

Deploy a multi-agent prompt pack -- a coordinator agent with two worker agents -- to AWS Bedrock AgentCore, with A2A discovery and tool gateways.

**Time:** ~20 minutes

## What You'll Build

A fully wired multi-agent system consisting of:

- **coordinator** -- the entry agent that routes tasks to workers
- **researcher** -- a worker agent that searches for information
- **writer** -- a worker agent that produces written content

Each agent gets its own AgentCore runtime. The adapter automatically wires A2A (agent-to-agent) discovery so the coordinator can invoke the workers.

## Learning Objectives

- Structure a multi-agent pack with `agents.entry` and `agents.members`
- Configure A2A authentication in the deploy config
- Understand the 6-phase deployment process
- Verify that A2A discovery injection works correctly
- Inspect multi-resource status and destroy in dependency order

## Prerequisites

Everything from [Tutorial 01](01-first-deployment), plus:

1. **A multi-agent pack** -- a `.pack.json` that defines an `agents` section. If you do not have one, the example below shows the structure.
2. **Familiarity with Tutorial 01** -- this tutorial assumes you know how to validate, plan, and deploy.

---

## Step 1: Understand the Pack Structure

A multi-agent pack has an `agents` section that declares the entry point and the member agents. Here is the relevant portion of a `pack.yaml` before compilation:

```yaml
# pack.yaml
id: content-team
version: "1.0.0"

agents:
  entry: coordinator
  members:
    - name: coordinator
      prompt: coordinator_prompt
      description: Routes tasks to the appropriate worker agent
    - name: researcher
      prompt: researcher_prompt
      description: Searches knowledge bases and returns findings
    - name: writer
      prompt: writer_prompt
      description: Produces written content from research findings

prompts:
  coordinator_prompt:
    model: anthropic.claude-sonnet-4-20250514
    system: "You are a coordinator. Delegate research to the researcher and writing to the writer."
  researcher_prompt:
    model: anthropic.claude-sonnet-4-20250514
    system: "You are a research assistant. Find relevant information."
  writer_prompt:
    model: anthropic.claude-sonnet-4-20250514
    system: "You are a technical writer. Produce clear, well-structured content."

tools:
  web_search:
    type: mcp
    uri: "https://search-api.example.com/mcp"
```

Key points:
- **`agents.entry`** identifies which member is the coordinator (receives external requests).
- **`agents.members`** lists all agents. Each gets its own AgentCore runtime.
- **`tools`** at the pack level are shared across agents via tool gateways.

Compile the pack:

```bash
packc build -o content-team.pack.json
```

## Step 2: Configure the Deployment

Add the deploy configuration to `arena.yaml`. For multi-agent packs, you should also configure **A2A authentication** so agents can securely invoke each other:

```yaml
# arena.yaml
deploy:
  provider: agentcore
  config:
    region: us-west-2
    runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreRuntime
    a2a_auth:
      mode: iam
    tags:
      team: content-platform
      environment: staging
```

The `a2a_auth` section is optional but recommended for multi-agent deployments:

- **`mode: iam`** -- agents authenticate using IAM role credentials. This is the simplest option and works when all agents run in the same AWS account.
- **`mode: jwt`** -- agents authenticate using JWT tokens. Required for cross-account scenarios. Needs additional fields (`discovery_url`, `allowed_audience`, `allowed_clients`).

The `tags` section adds custom tags to all created AWS resources, making them easier to find and manage.

## Step 3: Validate

```bash
promptarena deploy validate
```

Expected output:

```
Validating agentcore configuration...
  region:           us-west-2         OK
  runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreRuntime  OK
  a2a_auth.mode:    iam               OK
  tags:             2 tags            OK

Configuration is valid.
```

## Step 4: Plan the Deployment

```bash
promptarena deploy plan
```

For the `content-team` pack with three agents and one tool, the plan shows:

```
Planning agentcore deployment...

  tool_gateway    web_search_tool_gw    CREATE  Create tool gateway for web_search
  agent_runtime   coordinator           CREATE  Create AgentCore runtime for coordinator
  agent_runtime   researcher            CREATE  Create AgentCore runtime for researcher
  agent_runtime   writer                CREATE  Create AgentCore runtime for writer
  a2a_endpoint    coordinator_a2a       CREATE  Create A2A endpoint for coordinator
  a2a_endpoint    researcher_a2a        CREATE  Create A2A endpoint for researcher
  a2a_endpoint    writer_a2a            CREATE  Create A2A endpoint for writer

Plan: 7 to create, 0 to update, 0 to delete
```

Notice the three categories of resources:
- **`tool_gateway`** -- one per pack-level tool, created first because runtimes depend on them.
- **`agent_runtime`** -- one per agent member.
- **`a2a_endpoint`** -- one per agent, wiring inter-agent communication.

If your pack includes validators, tool policies, memory, or evaluators, additional resources will appear:

```
  memory          content-team_memory   CREATE  Create memory store (session) for content-team
  cedar_policy    coordinator_prompt    CREATE  Create Cedar policy for prompt coordinator_prompt
  tool_gateway    web_search_tool_gw    CREATE  Create tool gateway for web_search
  agent_runtime   coordinator           CREATE  Create AgentCore runtime for coordinator
  agent_runtime   researcher            CREATE  Create AgentCore runtime for researcher
  agent_runtime   writer                CREATE  Create AgentCore runtime for writer
  a2a_endpoint    coordinator_a2a       CREATE  Create A2A endpoint for coordinator
  a2a_endpoint    researcher_a2a        CREATE  Create A2A endpoint for researcher
  a2a_endpoint    writer_a2a            CREATE  Create A2A endpoint for writer
  evaluator       quality_eval          CREATE  Create evaluator for quality

Plan: 10 to create, 0 to update, 0 to delete
```

## Step 5: Apply the Deployment

```bash
promptarena deploy
```

The adapter creates resources in 6 dependency-ordered phases. Watch the progress output:

```
Deploying to agentcore...

  [  0%] Creating tool_gateway: web_search_tool_gw
  [  5%] Created tool_gateway: web_search_tool_gw
         ARN: arn:aws:bedrock:us-west-2:123456789012:gateway-tool/gw-abcd1234

  [ 20%] Creating cedar_policy: coordinator_prompt
  [ 25%] Created cedar_policy: coordinator_prompt
         ARN: arn:aws:bedrock:us-west-2:123456789012:policy/pol-efgh5678

  [ 40%] Creating agent_runtime: coordinator
  [ 42%] Creating agent_runtime: researcher
  [ 44%] Creating agent_runtime: writer
  [ 46%] Creating agent_runtime: coordinator (polling for READY status)
  [ 48%] Creating agent_runtime: researcher (polling for READY status)
  [ 50%] Created agent_runtime: coordinator
         ARN: arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-coord001
  [ 52%] Created agent_runtime: researcher
         ARN: arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-rsch002
  [ 54%] Created agent_runtime: writer
         ARN: arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-wrt003

  [ 50%] Injecting A2A endpoint map on entry agent: coordinator

  [ 60%] Creating a2a_endpoint: coordinator_a2a
  [ 65%] Creating a2a_endpoint: researcher_a2a
  [ 70%] Creating a2a_endpoint: writer_a2a
  [ 75%] Created a2a_endpoint: coordinator_a2a
  [ 77%] Created a2a_endpoint: researcher_a2a
  [ 80%] Created a2a_endpoint: writer_a2a

Deploy complete. 7 resources created.
```

The six phases execute in this order because each phase depends on the outputs of the previous one.

## Step 6: Verify A2A Discovery

After runtimes are created but before A2A wiring, the adapter performs a critical step: **A2A endpoint discovery injection**. It updates the entry agent's runtime with a `PROMPTPACK_AGENTS` environment variable containing a JSON map of agent names to runtime ARNs:

```json
{
  "coordinator": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-coord001",
  "researcher": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-rsch002",
  "writer": "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-wrt003"
}
```

This environment variable is how the coordinator knows where to route requests to worker agents. The PromptKit runtime SDK reads `PROMPTPACK_AGENTS` at startup and configures the A2A client accordingly.

Additional environment variables injected when A2A auth is configured:
- `PROMPTPACK_A2A_AUTH_MODE` -- set to `iam` or `jwt`
- `PROMPTPACK_A2A_AUTH_ROLE` -- the runtime role ARN (for IAM-mode signing)

You can verify these by inspecting the runtime configuration in the AWS Console under Bedrock > AgentCore > Runtimes > coordinator > Environment Variables.

## Step 7: Check Status

```bash
promptarena deploy status
```

Expected output for a healthy multi-agent deployment:

```
AgentCore deployment status: deployed

  tool_gateway    web_search_tool_gw  healthy
  agent_runtime   coordinator         healthy
  agent_runtime   researcher          healthy
  agent_runtime   writer              healthy
  a2a_endpoint    coordinator_a2a     healthy
  a2a_endpoint    researcher_a2a      healthy
  a2a_endpoint    writer_a2a          healthy

7 resources, all healthy.
```

If any resource shows `unhealthy` or `missing`, the aggregate status changes to `degraded`. Check the specific resource in the AWS Console for details.

## Step 8: Destroy

```bash
promptarena deploy destroy
```

Resources are destroyed in **reverse dependency order** to avoid orphaned references:

```
Destroying agentcore deployment...

  Destroying 7 resources
  Step 1: deleting evaluator resources (0)
  Step 2: deleting a2a_endpoint resources (3)
  Deleted a2a_endpoint "coordinator_a2a"
  Deleted a2a_endpoint "researcher_a2a"
  Deleted a2a_endpoint "writer_a2a"
  Step 3: deleting agent_runtime resources (3)
  Deleted agent_runtime "coordinator"
  Deleted agent_runtime "researcher"
  Deleted agent_runtime "writer"
  Step 4: deleting tool_gateway resources (1)
  Deleted tool_gateway "web_search_tool_gw"

Destroy complete.
```

---

## Understanding the Deploy Phases

The adapter creates resources in 6 dependency-ordered phases. Each phase must complete before the next begins because later resources depend on earlier ones.

### Phase 1: Tools (0-20%)

**Creates:** `tool_gateway` resources for each pack-level tool.

Tool gateways expose MCP tool endpoints to the AgentCore runtimes. They are created first because the runtime configuration references gateway ARNs. Without the gateways, runtimes would not know where to send tool invocations.

### Phase 2: Policies (20-40%)

**Creates:** `cedar_policy` resources for prompts that define `validators` or `tool_policy`.

Each policy consists of two AWS resources: a Cedar policy engine and a Cedar policy attached to it. The policy engine ARN is injected into the runtime configuration via `PROMPTPACK_POLICY_ENGINE_ARN`, so this phase must complete before runtimes are created.

### Phase 3: Runtimes (40-60%)

**Creates:** `agent_runtime` resources, one per agent member.

Each runtime is an AgentCore container running your agent's prompt and model configuration. The adapter polls each runtime until it reaches `READY` status before proceeding. Runtime creation includes:
- Model configuration from the prompt
- Environment variables (memory, observability, policy engine ARNs)
- Tool gateway references
- Resource tags

### Phase 4: A2A Discovery (at 50%)

**Updates:** the entry agent's runtime with `PROMPTPACK_AGENTS`.

This is not a separate resource creation -- it is an update to the entry agent runtime. After all runtimes are created and their ARNs are known, the adapter builds a JSON map and injects it as an environment variable. This is what allows the coordinator to discover and invoke worker agents.

### Phase 5: A2A Wiring (60-80%)

**Creates:** `a2a_endpoint` resources for each agent member.

A2A wiring resources represent the logical connections between agents. These depend on the runtimes already existing. In the current implementation, A2A wiring creates endpoint metadata that the PromptKit runtime SDK uses alongside `PROMPTPACK_AGENTS` for routing.

### Phase 6: Evaluators (80-100%)

**Creates:** `evaluator` resources for each eval defined in the pack.

Evaluators are created last because they may reference agent runtimes. If your pack defines evals with metrics, the adapter also injects `PROMPTPACK_METRICS_CONFIG` and `PROMPTPACK_DASHBOARD_CONFIG` environment variables into the runtimes.

---

## What You Learned

- Multi-agent packs use `agents.entry` and `agents.members` to define the agent topology.
- Each agent member gets its own `agent_runtime` in AgentCore.
- Pack-level tools are deployed as shared `tool_gateway` resources.
- A2A authentication (`a2a_auth.mode`) secures inter-agent communication.
- The adapter handles endpoint discovery automatically by injecting `PROMPTPACK_AGENTS` into the entry agent.
- Resources are created in 6 phases (tools, policies, runtimes, A2A discovery, A2A wiring, evaluators) and destroyed in reverse order.
- The plan command shows all resources across all phases before any changes are made.

---

## Next Steps

- [How-To: Configure the Adapter](/how-to/configure/) -- All configuration options including JWT-mode A2A auth, memory stores, and observability.
- [How-To: Use Dry-Run Mode](/how-to/dry-run/) -- Preview a full multi-agent deployment without calling AWS APIs.
- [Explanation: Resource Lifecycle](/explanation/resource-lifecycle/) -- Deep dive into how resources are created, updated, and destroyed.
- [Reference: Resource Types](/reference/resource-types/) -- Complete reference for all six resource types.
