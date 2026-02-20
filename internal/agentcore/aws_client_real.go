package agentcore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// pollInterval is the delay between status checks when waiting for a
// resource to become ready.
const pollInterval = 5 * time.Second

// maxPollAttempts limits how long we wait for a resource to become ready.
const maxPollAttempts = 60

// listPageSize is the MaxResults value used when listing resources via
// the AgentCore control-plane API.
const listPageSize = 100

// adoptedPlaceholder is used as a sentinel value when a resource already
// exists and is adopted instead of created.
const adoptedPlaceholder = "adopted"

// realAWSClient implements awsClient, resourceDestroyer, and resourceChecker
// using the real AWS Bedrock AgentCore control-plane SDK.
type realAWSClient struct {
	client     *bedrockagentcorecontrol.Client
	logsClient *cloudwatchlogs.Client
	cfg        *Config

	// gatewayID caches the gateway identifier so that CreateGatewayTool can
	// lazily create the parent gateway on the first tool and reuse it for
	// subsequent targets.
	gatewayID   string
	gatewayARN  string
	gatewayName string
}

// newRealAWSClient builds a realAWSClient from the Config.
func newRealAWSClient(ctx context.Context, cfg *Config) (*realAWSClient, error) {
	awsCfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	// Pre-flight check: verify the caller's AWS account matches the account
	// in the runtime_role_arn to catch misconfigurations before any Bedrock
	// API calls are made.
	arnAccount := extractAccountFromARN(cfg.RuntimeRoleARN)
	if arnAccount != "" {
		identity, err := sts.NewFromConfig(awsCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			return nil, fmt.Errorf("STS GetCallerIdentity: %w", err)
		}
		callerAccount := aws.ToString(identity.Account)
		if callerAccount != arnAccount {
			return nil, fmt.Errorf(
				"AWS caller account %s does not match runtime_role_arn account %s"+
					" — check your AWS credentials or update the role ARN",
				callerAccount, arnAccount,
			)
		}
	}

	client := bedrockagentcorecontrol.NewFromConfig(awsCfg)
	logsClient := cloudwatchlogs.NewFromConfig(awsCfg)
	return &realAWSClient{client: client, logsClient: logsClient, cfg: cfg}, nil
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

// isConflictError returns true if the error indicates a 409 Conflict (resource
// already exists).
func isConflictError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ConflictException")
}

// findRuntimeByName lists runtimes and returns the ARN of one matching name.
func (c *realAWSClient) findRuntimeByName(ctx context.Context, name string) (string, error) {
	out, err := c.client.ListAgentRuntimes(ctx, &bedrockagentcorecontrol.ListAgentRuntimesInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return "", err
	}
	for _, rt := range out.AgentRuntimes {
		if aws.ToString(rt.AgentRuntimeName) == name {
			return aws.ToString(rt.AgentRuntimeArn), nil
		}
	}
	return "", fmt.Errorf("runtime %q not found", name)
}

// findGatewayByName lists gateways and returns the ID and ARN of one matching name.
func (c *realAWSClient) findGatewayByName(ctx context.Context, name string) (id, arn string, err error) {
	out, err := c.client.ListGateways(ctx, &bedrockagentcorecontrol.ListGatewaysInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return "", "", err
	}
	for _, gw := range out.Items {
		if aws.ToString(gw.Name) == name {
			gwID := aws.ToString(gw.GatewayId)
			// Get full details to retrieve ARN.
			detail, getErr := c.client.GetGateway(ctx, &bedrockagentcorecontrol.GetGatewayInput{
				GatewayIdentifier: aws.String(gwID),
			})
			if getErr != nil {
				return gwID, "", getErr
			}
			return gwID, aws.ToString(detail.GatewayArn), nil
		}
	}
	return "", "", fmt.Errorf("gateway %q not found", name)
}

