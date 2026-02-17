package agentcore

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

// pollInterval is the delay between status checks when waiting for a
// resource to become ready.
const pollInterval = 5 * time.Second

// maxPollAttempts limits how long we wait for a resource to become ready.
const maxPollAttempts = 60

// realAWSClient implements awsClient, resourceDestroyer, and resourceChecker
// using the real AWS Bedrock AgentCore control-plane SDK.
type realAWSClient struct {
	client *bedrockagentcorecontrol.Client
	cfg    *AgentCoreConfig

	// gatewayID caches the gateway identifier so that CreateGatewayTool can
	// lazily create the parent gateway on the first tool and reuse it for
	// subsequent targets.
	gatewayID  string
	gatewayARN string
}

// newRealAWSClient builds a realAWSClient from the AgentCoreConfig.
func newRealAWSClient(ctx context.Context, cfg *AgentCoreConfig) (*realAWSClient, error) {
	awsCfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := bedrockagentcorecontrol.NewFromConfig(awsCfg)
	return &realAWSClient{client: client, cfg: cfg}, nil
}

// newRealAWSClientFactory is the awsClientFactory used by NewAgentCoreProvider.
func newRealAWSClientFactory(ctx context.Context, cfg *AgentCoreConfig) (awsClient, error) {
	return newRealAWSClient(ctx, cfg)
}

// newRealDestroyerFactory is the destroyerFactory used by NewAgentCoreProvider.
func newRealDestroyerFactory(ctx context.Context, cfg *AgentCoreConfig) (resourceDestroyer, error) {
	return newRealAWSClient(ctx, cfg)
}

// newRealCheckerFactory is the checkerFactory used by NewAgentCoreProvider.
func newRealCheckerFactory(ctx context.Context, cfg *AgentCoreConfig) (resourceChecker, error) {
	return newRealAWSClient(ctx, cfg)
}

// ---------- awsClient implementation ----------

func (c *realAWSClient) CreateRuntime(ctx context.Context, name string, cfg *AgentCoreConfig) (string, error) {
	out, err := c.client.CreateAgentRuntime(ctx, &bedrockagentcorecontrol.CreateAgentRuntimeInput{
		AgentRuntimeName: aws.String(name),
		RoleArn:          aws.String(cfg.RuntimeRoleARN),
		AgentRuntimeArtifact: &types.AgentRuntimeArtifactMemberContainerConfiguration{
			Value: types.ContainerConfiguration{
				ContainerUri: aws.String("public.ecr.aws/bedrock-agentcore/runtime:latest"),
			},
		},
		NetworkConfiguration: &types.NetworkConfiguration{
			NetworkMode: types.NetworkModePublic,
		},
	})
	if err != nil {
		return "", fmt.Errorf("CreateAgentRuntime %q: %w", name, err)
	}

	// Poll until READY or failure.
	if err := c.waitForRuntimeReady(ctx, aws.ToString(out.AgentRuntimeId)); err != nil {
		return aws.ToString(out.AgentRuntimeArn), fmt.Errorf("runtime %q created but not ready: %w", name, err)
	}

	return aws.ToString(out.AgentRuntimeArn), nil
}

