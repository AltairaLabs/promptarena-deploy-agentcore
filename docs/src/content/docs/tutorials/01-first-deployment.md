---
title: "01: First AgentCore Deployment"
sidebar:
  order: 1
---

Deploy a single-agent prompt pack to AWS Bedrock AgentCore, verify it is healthy, and tear it down cleanly.

**Time:** ~15 minutes

## What You'll Build

A single AgentCore runtime running your compiled prompt pack in AWS. By the end, you will have deployed, inspected, and destroyed your first AgentCore resource.

## Learning Objectives

- Configure the AgentCore deploy adapter in `arena.yaml`
- Validate your configuration before deploying
- Preview a deployment plan and understand the resource changes
- Apply the plan to create AWS resources
- Check deployment health with the status command
- Destroy all resources cleanly

## Prerequisites

Before starting, make sure you have the following ready:

1. **AWS account** with Bedrock AgentCore access enabled in your target region.
2. **IAM role** for the AgentCore runtime. The role must have permissions to invoke Bedrock models. Note the full ARN (e.g., `arn:aws:iam::123456789012:role/AgentCoreRuntime`).
3. **PromptKit CLI** installed and on your PATH. Verify with:
   ```bash
   promptarena --version
   ```
4. **Compiled pack** -- a `.pack.json` file produced by `packc compile`. For this tutorial, any single-agent pack will work. If you do not have one, compile the quickstart example:
   ```bash
   packc compile -o my-agent.pack.json
   ```

---

## Step 1: Create the Deploy Configuration

Open your `config.arena.yaml` file and add a `deploy` section under `spec:`. If you do not have an arena config yet, create one:

```yaml
# config.arena.yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: Arena
metadata:
  name: my-agent
spec:
  prompt_configs:
    - id: main
      file: prompts/main.yaml

  deploy:
    provider: agentcore
    config:
      region: us-west-2
      runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreRuntime
```

Replace the values:
- **`region`** -- the AWS region where AgentCore is available (e.g., `us-west-2`, `us-east-1`).
- **`runtime_role_arn`** -- the full ARN of the IAM role your runtime will assume.

These two fields are the only required configuration. The adapter supports additional options (memory, observability, tags) covered in the [Configuration Reference](/reference/configuration/).

## Step 2: Validate the Configuration

Before deploying, confirm that the configuration is syntactically correct and the field values pass validation:

```bash
promptarena deploy validate
```

Expected output on success:

```
Validating agentcore configuration...
  region:          us-west-2         OK
  runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreRuntime  OK

Configuration is valid.
```

If validation fails, the adapter returns specific error messages. For example:

```
Configuration is invalid:
  - region "us-west2" does not match expected format (e.g. us-west-2)
```

Fix any reported issues before continuing.

## Step 3: Preview the Deployment Plan

Run the plan command to see what resources the adapter will create -- without making any changes to AWS:

```bash
promptarena deploy plan
```

For a single-agent pack named `my-agent`, you will see output like this:

```
Planning agentcore deployment...

  agent_runtime  my-agent  CREATE  Create AgentCore runtime for my-agent

Plan: 1 to create, 0 to update, 0 to delete
```

The plan tells you:
- **Resource type**: `agent_runtime` -- a Bedrock AgentCore runtime container.
- **Name**: derived from your pack ID.
- **Action**: `CREATE` because no prior deployment state exists.

If your pack includes tools, validators, or memory configuration, additional resources will appear:

```
  memory          my-agent_memory       CREATE  Create memory store (session) for my-agent
  cedar_policy    main                  CREATE  Create Cedar policy for prompt main
  agent_runtime   my-agent              CREATE  Create AgentCore runtime for my-agent

Plan: 3 to create, 0 to update, 0 to delete
```

Review the plan and proceed when it looks correct.

## Step 4: Deploy

Apply the plan to create the resources in AWS:

```bash
promptarena deploy
```

The adapter streams progress as each resource is created:

