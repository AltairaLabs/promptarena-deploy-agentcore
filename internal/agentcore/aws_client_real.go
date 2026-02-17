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
	cfg    *Config

	// gatewayID caches the gateway identifier so that CreateGatewayTool can
	// lazily create the parent gateway on the first tool and reuse it for
	// subsequent targets.
	gatewayID  string
	gatewayARN string
}

// newRealAWSClient builds a realAWSClient from the Config.
func newRealAWSClient(ctx context.Context, cfg *Config) (*realAWSClient, error) {
	awsCfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := bedrockagentcorecontrol.NewFromConfig(awsCfg)
	return &realAWSClient{client: client, cfg: cfg}, nil
}

// newRealAWSClientFactory is the awsClientFactory used by NewProvider.
func newRealAWSClientFactory(ctx context.Context, cfg *Config) (awsClient, error) {
	return newRealAWSClient(ctx, cfg)
}

// newRealDestroyerFactory is the destroyerFactory used by NewProvider.
func newRealDestroyerFactory(ctx context.Context, cfg *Config) (resourceDestroyer, error) {
	return newRealAWSClient(ctx, cfg)
}

// newRealCheckerFactory is the checkerFactory used by NewProvider.
func newRealCheckerFactory(ctx context.Context, cfg *Config) (resourceChecker, error) {
	return newRealAWSClient(ctx, cfg)
}

// ---------- awsClient implementation ----------

// CreateRuntime provisions an AgentCore runtime via the AWS API and polls
// until it reaches READY status.
func (c *realAWSClient) CreateRuntime(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	input := &bedrockagentcorecontrol.CreateAgentRuntimeInput{
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
	}
	if len(cfg.RuntimeEnvVars) > 0 {
		input.EnvironmentVariables = cfg.RuntimeEnvVars
	}
	out, err := c.client.CreateAgentRuntime(ctx, input)
	if err != nil {
		return "", fmt.Errorf("CreateAgentRuntime %q: %w", name, err)
	}

	if err := c.waitForRuntimeReady(ctx, aws.ToString(out.AgentRuntimeId)); err != nil {
		return aws.ToString(out.AgentRuntimeArn),
			fmt.Errorf("runtime %q created but not ready: %w", name, err)
	}

	return aws.ToString(out.AgentRuntimeArn), nil
}

// UpdateRuntime updates an existing AgentCore runtime and polls until it
// reaches READY status.
func (c *realAWSClient) UpdateRuntime(
	ctx context.Context, arn string, name string, cfg *Config,
) (string, error) {
	id := extractResourceID(arn, "agent-runtime")
	if id == "" {
		return "", fmt.Errorf("UpdateAgentRuntime %q: could not extract ID from ARN %q", name, arn)
	}

	input := &bedrockagentcorecontrol.UpdateAgentRuntimeInput{
		AgentRuntimeId: aws.String(id),
		RoleArn:        aws.String(cfg.RuntimeRoleARN),
		AgentRuntimeArtifact: &types.AgentRuntimeArtifactMemberContainerConfiguration{
			Value: types.ContainerConfiguration{
				ContainerUri: aws.String("public.ecr.aws/bedrock-agentcore/runtime:latest"),
			},
		},
		NetworkConfiguration: &types.NetworkConfiguration{
			NetworkMode: types.NetworkModePublic,
		},
	}
	if len(cfg.RuntimeEnvVars) > 0 {
		input.EnvironmentVariables = cfg.RuntimeEnvVars
	}
	_, err := c.client.UpdateAgentRuntime(ctx, input)
	if err != nil {
		return arn, fmt.Errorf("UpdateAgentRuntime %q: %w", name, err)
	}

	if err := c.waitForRuntimeReady(ctx, id); err != nil {
		return arn, fmt.Errorf("runtime %q updated but not ready: %w", name, err)
	}

	return arn, nil
}

