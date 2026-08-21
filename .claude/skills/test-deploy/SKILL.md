---
name: test-deploy
description: End-to-end deploy test using the messaround pack
argument-hint: "[deploy|destroy|invoke|full]"
---

# End-to-End Deploy Test (messaround pack)

Run an end-to-end deployment test using the messaround pack at `~/repos/altairalabs/messaround`.

## Important Reminders

- **ALWAYS** set `PROMPTKIT_SCHEMA_SOURCE=local` when running `promptarena` commands. The published schemas are out of date and will reject valid config fields.
- The CLI is at `/Users/chaholl/go/bin/promptarena`
- Config file is `config.arena.yaml` — pass `--config config.arena.yaml`
- The adapter binary must be built with `GOWORK=off`
- The runtime binary must be cross-compiled for Linux ARM64

## Commands

Based on `$ARGUMENTS`:

- **`deploy`** — build and deploy only (steps 1-4)
- **`destroy`** — tear down the deployment (step 6)
- **`invoke`** — invoke the deployed agent (step 5)
- **`full`** or no argument — run the full cycle (build, deploy, invoke, destroy)

## Step 1: Build the Adapter Binary

```bash
cd /Users/chaholl/repos/altairalabs/promptarena-deploy-agentcore
GOWORK=off go build -o promptarena-deploy-agentcore .
```

Copy to the adapters directory so promptarena discovers it:

```bash
cp promptarena-deploy-agentcore ~/repos/altairalabs/messaround/.promptarena/adapters/
```

## Step 2: Build the Runtime Binary (Linux ARM64)

```bash
cd /Users/chaholl/repos/altairalabs/promptarena-deploy-agentcore
make build-runtime-arm64
```

This produces `./promptkit-runtime` (Linux ARM64 binary).

## Step 3: Compile the Pack

```bash
cd /Users/chaholl/repos/altairalabs/messaround
PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena pack compile --config config.arena.yaml
```

This produces `messaround.pack.json`.

## Step 4: Deploy

```bash
cd /Users/chaholl/repos/altairalabs/messaround
PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena deploy --config config.arena.yaml
```

Watch for:
- All resources showing `created` status
- Runtime reaching `READY` state (adapter polls for this)
- No errors in the output

## Step 5: Invoke the Agent

Use the AWS CLI to invoke. Get the runtime ARN from the deploy output or status command.

### Get status first
```bash
cd /Users/chaholl/repos/altairalabs/messaround
PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena deploy status --config config.arena.yaml
```

### Invoke with prompt format
```bash
aws bedrock-agent-core-control invoke-agent-runtime \
  --agent-runtime-id <RUNTIME_ID> \
  --payload '{"prompt":"What is 2+2?"}' \
  --region us-west-2
```

### Invoke with input format (also supported)
```bash
aws bedrock-agent-core-control invoke-agent-runtime \
  --agent-runtime-id <RUNTIME_ID> \
  --payload '{"input":"What is the capital of France?"}' \
  --region us-west-2
```

### Invoke with A2A JSON-RPC directly (bypasses HTTP bridge)
```bash
aws bedrock-agent-core-control invoke-agent-runtime \
  --agent-runtime-id <RUNTIME_ID> \
  --payload '{"jsonrpc":"2.0","id":"test-1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"Hello"}],"messageId":"msg-001"},"configuration":{"blocking":true}}}' \
  --region us-west-2
```

### Verify success
- Response should contain `{"response":"...","status":"success"}` (HTTP bridge format)
- Or A2A JSON-RPC response with artifacts containing text

## Step 6: Destroy

```bash
cd /Users/chaholl/repos/altairalabs/messaround
PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena deploy destroy --config config.arena.yaml
```

## Troubleshooting

### Empty response from invoke
- Check CloudWatch logs for the runtime (look for the log group in config)
- Verify the runtime reached READY status before invoking
- Check that the Bedrock model ID is correct and accessible in the region

### Schema validation errors from promptarena
- You forgot `PROMPTKIT_SCHEMA_SOURCE=local`. This is the #1 issue.

### Build failures
- Ensure `../promptkit` sibling checkout exists
- Use `GOWORK=off` for all go commands in the adapter repo

### Deploy shows "adapter not found"
- Copy the built adapter binary to `~/repos/altairalabs/messaround/.promptarena/adapters/`

### Runtime not reaching READY
- Check the runtime role ARN has correct permissions
- Check the region supports Bedrock AgentCore
- Look at CloudWatch logs for container startup errors

## Quick Reference One-Liners

```bash
# Full rebuild + redeploy
cd /Users/chaholl/repos/altairalabs/promptarena-deploy-agentcore && GOWORK=off go build -o promptarena-deploy-agentcore . && make build-runtime-arm64 && cp promptarena-deploy-agentcore ~/repos/altairalabs/messaround/.promptarena/adapters/

# Deploy
cd ~/repos/altairalabs/messaround && PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena deploy --config config.arena.yaml

# Status
cd ~/repos/altairalabs/messaround && PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena deploy status --config config.arena.yaml

# Destroy
cd ~/repos/altairalabs/messaround && PROMPTKIT_SCHEMA_SOURCE=local /Users/chaholl/go/bin/promptarena deploy destroy --config config.arena.yaml
```