```
Deploying to agentcore...

  [  0%] Creating agent_runtime: my-agent
  [ 45%] Creating agent_runtime: my-agent (polling for READY status)
  [ 50%] Created agent_runtime: my-agent
         ARN: arn:aws:bedrock:us-west-2:123456789012:agent-runtime/abcd1234

Deploy complete. 1 resource created.
```

The runtime creation includes a polling phase -- the adapter waits until the runtime transitions from `CREATING` to `READY` before reporting success. This typically takes 30-90 seconds.

## Step 5: Verify Deployment Health

Confirm that the deployed resources are healthy:

```bash
promptarena deploy status
```

Expected output:

```
AgentCore deployment status: deployed

  agent_runtime  my-agent  healthy

1 resource, all healthy.
```

Each resource is checked against the AWS API. Possible statuses are:
- **healthy** -- the resource exists and is functioning normally.
- **unhealthy** -- the resource exists but is in a degraded state.
- **missing** -- the resource was expected but could not be found in AWS.

If any resource is unhealthy or missing, the aggregate status changes to `degraded`.

## Step 6: Destroy the Deployment

When you are done, tear down all resources:

```bash
promptarena deploy destroy
```

The adapter deletes resources in reverse dependency order:

```
Destroying agentcore deployment...

  Destroying 1 resources
  Step 1: deleting agent_runtime resources (1)
  Deleted agent_runtime "my-agent"

Destroy complete.
```

Run `promptarena deploy status` again to confirm nothing remains:

```
AgentCore deployment status: not_deployed
```

---

## What You Learned

- The `deploy.config` section in `arena.yaml` requires only `region` and `runtime_role_arn` for a basic deployment.
- `promptarena deploy validate` catches configuration errors before you spend time on a real deployment.
- `promptarena deploy plan` previews the exact resources that will be created, updated, or deleted.
- `promptarena deploy` creates the resources and polls until they are ready.
- `promptarena deploy status` checks the health of every deployed resource.
- `promptarena deploy destroy` tears down resources in the correct dependency order.

---

## Common Issues

### Invalid Region Format

```
region "uswest2" does not match expected format (e.g. us-west-2)
```

The region must be a valid AWS region identifier in the format `xx-xxxx-N` (e.g., `us-west-2`, `eu-central-1`). Check the [AWS region list](https://docs.aws.amazon.com/general/latest/gr/bedrock.html) for regions that support Bedrock AgentCore.

### Missing IAM Permissions

```
agentcore: create agent_runtime "my-agent": AccessDeniedException:
  User is not authorized to perform bedrock:CreateAgentRuntime
```

The AWS credentials used by the CLI (not the runtime role) need permission to manage AgentCore resources. Ensure your IAM user or role has the `bedrock:*AgentRuntime*` permissions, or use the AWS-managed `AmazonBedrockAgentCoreDeveloperAccess` policy.

The **runtime role** (`runtime_role_arn`) is a separate concern -- it is the role the runtime assumes when it runs. It needs `bedrock:InvokeModel` and related permissions.

### Runtime Stuck in CREATING

```
agentcore: create agent_runtime "my-agent": timed out waiting for READY status
```

The adapter polls for up to 5 minutes waiting for the runtime to become `READY`. If it times out:

1. Check the AWS Console under Bedrock > AgentCore to see the runtime's actual status.
2. Look for error details in the runtime's event log.
3. Common causes: the runtime role ARN is invalid, the role lacks a trust policy for `bedrock.amazonaws.com`, or the region does not have AgentCore capacity.
4. After fixing the issue, run `promptarena deploy` again -- the adapter will detect the existing resource and attempt an update rather than creating a duplicate.

### Role ARN Validation Error

```
runtime_role_arn "arn:aws:iam:123456789012:role/MyRole" is not a valid IAM role ARN
```

The ARN must follow the exact format `arn:aws:iam::<12-digit-account-id>:role/<role-name>`. Note the double colon (`::`) between `iam` and the account ID. A common mistake is using a single colon.

---

## Next Steps

- [Tutorial 02: Multi-Agent Deployment](02-multi-agent) -- Deploy a coordinator with worker agents and A2A discovery.
- [How-To: Configure the Adapter](/how-to/configure/) -- Explore all configuration options including memory, observability, and tags.