// CreateGatewayTool provisions a tool gateway target, lazily creating the
// parent gateway on the first invocation.
func (c *realAWSClient) CreateGatewayTool(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	if c.gatewayID == "" {
		if err := c.createParentGateway(ctx, name, cfg); err != nil {
			return c.gatewayARN, err
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

// createParentGateway provisions the shared gateway and waits for it to
// become ready.
func (c *realAWSClient) createParentGateway(
	ctx context.Context, name string, cfg *Config,
) error {
	gwOut, err := c.client.CreateGateway(ctx, &bedrockagentcorecontrol.CreateGatewayInput{
		Name:           aws.String(name + "_gw"),
		RoleArn:        aws.String(cfg.RuntimeRoleARN),
		ProtocolType:   types.GatewayProtocolTypeMcp,
		AuthorizerType: types.AuthorizerTypeNone,
	})
	if err != nil {
		return fmt.Errorf("CreateGateway for tool %q: %w", name, err)
	}
	c.gatewayID = aws.ToString(gwOut.GatewayId)
	c.gatewayARN = aws.ToString(gwOut.GatewayArn)

	if err := c.waitForGatewayReady(ctx, c.gatewayID); err != nil {
		return fmt.Errorf("gateway for tool %q created but not ready: %w", name, err)
	}
	return nil
}

// CreateA2AWiring registers a logical A2A endpoint. No separate AWS API
// call is required; the runtime exposes A2A when configured.
func (c *realAWSClient) CreateA2AWiring(
	_ context.Context, name string, _ *Config,
) (string, error) {
	log.Printf("agentcore: A2A wiring %q is a logical resource (no separate API call)", name)
	return fmt.Sprintf("arn:aws:bedrock:%s:a2a-endpoint/%s", c.cfg.Region, name), nil
}

// CreateEvaluator returns a placeholder ARN. The evaluator API is not yet
// available in the SDK.
func (c *realAWSClient) CreateEvaluator(
	_ context.Context, name string, _ *Config,
) (string, error) {
	log.Printf("agentcore: evaluator %q creation not yet supported by SDK; returning placeholder", name)
	return fmt.Sprintf("arn:aws:bedrock:%s:evaluator/%s", c.cfg.Region, name), nil
}

// ---------- resourceDestroyer implementation ----------

// DeleteResource removes a single resource by type.
func (c *realAWSClient) DeleteResource(ctx context.Context, res ResourceState) error {
	switch res.Type {
	case ResTypeAgentRuntime:
		return c.deleteRuntime(ctx, res)
	case ResTypeToolGateway:
		return c.deleteGateway(ctx, res)
	case ResTypeA2AEndpoint:
		log.Printf("agentcore: a2a_endpoint %q is logical; skipping delete", res.Name)
		return nil
	case ResTypeEvaluator:
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

// CheckResource returns the health status of a single resource.
func (c *realAWSClient) CheckResource(ctx context.Context, res ResourceState) (string, error) {
	switch res.Type {
	case ResTypeAgentRuntime:
		return c.checkRuntime(ctx, res)
	case ResTypeToolGateway:
		return c.checkGateway(ctx, res)
	case ResTypeA2AEndpoint:
		return StatusHealthy, nil
	case ResTypeEvaluator:
		return StatusHealthy, nil
	default:
		return StatusMissing, fmt.Errorf("unknown resource type %q", res.Type)
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
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetAgentRuntime %q: %w", res.Name, err)
	}
	if out.Status == types.AgentRuntimeStatusReady {
		return StatusHealthy, nil
	}
	return StatusUnhealthy, nil
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
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetGateway %q: %w", res.Name, err)
	}
	if out.Status == types.GatewayStatusReady {
		return StatusHealthy, nil
	}
	return StatusUnhealthy, nil
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
		case types.AgentRuntimeStatusCreating,
			types.AgentRuntimeStatusUpdating,
			types.AgentRuntimeStatusDeleting:
			// Transitional states — keep polling.
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
		case types.GatewayStatusCreating,
			types.GatewayStatusUpdating,
			types.GatewayStatusUpdateUnsuccessful,
			types.GatewayStatusDeleting:
			// Transitional states — keep polling.
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("gateway %q did not become ready after %d attempts", id, maxPollAttempts)
}