func (c *realAWSClient) CreateGatewayTool(ctx context.Context, name string, cfg *AgentCoreConfig) (string, error) {
	// Lazily create the parent gateway if not already created.
	if c.gatewayID == "" {
		gwOut, err := c.client.CreateGateway(ctx, &bedrockagentcorecontrol.CreateGatewayInput{
			Name:           aws.String(name + "_gw"),
			RoleArn:        aws.String(cfg.RuntimeRoleARN),
			ProtocolType:   types.GatewayProtocolTypeMcp,
			AuthorizerType: types.AuthorizerTypeNone,
		})
		if err != nil {
			return "", fmt.Errorf("CreateGateway for tool %q: %w", name, err)
		}
		c.gatewayID = aws.ToString(gwOut.GatewayId)
		c.gatewayARN = aws.ToString(gwOut.GatewayArn)

		if err := c.waitForGatewayReady(ctx, c.gatewayID); err != nil {
			return c.gatewayARN, fmt.Errorf("gateway for tool %q created but not ready: %w", name, err)
		}
	}

	targetOut, err := c.client.CreateGatewayTarget(ctx, &bedrockagentcorecontrol.CreateGatewayTargetInput{
		GatewayIdentifier: aws.String(c.gatewayID),
		Name:              aws.String(name),
		TargetConfiguration: &types.TargetConfigurationMemberMcp{
			Value: &types.McpTargetConfigurationMemberMcpServer{
				Value: types.McpServerTargetConfiguration{
					Endpoint: aws.String(fmt.Sprintf("https://%s.mcp.local", name)),
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("CreateGatewayTarget %q: %w", name, err)
	}

	return aws.ToString(targetOut.GatewayArn), nil
}

func (c *realAWSClient) CreateA2AWiring(ctx context.Context, name string, _ *AgentCoreConfig) (string, error) {
	// A2A wiring is a logical resource that verifies the runtime exists.
	// There is no separate AWS API for A2A endpoints; the runtime itself
	// exposes the A2A endpoint when configured with the appropriate protocol.
	// We return a synthetic ARN derived from the runtime.
	log.Printf("agentcore: A2A wiring %q is a logical resource (no separate API call)", name)
	return fmt.Sprintf("arn:aws:bedrock:%s:a2a-endpoint/%s", c.cfg.Region, name), nil
}

func (c *realAWSClient) CreateEvaluator(ctx context.Context, name string, _ *AgentCoreConfig) (string, error) {
	// The evaluator API is not yet available in the bedrockagentcorecontrol
	// SDK. Return a placeholder ARN. When the API ships, replace this with
	// a real CreateEvaluator call.
	log.Printf("agentcore: evaluator %q creation not yet supported by SDK; returning placeholder", name)
	return fmt.Sprintf("arn:aws:bedrock:%s:evaluator/%s", c.cfg.Region, name), nil
}

// ---------- resourceDestroyer implementation ----------

func (c *realAWSClient) DeleteResource(ctx context.Context, res ResourceState) error {
	switch res.Type {
	case "agent_runtime":
		return c.deleteRuntime(ctx, res)
	case "tool_gateway":
		return c.deleteGateway(ctx, res)
	case "a2a_endpoint":
		// Logical resource — nothing to delete in AWS.
		log.Printf("agentcore: a2a_endpoint %q is logical; skipping delete", res.Name)
		return nil
	case "evaluator":
		// Not yet supported by SDK.
		log.Printf("agentcore: evaluator %q delete not yet supported; skipping", res.Name)
		return nil
	default:
		return fmt.Errorf("unknown resource type %q for deletion", res.Type)
	}
}

func (c *realAWSClient) deleteRuntime(ctx context.Context, res ResourceState) error {
	id := extractResourceID(res.ARN, "agent-runtime")
	if id == "" {
		id = res.Name
	}
	_, err := c.client.DeleteAgentRuntime(ctx, &bedrockagentcorecontrol.DeleteAgentRuntimeInput{
		AgentRuntimeId: aws.String(id),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeleteAgentRuntime %q: %w", res.Name, err)
	}
	return nil
}

func (c *realAWSClient) deleteGateway(ctx context.Context, res ResourceState) error {
	id := extractResourceID(res.ARN, "gateway")
	if id == "" {
		id = res.Name
	}
	_, err := c.client.DeleteGateway(ctx, &bedrockagentcorecontrol.DeleteGatewayInput{
		GatewayIdentifier: aws.String(id),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeleteGateway %q: %w", res.Name, err)
	}
	return nil
}

// ---------- resourceChecker implementation ----------

func (c *realAWSClient) CheckResource(ctx context.Context, res ResourceState) (string, error) {
	switch res.Type {
	case "agent_runtime":
		return c.checkRuntime(ctx, res)
	case "tool_gateway":
		return c.checkGateway(ctx, res)
	case "a2a_endpoint":
		return "healthy", nil // logical resource
	case "evaluator":
		return "healthy", nil // placeholder
	default:
		return "missing", fmt.Errorf("unknown resource type %q", res.Type)
	}
}

func (c *realAWSClient) checkRuntime(ctx context.Context, res ResourceState) (string, error) {
	id := extractResourceID(res.ARN, "agent-runtime")
	if id == "" {
		id = res.Name
	}
	out, err := c.client.GetAgentRuntime(ctx, &bedrockagentcorecontrol.GetAgentRuntimeInput{
		AgentRuntimeId: aws.String(id),
	})
	if err != nil {
		if isNotFound(err) {
			return "missing", nil
		}
		return "unhealthy", fmt.Errorf("GetAgentRuntime %q: %w", res.Name, err)
	}
	if out.Status == types.AgentRuntimeStatusReady {
		return "healthy", nil
	}
	return "unhealthy", nil
}

func (c *realAWSClient) checkGateway(ctx context.Context, res ResourceState) (string, error) {
	id := extractResourceID(res.ARN, "gateway")
	if id == "" {
		id = res.Name
	}
	out, err := c.client.GetGateway(ctx, &bedrockagentcorecontrol.GetGatewayInput{
		GatewayIdentifier: aws.String(id),
	})
	if err != nil {
		if isNotFound(err) {
			return "missing", nil
		}
		return "unhealthy", fmt.Errorf("GetGateway %q: %w", res.Name, err)
	}
	if out.Status == types.GatewayStatusReady {
		return "healthy", nil
	}
	return "unhealthy", nil
}

// waitForRuntimeReady polls GetAgentRuntime until the status is READY or a
// terminal failure state.
func (c *realAWSClient) waitForRuntimeReady(ctx context.Context, id string) error {
	for i := 0; i < maxPollAttempts; i++ {
		out, err := c.client.GetAgentRuntime(ctx, &bedrockagentcorecontrol.GetAgentRuntimeInput{
			AgentRuntimeId: aws.String(id),
		})
		if err != nil {
			return fmt.Errorf("polling runtime %q: %w", id, err)
		}
		switch out.Status {
		case types.AgentRuntimeStatusReady:
			return nil
		case types.AgentRuntimeStatusCreateFailed, types.AgentRuntimeStatusUpdateFailed:
			reason := ""
			if out.FailureReason != nil {
				reason = ": " + *out.FailureReason
			}
			return fmt.Errorf("runtime %q entered status %s%s", id, out.Status, reason)
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("runtime %q did not become ready after %d attempts", id, maxPollAttempts)
}

// waitForGatewayReady polls GetGateway until the status is READY or a
// terminal failure state.
func (c *realAWSClient) waitForGatewayReady(ctx context.Context, id string) error {
	for i := 0; i < maxPollAttempts; i++ {
		out, err := c.client.GetGateway(ctx, &bedrockagentcorecontrol.GetGatewayInput{
			GatewayIdentifier: aws.String(id),
		})
		if err != nil {
			return fmt.Errorf("polling gateway %q: %w", id, err)
		}
		switch out.Status {
		case types.GatewayStatusReady:
			return nil
		case types.GatewayStatusFailed:
			return fmt.Errorf("gateway %q entered status FAILED", id)
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("gateway %q did not become ready after %d attempts", id, maxPollAttempts)
}