// findEvaluatorByName lists evaluators and returns the ARN of one matching name.
func (c *realAWSClient) findEvaluatorByName(ctx context.Context, name string) (string, error) {
	out, err := c.client.ListEvaluators(ctx, &bedrockagentcorecontrol.ListEvaluatorsInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return "", err
	}
	for _, ev := range out.Evaluators {
		if aws.ToString(ev.EvaluatorName) == name {
			return aws.ToString(ev.EvaluatorArn), nil
		}
	}
	return "", fmt.Errorf("evaluator %q not found", name)
}

// findOnlineEvalConfigByName lists online eval configs and returns the ARN of one matching name.
func (c *realAWSClient) findOnlineEvalConfigByName(ctx context.Context, name string) (string, error) {
	out, err := c.client.ListOnlineEvaluationConfigs(ctx, &bedrockagentcorecontrol.ListOnlineEvaluationConfigsInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return "", err
	}
	for _, cfg := range out.OnlineEvaluationConfigs {
		if aws.ToString(cfg.OnlineEvaluationConfigName) == name {
			return aws.ToString(cfg.OnlineEvaluationConfigArn), nil
		}
	}
	return "", fmt.Errorf("online eval config %q not found", name)
}

// findPolicyEngineByName lists policy engines and returns the ARN and ID of one matching name.
func (c *realAWSClient) findPolicyEngineByName(ctx context.Context, name string) (arn, engineID string, err error) {
	out, err := c.client.ListPolicyEngines(ctx, &bedrockagentcorecontrol.ListPolicyEnginesInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return "", "", err
	}
	for _, pe := range out.PolicyEngines {
		if aws.ToString(pe.Name) == name {
			return aws.ToString(pe.PolicyEngineArn), aws.ToString(pe.PolicyEngineId), nil
		}
	}
	return "", "", fmt.Errorf("policy engine %q not found", name)
}

// CreateRuntime provisions an AgentCore runtime via the AWS API and polls
// until it reaches READY status. On conflict (409), adopts the existing runtime.
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
		if isConflictError(err) {
			log.Printf("agentcore: runtime %q already exists, adopting", name)
			return c.findRuntimeByName(ctx, name)
		}
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
	id := extractResourceID(arn, "runtime")
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
		if isConflictError(err) {
			log.Printf("agentcore: gateway target %q already exists, adopting", name)
			return c.gatewayARN, nil
		}
		return "", fmt.Errorf("CreateGatewayTarget %q: %w", name, err)
	}

	return aws.ToString(targetOut.GatewayArn), nil
}

// createParentGateway provisions the shared gateway and waits for it to
// become ready.
func (c *realAWSClient) createParentGateway(
	ctx context.Context, name string, cfg *Config,
) error {
	gwName := name + "-gw"
	gwInput := &bedrockagentcorecontrol.CreateGatewayInput{
		Name:           aws.String(gwName),
		RoleArn:        aws.String(cfg.RuntimeRoleARN),
		ProtocolType:   types.GatewayProtocolTypeMcp,
		AuthorizerType: types.AuthorizerTypeNone,
	}
	if len(cfg.ResourceTags) > 0 {
		gwInput.Tags = cfg.ResourceTags
	}
	gwOut, err := c.client.CreateGateway(ctx, gwInput)
	if err != nil {
		if isConflictError(err) {
			log.Printf("agentcore: gateway %q already exists, adopting", gwName)
			id, arn, findErr := c.findGatewayByName(ctx, gwName)
			if findErr != nil {
				return fmt.Errorf("CreateGateway for tool %q (adopt): %w", name, findErr)
			}
			c.gatewayID = id
			c.gatewayARN = arn
			c.gatewayName = gwName
			return nil
		}
		return fmt.Errorf("CreateGateway for tool %q: %w", name, err)
	}
	c.gatewayID = aws.ToString(gwOut.GatewayId)
	c.gatewayARN = aws.ToString(gwOut.GatewayArn)
	c.gatewayName = gwName

	if err := c.waitForGatewayReady(ctx, c.gatewayID); err != nil {
		return fmt.Errorf("gateway for tool %q created but not ready: %w", name, err)
	}
	return nil
}

