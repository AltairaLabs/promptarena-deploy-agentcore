# Deployed integration tests

These deploy a real agent to a real AWS account and invoke it. **They create
billable Bedrock AgentCore resources.** They are behind the `integration` build
tag and skip unless the three variables below are set, so a plain
`go test ./...` never touches AWS.

## Running them

```bash
# Build the runtime the adapter will upload. It runs inside AgentCore, whose
# architecture is linux/arm64 — not your machine's. An amd64 binary uploads and
# deploys without complaint and then cannot exec.
make build-runtime-arm64

export AGENTCORE_TEST_REGION=us-west-2
export AGENTCORE_TEST_ROLE_ARN=arn:aws:iam::<account>:role/AgentCoreRuntime
export AGENTCORE_TEST_BINARY_PATH="$PWD/promptkit-runtime"

make test-integration
```

Optional:

- `AGENTCORE_TEST_MODEL` — the model the agent talks to. Defaults to
  `claude-haiku-4-5-20251001`.
- `AGENTCORE_TEST_EVAL_MODEL` — the full Bedrock id for judge evals. Defaults to
  `anthropic.claude-haiku-4-5-20251001-v1:0`.

Both must exist **in your region**. The adapter validates model availability up
front and fails the whole apply when one is missing, so a model that is not
enabled fails before anything deploys. `aws bedrock list-foundation-models
--region <region>` lists what you actually have.

## What they cover

| Test | What a failure means |
|------|----------------------|
| `ApplyCreatesRuntime` | A deploy did not leave an agent runtime with an ARN. |
| `StatusReportsDeployed` | The adapter's view of a live deploy disagrees with AWS's, or it surfaces no console link. |
| `UnaryInvocation` | The runtime does not serve the HTTP bridge, or the system prompt never reached the model. |
| `ToolCalling` | The tool path is broken between model, arena mock and model. The mock returns a value the model cannot invent. |
| `MemoryCarriesConversation` | AgentCore Memory is not carrying a session. This is where agentcore differs from vertex and foundry, which keep no store and pin the opposite. |
| `ReapplyIsIdempotent` | An unchanged deploy churns the runtime, costing a rollout for nothing. |
| `DriftIsDetected` | Plan does not notice a deployment destroyed outside the adapter. |
| `DestroyIsIdempotent` | A retried teardown fails, turning an interrupted destroy into console cleanup. |

Each test deploys its own runtime under a unique pack id and destroys it on
cleanup, including on failure. Sharing one name would let a teardown race the
next test's create, and the failure would land on whichever test ran second.

## Prerequisites

- An IAM role AgentCore can assume — trust policy for
  `bedrock-agentcore.amazonaws.com`.
- Credentials able to create and delete AgentCore runtimes and memories.
- A region where Bedrock AgentCore is available and your models are enabled.

## What replaced what

These supersede three files that lived in `internal/agentcore`:

- `pack_lifecycle_test.go` loaded a pack from `../../../messaround/` — a
  fixture in a **sibling repository**, by relative path. The suite could not
  run without a checkout it does not own, and the pack named a model that
  exists in neither `us-west-2` nor `us-east-1`, so every run failed at
  validation before deploying anything.
- `invoke_integration_test.go` required `AGENTCORE_TEST_RUNTIME_ARN` — a
  runtime someone had already deployed by hand. It skipped in every automated
  run, which is why nothing noticed.
- `apply_only_test.go` applied and asserted nothing beyond the absence of an
  error.

`aws_client_integration_test.go` stays where it is: it exercises unexported
client internals, so it cannot move out of the package.
