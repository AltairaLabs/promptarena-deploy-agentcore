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
	envVars := runtimeEnvVarsForAgent(cfg, name)
	input := &bedrockagentcorecontrol.CreateAgentRuntimeInput{
		AgentRuntimeName: aws.String(name),
		RoleArn:          aws.String(cfg.RuntimeRoleARN),
		AgentRuntimeArtifact: &types.AgentRuntimeArtifactMemberContainerConfiguration{
			Value: types.ContainerConfiguration{
				ContainerUri: aws.String(cfg.containerImageForAgent(name)),
			},
		},
		NetworkConfiguration: &types.NetworkConfiguration{
			NetworkMode: types.NetworkModePublic,
		},
	}
	if len(envVars) > 0 {
		input.EnvironmentVariables = envVars
	}
	if authCfg := buildAuthorizerConfig(cfg); authCfg != nil {
		input.AuthorizerConfiguration = authCfg
	}
	if tags := tagsWithAgent(cfg.ResourceTags, name); len(tags) > 0 {
		input.Tags = tags
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

	envVars := runtimeEnvVarsForAgent(cfg, name)
	input := &bedrockagentcorecontrol.UpdateAgentRuntimeInput{
		AgentRuntimeId: aws.String(id),
		RoleArn:        aws.String(cfg.RuntimeRoleARN),
		AgentRuntimeArtifact: &types.AgentRuntimeArtifactMemberContainerConfiguration{
			Value: types.ContainerConfiguration{
				ContainerUri: aws.String(cfg.containerImageForAgent(name)),
			},
		},
		NetworkConfiguration: &types.NetworkConfiguration{
			NetworkMode: types.NetworkModePublic,
		},
	}
	if len(envVars) > 0 {
		input.EnvironmentVariables = envVars
	}
	if authCfg := buildAuthorizerConfig(cfg); authCfg != nil {
		input.AuthorizerConfiguration = authCfg
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
	gwInput := &bedrockagentcorecontrol.CreateGatewayInput{
		Name:           aws.String(name + "_gw"),
		RoleArn:        aws.String(cfg.RuntimeRoleARN),
		ProtocolType:   types.GatewayProtocolTypeMcp,
		AuthorizerType: types.AuthorizerTypeNone,
	}
	if len(cfg.ResourceTags) > 0 {
		gwInput.Tags = cfg.ResourceTags
	}
	gwOut, err := c.client.CreateGateway(ctx, gwInput)
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

// buildAuthorizerConfig returns the SDK AuthorizerConfiguration for the
// given config, or nil if no auth is configured (or IAM mode is used).
func buildAuthorizerConfig(cfg *Config) types.AuthorizerConfiguration {
	if cfg.A2AAuth == nil || cfg.A2AAuth.Mode != A2AAuthModeJWT {
		return nil
	}
	return &types.AuthorizerConfigurationMemberCustomJWTAuthorizer{
		Value: types.CustomJWTAuthorizerConfiguration{
			DiscoveryUrl:    aws.String(cfg.A2AAuth.DiscoveryURL),
			AllowedAudience: cfg.A2AAuth.AllowedAud,
			AllowedClients:  cfg.A2AAuth.AllowedClts,
		},
	}
}

// memoryExpiryDays is the default event expiry duration for memory resources.
const memoryExpiryDays = 30

// memoryStrategySession is the strategy name for session (episodic) memory.
const memoryStrategySession = "session_memory"

// memoryStrategyPersistent is the strategy name for persistent (semantic) memory.
const memoryStrategyPersistent = "persistent_memory"

// CreateMemory provisions a memory resource via the AWS API.
func (c *realAWSClient) CreateMemory(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	input := &bedrockagentcorecontrol.CreateMemoryInput{
		Name:                aws.String(name),
		EventExpiryDuration: aws.Int32(memoryExpiryDays),
	}

	if cfg.RuntimeRoleARN != "" {
		input.MemoryExecutionRoleArn = aws.String(cfg.RuntimeRoleARN)
	}

	input.MemoryStrategies = memoryStrategies(cfg.MemoryStore)
	if len(cfg.ResourceTags) > 0 {
		input.Tags = cfg.ResourceTags
	}

	out, err := c.client.CreateMemory(ctx, input)
	if err != nil {
		return "", fmt.Errorf("CreateMemory %q: %w", name, err)
	}

	return aws.ToString(out.Memory.Arn), nil
}

// memoryStrategies returns the SDK strategy inputs for the given store type.
func memoryStrategies(storeType string) []types.MemoryStrategyInput {
	switch storeType {
	case "session":
		return []types.MemoryStrategyInput{
			&types.MemoryStrategyInputMemberEpisodicMemoryStrategy{
				Value: types.EpisodicMemoryStrategyInput{
					Name: aws.String(memoryStrategySession),
				},
			},
		}
	case "persistent":
		return []types.MemoryStrategyInput{
			&types.MemoryStrategyInputMemberSemanticMemoryStrategy{
				Value: types.SemanticMemoryStrategyInput{
					Name: aws.String(memoryStrategyPersistent),
				},
			},
		}
	default:
		return nil
	}
}

// CreatePolicyEngine provisions a policy engine and polls until it reaches
// ACTIVE status.
func (c *realAWSClient) CreatePolicyEngine(
	ctx context.Context, name string, _ *Config,
) (arn, engineID string, err error) {
	out, err := c.client.CreatePolicyEngine(ctx, &bedrockagentcorecontrol.CreatePolicyEngineInput{
		Name: aws.String(name),
	})
	if err != nil {
		return "", "", fmt.Errorf("CreatePolicyEngine %q: %w", name, err)
	}

	engineID = aws.ToString(out.PolicyEngineId)
	if err = c.waitForPolicyEngineActive(ctx, engineID); err != nil {
		return aws.ToString(out.PolicyEngineArn), engineID,
			fmt.Errorf("policy engine %q created but not active: %w", name, err)
	}

	return aws.ToString(out.PolicyEngineArn), engineID, nil
}

// CreateCedarPolicy creates a Cedar policy within a policy engine.
func (c *realAWSClient) CreateCedarPolicy(
	ctx context.Context, engineID string, name string, cedarStatement string, _ *Config,
) (policyARN, policyID string, retErr error) {
	out, err := c.client.CreatePolicy(ctx, &bedrockagentcorecontrol.CreatePolicyInput{
		PolicyEngineId: aws.String(engineID),
		Name:           aws.String(name),
		Definition: &types.PolicyDefinitionMemberCedar{
			Value: types.CedarPolicy{
				Statement: aws.String(cedarStatement),
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("CreatePolicy %q on engine %q: %w", name, engineID, err)
	}

	return aws.ToString(out.PolicyArn), aws.ToString(out.PolicyId), nil
}

// waitForPolicyEngineActive polls GetPolicyEngine until the status is ACTIVE.
func (c *realAWSClient) waitForPolicyEngineActive(ctx context.Context, id string) error {
	for i := 0; i < maxPollAttempts; i++ {
		out, err := c.client.GetPolicyEngine(ctx, &bedrockagentcorecontrol.GetPolicyEngineInput{
			PolicyEngineId: aws.String(id),
		})
		if err != nil {
			return fmt.Errorf("polling policy engine %q: %w", id, err)
		}
		if out.Status == types.PolicyEngineStatusActive {
			return nil
		}
		if out.Status == types.PolicyEngineStatusCreating {
			time.Sleep(pollInterval)
			continue
		}
		return fmt.Errorf("policy engine %q entered status %s", id, out.Status)
	}
	return fmt.Errorf("policy engine %q did not become active after %d attempts", id, maxPollAttempts)
}

// ---------- resourceDestroyer implementation ----------

// DeleteResource removes a single resource by type.
func (c *realAWSClient) DeleteResource(ctx context.Context, res ResourceState) error {
	switch res.Type {
	case ResTypeMemory:
		return c.deleteMemory(ctx, res)
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
	case ResTypeCedarPolicy:
		return c.deleteCedarPolicy(ctx, res)
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

func (c *realAWSClient) deleteMemory(ctx context.Context, res ResourceState) error {
	id := extractResourceID(res.ARN, "memory")
	if id == "" {
		id = res.Name
	}
	_, err := c.client.DeleteMemory(ctx, &bedrockagentcorecontrol.DeleteMemoryInput{
		MemoryId: aws.String(id),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeleteMemory %q: %w", res.Name, err)
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

func (c *realAWSClient) deleteCedarPolicy(ctx context.Context, res ResourceState) error {
	engineID := res.Metadata["policy_engine_id"]
	policyID := res.Metadata["policy_id"]

	if policyID != "" && engineID != "" {
		_, err := c.client.DeletePolicy(ctx, &bedrockagentcorecontrol.DeletePolicyInput{
			PolicyEngineId: aws.String(engineID),
			PolicyId:       aws.String(policyID),
		})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("DeletePolicy %q: %w", res.Name, err)
		}
	}

	if engineID != "" {
		_, err := c.client.DeletePolicyEngine(ctx, &bedrockagentcorecontrol.DeletePolicyEngineInput{
			PolicyEngineId: aws.String(engineID),
		})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("DeletePolicyEngine %q: %w", res.Name, err)
		}
	}

	return nil
}

// ---------- resourceChecker implementation ----------

// CheckResource returns the health status of a single resource.
func (c *realAWSClient) CheckResource(ctx context.Context, res ResourceState) (string, error) {
	switch res.Type {
	case ResTypeMemory:
		return c.checkMemory(ctx, res)
	case ResTypeAgentRuntime:
		return c.checkRuntime(ctx, res)
	case ResTypeToolGateway:
		return c.checkGateway(ctx, res)
	case ResTypeA2AEndpoint:
		return StatusHealthy, nil
	case ResTypeEvaluator:
		return StatusHealthy, nil
	case ResTypeCedarPolicy:
		return c.checkCedarPolicy(ctx, res)
	default:
		return StatusMissing, fmt.Errorf("unknown resource type %q", res.Type)
	}
}

func (c *realAWSClient) checkMemory(ctx context.Context, res ResourceState) (string, error) {
	id := extractResourceID(res.ARN, "memory")
	if id == "" {
		id = res.Name
	}
	out, err := c.client.GetMemory(ctx, &bedrockagentcorecontrol.GetMemoryInput{
		MemoryId: aws.String(id),
	})
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetMemory %q: %w", res.Name, err)
	}
	if out.Memory != nil && out.Memory.Status == types.MemoryStatusActive {
		return StatusHealthy, nil
	}
	return StatusUnhealthy, nil
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

func (c *realAWSClient) checkCedarPolicy(ctx context.Context, res ResourceState) (string, error) {
	engineID := res.Metadata["policy_engine_id"]
	if engineID == "" {
		return StatusMissing, nil
	}
	out, err := c.client.GetPolicyEngine(ctx, &bedrockagentcorecontrol.GetPolicyEngineInput{
		PolicyEngineId: aws.String(engineID),
	})
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetPolicyEngine %q: %w", res.Name, err)
	}
	if out.Status == types.PolicyEngineStatusActive {
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
