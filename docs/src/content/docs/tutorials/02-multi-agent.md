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

## Step 1: Create the Project Structure

A multi-agent project uses standard PromptKit source files. Create the following directory layout:

```
content-team/
├── config.arena.yaml              # main config — ties everything together
├── prompts/
│   ├── coordinator.yaml           # kind: PromptConfig
│   ├── researcher.yaml            # kind: PromptConfig
│   └── writer.yaml                # kind: PromptConfig
└── tools/
    └── web-search.tool.yaml       # kind: Tool
```

### Define the prompts

Each agent needs a `PromptConfig` YAML file. The key under `agents.members` must match the prompt's `task_type`.

```yaml
# prompts/coordinator.yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: PromptConfig
metadata:
  name: coordinator
spec:
  task_type: coordinator
  version: "v1.0.0"
  description: "Routes tasks to the appropriate worker agent"
  system_template: |
    You are a coordinator agent. Delegate research tasks to the
    researcher agent and writing tasks to the writer agent.
    Synthesize their results into a final response.
  variables:
    - name: topic
      type: string
      required: true
      description: "The topic to research and write about"
```

```yaml
# prompts/researcher.yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: PromptConfig
metadata:
  name: researcher
spec:
  task_type: researcher
  version: "v1.0.0"
  description: "Searches knowledge bases and returns findings"
  system_template: |
    You are a research assistant. Find relevant information on the
    given topic and return structured findings.
  allowed_tools:
    - web_search
  variables:
    - name: query
      type: string
      required: true
      description: "The search query"
```

```yaml
# prompts/writer.yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: PromptConfig
metadata:
  name: writer
spec:
  task_type: writer
  version: "v1.0.0"
  description: "Produces written content from research findings"
  system_template: |
    You are a technical writer. Produce clear, well-structured
    content based on the provided research findings.
  variables:
    - name: findings
      type: string
      required: true
      description: "Research findings to write about"
```

### Define the tool

Tools are defined at the pack level and referenced by name in prompts via `allowed_tools`.

```yaml
# tools/web-search.tool.yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: Tool
metadata:
  name: web_search
spec:
  name: web_search
  description: "Search the web for information on a topic"
  input_schema:
    type: object
    properties:
      query:
        type: string
        description: "The search query"
    required:
      - query
  mode: mock
  mock_result:
    results:
      - title: "Example result"
        snippet: "This is a mock search result."
```

### Define the arena config

The `config.arena.yaml` ties everything together. The `agents` section declares the entry point and members. Each member key must match a `task_type` from the prompt configs.

```yaml
# config.arena.yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: Arena
metadata:
  name: content-team
spec:
  prompt_configs:
    - id: coordinator
      file: prompts/coordinator.yaml
    - id: researcher
      file: prompts/researcher.yaml
    - id: writer
      file: prompts/writer.yaml

  tools:
    - file: tools/web-search.tool.yaml

  agents:
    entry: coordinator
    members:
      coordinator:
        description: "Routes tasks to the appropriate worker agent"
        tags: ["router", "coordinator"]
        input_modes: ["text/plain"]
        output_modes: ["text/plain"]
      researcher:
        description: "Searches knowledge bases and returns findings"
        tags: ["research", "search"]
        input_modes: ["text/plain"]
        output_modes: ["text/plain"]
      writer:
        description: "Produces written content from research findings"
        tags: ["writing", "content"]
        input_modes: ["text/plain"]
        output_modes: ["text/plain"]
```

Key points:
- **`agents.entry`** identifies which member is the coordinator (receives external requests).
- **`agents.members`** is a map where each key matches a prompt's `task_type`. Each member gets its own AgentCore runtime.
- **`tools`** at the pack level are shared across agents via tool gateways.
- Member fields (`description`, `tags`, `input_modes`, `output_modes`) provide A2A Agent Card metadata.

### Compile the pack

```bash
packc compile -o content-team.pack.json
```

This reads the arena config, compiles all prompts, tools, and the agents section into a single `.pack.json` file.

## Step 2: Configure the Deployment

Add a `deploy` section to your `config.arena.yaml`. For multi-agent packs, you should also configure **A2A authentication** so agents can securely invoke each other:

```yaml
# Add this under spec: in config.arena.yaml
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