// AssociatePolicyEngine updates the gateway to reference a policy engine.
// This must be called after both the gateway and policy engine exist so the
// engine's Cedar schema includes the gateway's registered tools/actions.
func (c *realAWSClient) AssociatePolicyEngine(
	ctx context.Context, policyEngineARN string, cfg *Config,
) error {
	if c.gatewayID == "" {
		return fmt.Errorf("no gateway to associate policy engine with")
	}
	_, err := c.client.UpdateGateway(ctx, &bedrockagentcorecontrol.UpdateGatewayInput{
		GatewayIdentifier: aws.String(c.gatewayID),
		Name:              aws.String(c.gatewayName),
		RoleArn:           aws.String(cfg.RuntimeRoleARN),
		ProtocolType:      types.GatewayProtocolTypeMcp,
		AuthorizerType:    types.AuthorizerTypeNone,
		PolicyEngineConfiguration: &types.GatewayPolicyEngineConfiguration{
			Arn:  aws.String(policyEngineARN),
			Mode: types.GatewayPolicyEngineModeEnforce,
		},
	})
	if err != nil {
		return fmt.Errorf("UpdateGateway to associate policy engine: %w", err)
	}
	log.Printf("agentcore: associated policy engine with gateway %s", c.gatewayID)

	// Wait for gateway to become ready after the update.
	if err := c.waitForGatewayReady(ctx, c.gatewayID); err != nil {
		return fmt.Errorf("gateway not ready after policy engine association: %w", err)
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

// defaultEvalModel is the default Bedrock model ID used for LLM-as-a-Judge evaluators.
const defaultEvalModel = "anthropic.claude-sonnet-4-20250514-v1:0"

// defaultRatingScaleSize is the default number of levels in numerical rating scales.
const defaultRatingScaleSize = 5

// CreateEvaluator provisions an evaluator via the AWS API and polls until
// it reaches ACTIVE status. The eval definition is looked up from cfg.EvalDefs.
func (c *realAWSClient) CreateEvaluator(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	evalDef, ok := cfg.EvalDefs[name]
	if !ok {
		return "", fmt.Errorf("CreateEvaluator %q: no eval definition found", name)
	}

	level := mapTriggerToLevel(evalDef.Trigger)
	instructions := evalParamString(evalDef.Params, "instructions", "Evaluate the agent response quality.")
	instructions = ensureEvalPlaceholders(instructions)
	modelID := evalParamString(evalDef.Params, "model", defaultEvalModel)

	input := &bedrockagentcorecontrol.CreateEvaluatorInput{
		EvaluatorName: aws.String(name),
		Level:         level,
		EvaluatorConfig: &types.EvaluatorConfigMemberLlmAsAJudge{
			Value: types.LlmAsAJudgeEvaluatorConfig{
				Instructions: aws.String(instructions),
				ModelConfig: &types.EvaluatorModelConfigMemberBedrockEvaluatorModelConfig{
					Value: types.BedrockEvaluatorModelConfig{
						ModelId: aws.String(modelID),
					},
				},
				RatingScale: buildNumericalRatingScale(evalDef.Params),
			},
		},
	}
	if evalDef.Description != "" {
		input.Description = aws.String(evalDef.Description)
	}
	if len(cfg.ResourceTags) > 0 {
		input.Tags = cfg.ResourceTags
	}

	out, err := c.client.CreateEvaluator(ctx, input)
	if err != nil {
		if isConflictError(err) {
			log.Printf("agentcore: evaluator %q already exists, adopting", name)
			return c.findEvaluatorByName(ctx, name)
		}
		return "", fmt.Errorf("CreateEvaluator %q: %w", name, err)
	}

	if err := c.waitForEvaluatorReady(ctx, aws.ToString(out.EvaluatorId)); err != nil {
		return aws.ToString(out.EvaluatorArn),
			fmt.Errorf("evaluator %q created but not active: %w", name, err)
	}

	return aws.ToString(out.EvaluatorArn), nil
}

// mapTriggerToLevel maps a PromptKit eval trigger to an SDK evaluator level.
func mapTriggerToLevel(trigger evals.EvalTrigger) types.EvaluatorLevel {
	switch trigger {
	case evals.TriggerOnSessionComplete, evals.TriggerSampleSessions:
		return types.EvaluatorLevelSession
	case evals.TriggerEveryTurn, evals.TriggerSampleTurns:
		return types.EvaluatorLevelTrace
	}
	return types.EvaluatorLevelTrace
}

// evalParamString extracts a string parameter with a default fallback.
func evalParamString(params map[string]any, key, defaultVal string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return defaultVal
}

// ensureEvalPlaceholders appends required AWS evaluator placeholders if none
// are present. AWS requires at least one of {context} or {assistant_turn}
// for TRACE-level evaluators.
func ensureEvalPlaceholders(instructions string) string {
	if strings.Contains(instructions, "{context}") ||
		strings.Contains(instructions, "{assistant_turn}") ||
		strings.Contains(instructions, "{user_input}") {
		return instructions
	}
	return instructions + "\n\nContext: {context}\nAssistant response: {assistant_turn}"
}

// buildNumericalRatingScale builds a 1–N numerical rating scale from eval params.
func buildNumericalRatingScale(params map[string]any) *types.RatingScaleMemberNumerical {
	size := defaultRatingScaleSize
	if v, ok := params["rating_scale_size"]; ok {
		if n, ok := v.(float64); ok && n >= 2 {
			size = int(n)
		}
	}
	defs := make([]types.NumericalScaleDefinition, size)
	for i := range size {
		val := float64(i + 1)
		defs[i] = types.NumericalScaleDefinition{
			Value:      aws.Float64(val),
			Label:      aws.String(fmt.Sprintf("Score %d", i+1)),
			Definition: aws.String(fmt.Sprintf("Rating level %d of %d", i+1, size)),
		}
	}
	return &types.RatingScaleMemberNumerical{Value: defs}
}

// waitForEvaluatorReady polls GetEvaluator until status is ACTIVE or a
// terminal failure state.
func (c *realAWSClient) waitForEvaluatorReady(ctx context.Context, id string) error {
	for i := 0; i < maxPollAttempts; i++ {
		out, err := c.client.GetEvaluator(ctx, &bedrockagentcorecontrol.GetEvaluatorInput{
			EvaluatorId: aws.String(id),
		})
		if err != nil {
			return fmt.Errorf("polling evaluator %q: %w", id, err)
		}
		switch out.Status {
		case types.EvaluatorStatusActive:
			return nil
		case types.EvaluatorStatusCreateFailed, types.EvaluatorStatusUpdateFailed:
			return fmt.Errorf("evaluator %q entered status %s", id, out.Status)
		case types.EvaluatorStatusCreating, types.EvaluatorStatusUpdating, types.EvaluatorStatusDeleting:
			// Transitional — keep polling.
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("evaluator %q did not become active after %d attempts", id, maxPollAttempts)
}

// defaultSamplingPercentage is the default sampling percentage for online
// evaluation configs when not specified in eval params.
const defaultSamplingPercentage = 100.0

// defaultTraceLogGroup is the CloudWatch log group where AgentCore runtimes
// emit OTEL traces via Transaction Search. Online eval configs read from this
// group to evaluate agent interactions.
const defaultTraceLogGroup = "aws/spans"

// ensureLogGroup creates the CloudWatch log group if it does not already exist.
func (c *realAWSClient) ensureLogGroup(ctx context.Context, logGroupName string) error {
	_, err := c.logsClient.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	})
	if err != nil {
		// Ignore ResourceAlreadyExistsException — the log group already exists.
		if strings.Contains(err.Error(), "ResourceAlreadyExistsException") {
			return nil
		}
		return fmt.Errorf("create log group %q: %w", logGroupName, err)
	}
	log.Printf("[agentcore] created CloudWatch log group %s", logGroupName)
	return nil
}

// CreateOnlineEvalConfig provisions an online evaluation config that wires
// evaluators to agent runtime traces via CloudWatch logs.
func (c *realAWSClient) CreateOnlineEvalConfig(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	logGroup := resolveLogGroup(cfg)

	// Ensure the log group exists before referencing it. For the default
	// "aws/spans" group, Transaction Search must be enabled — the group is
	// AWS-managed and cannot be created manually.
	if logGroup != defaultTraceLogGroup {
		if err := c.ensureLogGroup(ctx, logGroup); err != nil {
			return "", fmt.Errorf("CreateOnlineEvalConfig %q: %w", name, err)
		}
	}

	// Service name follows AgentCore convention: <runtime-name>.DEFAULT
	packID := cfg.ResourceTags[TagKeyPackID]
	serviceName := packID + ".DEFAULT"

	evalRefs := buildEvaluatorReferences(cfg.EvalARNs, cfg.BuiltinEvalIDs)
	if len(evalRefs) == 0 {
		return "", fmt.Errorf("CreateOnlineEvalConfig %q: no evaluator references available", name)
	}

	samplingPct := resolveSamplingPercentage(cfg)

	input := &bedrockagentcorecontrol.CreateOnlineEvaluationConfigInput{
		OnlineEvaluationConfigName: aws.String(name),
		EvaluationExecutionRoleArn: aws.String(cfg.RuntimeRoleARN),
		EnableOnCreate:             aws.Bool(true),
		DataSourceConfig: &types.DataSourceConfigMemberCloudWatchLogs{
			Value: types.CloudWatchLogsInputConfig{
				LogGroupNames: []string{logGroup},
				ServiceNames:  []string{serviceName},
			},
		},
		Evaluators: evalRefs,
		Rule: &types.Rule{
			SamplingConfig: &types.SamplingConfig{
				SamplingPercentage: aws.Float64(samplingPct),
			},
		},
	}
	if cfg.ResourceTags != nil {
		input.Tags = cfg.ResourceTags
	}

	out, err := c.client.CreateOnlineEvaluationConfig(ctx, input)
	if err != nil {
		if isConflictError(err) {
			log.Printf("agentcore: online eval config %q already exists, adopting", name)
			return c.findOnlineEvalConfigByName(ctx, name)
		}
		return "", fmt.Errorf("CreateOnlineEvaluationConfig %q: %w", name, err)
	}

	if err := c.waitForOnlineEvalConfigReady(
		ctx, aws.ToString(out.OnlineEvaluationConfigId),
	); err != nil {
		return aws.ToString(out.OnlineEvaluationConfigArn),
			fmt.Errorf("online eval config %q created but not active: %w", name, err)
	}

	return aws.ToString(out.OnlineEvaluationConfigArn), nil
}

// resolveLogGroup returns the CloudWatch log group for online eval config.
// Defaults to "aws/spans" (the Transaction Search spans log group) which is
// where OTEL-instrumented AgentCore runtimes write their traces.
func resolveLogGroup(cfg *Config) string {
	if cfg.Observability != nil && cfg.Observability.CloudWatchLogGroup != "" {
		return cfg.Observability.CloudWatchLogGroup
	}
	return defaultTraceLogGroup
}

// buildEvaluatorReferences converts evaluator ARNs and built-in IDs into
// SDK EvaluatorReference values. Built-in evaluators (e.g. "Builtin.Helpfulness")
// are referenced directly by ID without needing CreateEvaluator.
func buildEvaluatorReferences(evalARNs map[string]string, builtinIDs []string) []types.EvaluatorReference {
	refs := make([]types.EvaluatorReference, 0, len(evalARNs)+len(builtinIDs))

	// Custom evaluators — extract ID from ARN.
	for _, arn := range evalARNs {
		evalID := extractResourceID(arn, "evaluator")
		if evalID == "" {
			continue
		}
		refs = append(refs, &types.EvaluatorReferenceMemberEvaluatorId{
			Value: evalID,
		})
	}

	// Built-in evaluators — use ID directly (e.g. "Builtin.Helpfulness").
	for _, id := range builtinIDs {
		refs = append(refs, &types.EvaluatorReferenceMemberEvaluatorId{
			Value: id,
		})
	}

	return refs
}

// resolveSamplingPercentage extracts the sampling percentage from eval params,
// defaulting to 100%.
func resolveSamplingPercentage(cfg *Config) float64 {
	for _, def := range cfg.EvalDefs {
		if v, ok := def.Params["sample_percentage"]; ok {
			if pct, ok := v.(float64); ok && pct > 0 {
				return pct
			}
		}
	}
	return defaultSamplingPercentage
}

// waitForOnlineEvalConfigReady polls GetOnlineEvaluationConfig until ACTIVE
// or a terminal failure state.
func (c *realAWSClient) waitForOnlineEvalConfigReady(ctx context.Context, id string) error {
	for i := 0; i < maxPollAttempts; i++ {
		out, err := c.client.GetOnlineEvaluationConfig(ctx,
			&bedrockagentcorecontrol.GetOnlineEvaluationConfigInput{
				OnlineEvaluationConfigId: aws.String(id),
			})
		if err != nil {
			return fmt.Errorf("polling online eval config %q: %w", id, err)
		}
		switch out.Status {
		case types.OnlineEvaluationConfigStatusActive:
			return nil
		case types.OnlineEvaluationConfigStatusCreateFailed,
			types.OnlineEvaluationConfigStatusUpdateFailed:
			reason := ""
			if out.FailureReason != nil {
				reason = ": " + *out.FailureReason
			}
			return fmt.Errorf("online eval config %q entered status %s%s", id, out.Status, reason)
		case types.OnlineEvaluationConfigStatusCreating,
			types.OnlineEvaluationConfigStatusUpdating,
			types.OnlineEvaluationConfigStatusDeleting:
			// Transitional — keep polling.
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("online eval config %q did not become active after %d attempts", id, maxPollAttempts)
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
		if isConflictError(err) {
			log.Printf("agentcore: policy engine %q already exists, adopting and purging stale policies", name)
			arn, id, findErr := c.findPolicyEngineByName(ctx, name)
			if findErr != nil {
				return "", "", findErr
			}
			// Purge stale policies so fresh ones can be created.
			if purgeErr := c.purgeAllPolicies(ctx, id); purgeErr != nil {
				return "", "", fmt.Errorf("purge stale policies on engine %q: %w", name, purgeErr)
			}
			return arn, id, nil
		}
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
		if isConflictError(err) {
			log.Printf("agentcore: cedar policy %q already exists on engine %q, adopting", name, engineID)
			return adoptedPlaceholder, adoptedPlaceholder, nil
		}
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
		return c.deleteEvaluator(ctx, res)
	case ResTypeOnlineEvalConfig:
		return c.deleteOnlineEvalConfig(ctx, res)
	case ResTypeCedarPolicy:
		return c.deleteCedarPolicy(ctx, res)
	default:
		return fmt.Errorf("unknown resource type %q for deletion", res.Type)
	}
}

func (c *realAWSClient) deleteRuntime(ctx context.Context, res ResourceState) error {
	id := extractResourceID(res.ARN, "runtime")
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
	if engineID == "" {
		return nil
	}

	// Delete ALL policies in the engine, not just the one stored in metadata.
	if err := c.purgeAllPolicies(ctx, engineID); err != nil {
		return err
	}

	_, err := c.client.DeletePolicyEngine(ctx, &bedrockagentcorecontrol.DeletePolicyEngineInput{
		PolicyEngineId: aws.String(engineID),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeletePolicyEngine %q: %w", res.Name, err)
	}

	return nil
}

// purgeAllPolicies lists and deletes every policy within a policy engine.
func (c *realAWSClient) purgeAllPolicies(ctx context.Context, engineID string) error {
	out, err := c.client.ListPolicies(ctx, &bedrockagentcorecontrol.ListPoliciesInput{
		PolicyEngineId: aws.String(engineID),
		MaxResults:     aws.Int32(listPageSize),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("ListPolicies on engine %q: %w", engineID, err)
	}
	for _, p := range out.Policies {
		policyID := aws.ToString(p.PolicyId)
		if policyID == "" {
			continue
		}
		_, delErr := c.client.DeletePolicy(ctx, &bedrockagentcorecontrol.DeletePolicyInput{
			PolicyEngineId: aws.String(engineID),
			PolicyId:       aws.String(policyID),
		})
		if delErr != nil && !isNotFound(delErr) {
			return fmt.Errorf("DeletePolicy %q on engine %q: %w", policyID, engineID, delErr)
		}
		log.Printf("agentcore: deleted policy %s from engine %s", policyID, engineID)
	}
	return nil
}

func (c *realAWSClient) deleteOnlineEvalConfig(ctx context.Context, res ResourceState) error {
	id := extractResourceID(res.ARN, "online-evaluation-config")
	if id == "" {
		id = res.Name
	}
	_, err := c.client.DeleteOnlineEvaluationConfig(ctx,
		&bedrockagentcorecontrol.DeleteOnlineEvaluationConfigInput{
			OnlineEvaluationConfigId: aws.String(id),
		})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeleteOnlineEvaluationConfig %q: %w", res.Name, err)
	}
	return nil
}

func (c *realAWSClient) deleteEvaluator(ctx context.Context, res ResourceState) error {
	id := extractResourceID(res.ARN, "evaluator")
	if id == "" {
		id = res.Name
	}
	_, err := c.client.DeleteEvaluator(ctx, &bedrockagentcorecontrol.DeleteEvaluatorInput{
		EvaluatorId: aws.String(id),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeleteEvaluator %q: %w", res.Name, err)
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
		return c.checkEvaluator(ctx, res)
	case ResTypeOnlineEvalConfig:
		return c.checkOnlineEvalConfig(ctx, res)
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
	id := extractResourceID(res.ARN, "runtime")
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

func (c *realAWSClient) checkOnlineEvalConfig(ctx context.Context, res ResourceState) (string, error) {
	id := extractResourceID(res.ARN, "online-evaluation-config")
	if id == "" {
		id = res.Name
	}
	out, err := c.client.GetOnlineEvaluationConfig(ctx,
		&bedrockagentcorecontrol.GetOnlineEvaluationConfigInput{
			OnlineEvaluationConfigId: aws.String(id),
		})
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetOnlineEvaluationConfig %q: %w", res.Name, err)
	}
	if out.Status == types.OnlineEvaluationConfigStatusActive {
		return StatusHealthy, nil
	}
	return StatusUnhealthy, nil
}

func (c *realAWSClient) checkEvaluator(ctx context.Context, res ResourceState) (string, error) {
	id := extractResourceID(res.ARN, "evaluator")
	if id == "" {
		id = res.Name
	}
	out, err := c.client.GetEvaluator(ctx, &bedrockagentcorecontrol.GetEvaluatorInput{
		EvaluatorId: aws.String(id),
	})
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetEvaluator %q: %w", res.Name, err)
	}
	if out.Status == types.EvaluatorStatusActive {
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
