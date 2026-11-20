package agentcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/v2/evals"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	client        *bedrockagentcorecontrol.Client
	logsClient    *cloudwatchlogs.Client
	s3Client      *s3.Client
	bedrockClient *bedrock.Client
	cfg           *Config

	// gatewayID caches the gateway identifier so that CreateGatewayTool can
	// lazily create the parent gateway on the first tool and reuse it for
	// subsequent targets.
	gatewayID   string
	gatewayARN  string
	gatewayName string
	// gatewayURL is the MCP endpoint an agent calls to reach these tools.
	// Provisioning the gateway without carrying this anywhere leaves the
	// targets reachable by nothing.
	gatewayURL string
	// gatewayAuthorizer records how the gateway authenticates callers, so an
	// update can put back what the gateway already had. UpdateGateway is a
	// full replace and requires this field, so sending a fixed value silently
	// rewrites it — on an adopted gateway that means stripping its auth.
	gatewayAuthorizer     types.AuthorizerType
	gatewayAuthorizerConf types.AuthorizerConfiguration
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
	s3Client := s3.NewFromConfig(awsCfg)
	bedrockClient := bedrock.NewFromConfig(awsCfg)
	return &realAWSClient{
		client: client, logsClient: logsClient,
		s3Client: s3Client, bedrockClient: bedrockClient, cfg: cfg,
	}, nil
}

// EvalModelCatalog lists what the configured region offers an evaluator: its
// foundation models, and which of those run on demand, and its inference
// profiles. Used to pre-flight evaluator models and to pick the default judge
// model before Apply creates anything; callers treat an error as "unknown",
// not "unavailable".
func (c *realAWSClient) EvalModelCatalog(ctx context.Context) (*evalModelCatalog, error) {
	out, err := c.bedrockClient.ListFoundationModels(ctx, &bedrock.ListFoundationModelsInput{})
	if err != nil {
		return nil, fmt.Errorf("ListFoundationModels: %w", err)
	}
	catalog := newEvalModelCatalog()
	for i := range out.ModelSummaries {
		m := &out.ModelSummaries[i]
		catalog.addFoundationModel(aws.ToString(m.ModelId), aws.ToString(m.ModelArn),
			slices.Contains(m.InferenceTypesSupported, bedrocktypes.InferenceTypeOnDemand))
	}
	// System-defined profiles are the cross-region ones AWS publishes;
	// application profiles are ones the account made, which an eval may name
	// by ARN.
	for _, typ := range []bedrocktypes.InferenceProfileType{
		bedrocktypes.InferenceProfileTypeSystemDefined, bedrocktypes.InferenceProfileTypeApplication,
	} {
		if err := c.addInferenceProfiles(ctx, catalog, typ); err != nil {
			return nil, err
		}
	}
	catalog.finish()
	return catalog, nil
}

// addInferenceProfiles adds every active inference profile of one type.
func (c *realAWSClient) addInferenceProfiles(
	ctx context.Context, catalog *evalModelCatalog, typ bedrocktypes.InferenceProfileType,
) error {
	pages := bedrock.NewListInferenceProfilesPaginator(c.bedrockClient, &bedrock.ListInferenceProfilesInput{
		TypeEquals: typ,
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("ListInferenceProfiles: %w", err)
		}
		for i := range page.InferenceProfileSummaries {
			p := &page.InferenceProfileSummaries[i]
			if p.Status != bedrocktypes.InferenceProfileStatusActive {
				continue
			}
			modelARNs := make([]string, 0, len(p.Models))
			for _, m := range p.Models {
				modelARNs = append(modelARNs, aws.ToString(m.ModelArn))
			}
			catalog.addProfile(aws.ToString(p.InferenceProfileId), aws.ToString(p.InferenceProfileArn), modelARNs)
		}
	}
	return nil
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

// findGatewayByName lists gateways and returns the ID, ARN and MCP endpoint of
// one matching name.
func (c *realAWSClient) findGatewayByName(
	ctx context.Context, name string,
) (found gatewayDetails, err error) {
	out, err := c.client.ListGateways(ctx, &bedrockagentcorecontrol.ListGatewaysInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return gatewayDetails{}, err
	}
	for _, gw := range out.Items {
		if aws.ToString(gw.Name) == name {
			return c.getGatewayDetails(ctx, aws.ToString(gw.GatewayId))
		}
	}
	return gatewayDetails{}, fmt.Errorf("gateway %q not found", name)
}

// getGatewayDetails reads the ARN, name, MCP endpoint, and how the gateway
// authenticates its callers.
func (c *realAWSClient) getGatewayDetails(ctx context.Context, gwID string) (gatewayDetails, error) {
	detail, err := c.client.GetGateway(ctx, &bedrockagentcorecontrol.GetGatewayInput{
		GatewayIdentifier: aws.String(gwID),
	})
	if err != nil {
		return gatewayDetails{id: gwID}, err
	}
	return gatewayDetails{
		id:             gwID,
		arn:            aws.ToString(detail.GatewayArn),
		name:           aws.ToString(detail.Name),
		url:            aws.ToString(detail.GatewayUrl),
		authorizer:     detail.AuthorizerType,
		authorizerConf: detail.AuthorizerConfiguration,
		status:         detail.Status,
	}, nil
}

// gatewayDetails is what an existing gateway tells us about itself.
type gatewayDetails struct {
	id, arn, name, url string
	authorizer         types.AuthorizerType
	authorizerConf     types.AuthorizerConfiguration
	status             types.GatewayStatus
}

// gatewayUnusable reports whether a gateway can never take new targets: it is
// being deleted, or its creation failed.
func gatewayUnusable(status types.GatewayStatus) bool {
	return status == types.GatewayStatusDeleting || status == types.GatewayStatusFailed
}

// adoptPriorGateway makes the prior state's gateway this apply's parent
// gateway, waiting for it to be ready. It reports false, with no error, when
// there is no prior gateway or it is gone or unusable, so the caller creates
// one.
func (c *realAWSClient) adoptPriorGateway(ctx context.Context, cfg *Config) (bool, error) {
	id := extractResourceID(cfg.PriorGatewayARN, "gateway")
	if id == "" {
		return false, nil
	}
	found, err := c.getGatewayDetails(ctx, id)
	if err != nil {
		if isNotFound(err) {
			log.Printf("agentcore: gateway %q from prior state no longer exists; creating one", id)
			return false, nil
		}
		return false, fmt.Errorf("GetGateway %q: %w", id, err)
	}
	if gatewayUnusable(found.status) {
		log.Printf("agentcore: gateway %q from prior state is %s; creating one", found.name, found.status)
		return false, nil
	}
	log.Printf("agentcore: keeping existing gateway %q", found.name)
	c.useGateway(&found)
	if found.status != types.GatewayStatusReady {
		if err := c.waitForGatewayReady(ctx, found.id); err != nil {
			return true, fmt.Errorf("existing gateway %q not ready: %w", found.name, err)
		}
	}
	return true, nil
}

// useGateway makes found the parent gateway for this apply's tools.
func (c *realAWSClient) useGateway(found *gatewayDetails) {
	c.gatewayID = found.id
	c.gatewayARN = found.arn
	c.gatewayURL = found.url
	c.gatewayName = found.name
	c.gatewayAuthorizer = found.authorizer
	c.gatewayAuthorizerConf = found.authorizerConf
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

// buildRuntimeArtifact returns the runtime_override image as a container
// artifact when one is set, and otherwise the CodeConfiguration artifact
// referencing the S3 code package uploaded during prepareApply.
func buildRuntimeArtifact(cfg *Config) types.AgentRuntimeArtifact {
	if image := cfg.overrideImage(); image != "" {
		return &types.AgentRuntimeArtifactMemberContainerConfiguration{
			Value: types.ContainerConfiguration{ContainerUri: aws.String(image)},
		}
	}
	accountID := extractAccountFromARN(cfg.RuntimeRoleARN)
	bucket := codeDeployS3Bucket(accountID, cfg.Region)
	packID := cfg.ResourceTags[TagKeyPackID]
	version := cfg.ResourceTags[TagKeyVersion]
	key := codeDeployS3Key(packID, version)
	return &types.AgentRuntimeArtifactMemberCodeConfiguration{
		Value: types.CodeConfiguration{
			Code: &types.CodeMemberS3{
				Value: types.S3Location{
					Bucket: aws.String(bucket),
					Prefix: aws.String(key),
				},
			},
			EntryPoint: []string{codeDeployEntryPoint},
			Runtime:    types.AgentManagedRuntimeTypePython313,
		},
	}
}

// UploadCodePackage uploads a ZIP archive to S3 for code deploy mode.
func (c *realAWSClient) UploadCodePackage(
	ctx context.Context, zipData []byte, bucket, key string,
) error {
	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(zipData),
	})
	if err != nil {
		return fmt.Errorf("S3 PutObject %s/%s: %w", bucket, key, err)
	}
	log.Printf("agentcore: uploaded code package to s3://%s/%s (%d bytes)", bucket, key, len(zipData))
	return nil
}

// CreateRuntime provisions an AgentCore runtime via the AWS API and polls
// until it reaches READY status. On conflict (409), adopts the existing runtime.
func (c *realAWSClient) CreateRuntime(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	envVars := runtimeEnvVarsForAgent(cfg, name)
	artifact := buildRuntimeArtifact(cfg)

	input := &bedrockagentcorecontrol.CreateAgentRuntimeInput{
		AgentRuntimeName:     aws.String(name),
		RoleArn:              aws.String(cfg.RuntimeRoleARN),
		AgentRuntimeArtifact: artifact,
		NetworkConfiguration: &types.NetworkConfiguration{
			NetworkMode: types.NetworkModePublic,
		},
	}
	if proto := resolveServerProtocol(cfg); proto != "" {
		input.ProtocolConfiguration = &types.ProtocolConfiguration{
			ServerProtocol: types.ServerProtocol(proto),
		}
	}
	if len(envVars) > 0 {
		input.EnvironmentVariables = envVars
	}
	input.AuthorizerConfiguration, input.RequestHeaderConfiguration = runtimeAccess(cfg)
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
	artifact := buildRuntimeArtifact(cfg)
	input := &bedrockagentcorecontrol.UpdateAgentRuntimeInput{
		AgentRuntimeId:       aws.String(id),
		RoleArn:              aws.String(cfg.RuntimeRoleARN),
		AgentRuntimeArtifact: artifact,
		NetworkConfiguration: &types.NetworkConfiguration{
			NetworkMode: types.NetworkModePublic,
		},
	}
	if proto := resolveServerProtocol(cfg); proto != "" {
		input.ProtocolConfiguration = &types.ProtocolConfiguration{
			ServerProtocol: types.ServerProtocol(proto),
		}
	}
	if len(envVars) > 0 {
		input.EnvironmentVariables = envVars
	}
	input.AuthorizerConfiguration, input.RequestHeaderConfiguration = runtimeAccess(cfg)
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

	input := &bedrockagentcorecontrol.CreateGatewayTargetInput{
		GatewayIdentifier: aws.String(c.gatewayID),
		// Sanitized for the same reason as the parent gateway: a target name
		// rejects underscores too, so a tool called "lookup_order" is refused
		// here one call after the gateway refused it. Destroy purges targets by
		// listing them rather than by name, so renaming breaks no lookup.
		Name:                aws.String(sanitizeGatewayTargetName(name)),
		TargetConfiguration: buildTargetConfig(name, cfg),
	}
	if creds := buildCredentialProviderConfigs(name, cfg); len(creds) > 0 {
		input.CredentialProviderConfigurations = creds
	}
	targetOut, err := c.client.CreateGatewayTarget(ctx, input)
	if err != nil {
		if isConflictError(err) {
			log.Printf("agentcore: gateway target %q already exists, adopting", name)
			return c.gatewayARN, nil
		}
		// The parent gateway exists whatever happened to the target; its ARN
		// lets a failed apply's state record it, so destroy can remove it.
		return c.gatewayARN, fmt.Errorf("CreateGatewayTarget %q: %w", name, err)
	}

	return aws.ToString(targetOut.GatewayArn), nil
}

// createParentGateway finds or provisions the shared gateway and waits for it
// to become ready.
//
// The gateway the prior state's tools already use is kept, whatever it is
// called: deployments made when the gateway was named after its first tool
// keep that gateway. Only when there is none, or it has been deleted outside
// promptarena, is one created, under the pack-derived cfg.GatewayName.
func (c *realAWSClient) createParentGateway(
	ctx context.Context, name string, cfg *Config,
) error {
	if kept, err := c.adoptPriorGateway(ctx, cfg); kept || err != nil {
		return err
	}

	// Never derived from a tool here: that is what named a different
	// gateway whenever the first tool changed.
	gwName := cfg.GatewayName
	if gwName == "" {
		return fmt.Errorf("CreateGateway for tool %q: no gateway name configured", name)
	}
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
			found, findErr := c.findGatewayByName(ctx, gwName)
			if findErr != nil {
				return fmt.Errorf("CreateGateway for tool %q (adopt): %w", name, findErr)
			}
			c.useGateway(&found)
			if found.status != types.GatewayStatusReady {
				return c.waitForGatewayReady(ctx, found.id)
			}
			return nil
		}
		return fmt.Errorf("CreateGateway for tool %q: %w", name, err)
	}
	c.gatewayID = aws.ToString(gwOut.GatewayId)
	c.gatewayARN = aws.ToString(gwOut.GatewayArn)
	c.gatewayURL = aws.ToString(gwOut.GatewayUrl)
	c.gatewayName = gwName
	c.gatewayAuthorizer = gwOut.AuthorizerType
	c.gatewayAuthorizerConf = gwOut.AuthorizerConfiguration

	if err := c.waitForGatewayReady(ctx, c.gatewayID); err != nil {
		return fmt.Errorf("gateway for tool %q created but not ready: %w", name, err)
	}
	return nil
}

// GatewayEndpoint reports the gateway's MCP endpoint and authorizer.
func (c *realAWSClient) GatewayEndpoint() (url, authorizer string) {
	return c.gatewayURL, string(c.gatewayAuthorizer)
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
		// What the gateway already uses, not a fixed value: this call is a
		// full replace, and an adopted gateway may authenticate its callers.
		AuthorizerType:          c.gatewayAuthorizer,
		AuthorizerConfiguration: c.gatewayAuthorizerConf,
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

// DisassociatePolicyEngine removes the gateway's policy engine, leaving the
// gateway otherwise as it is. UpdateGateway is a full replace, so sending it
// without a PolicyEngineConfiguration clears the association.
//
// Apply calls this when the pack no longer has any Cedar policy but keeps its
// gateway: the engine it used is about to be deleted, and AWS refuses to
// delete an engine a gateway still enforces.
func (c *realAWSClient) DisassociatePolicyEngine(ctx context.Context, cfg *Config) error {
	if c.gatewayID == "" {
		return nil
	}
	_, err := c.client.UpdateGateway(ctx, &bedrockagentcorecontrol.UpdateGatewayInput{
		GatewayIdentifier:       aws.String(c.gatewayID),
		Name:                    aws.String(c.gatewayName),
		RoleArn:                 aws.String(cfg.RuntimeRoleARN),
		ProtocolType:            types.GatewayProtocolTypeMcp,
		AuthorizerType:          c.gatewayAuthorizer,
		AuthorizerConfiguration: c.gatewayAuthorizerConf,
	})
	if err != nil {
		return fmt.Errorf("UpdateGateway to remove policy engine: %w", err)
	}
	log.Printf("agentcore: removed policy engine from gateway %s", c.gatewayID)
	if err := c.waitForGatewayReady(ctx, c.gatewayID); err != nil {
		return fmt.Errorf("gateway not ready after removing policy engine: %w", err)
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

	level := mapTriggerToLevel(evals.EvalTrigger(evalDef.Trigger))
	instructions := evalParamString(evalDef.Params, "instructions", "Evaluate the agent response quality.")
	instructions = ensureEvalPlaceholders(instructions)
	modelID := evalModelFor(&evalDef, cfg.defaultEvalModel())

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
	case evals.TriggerOnSessionComplete, evals.TriggerSampleSessions,
		evals.TriggerOnConversationComplete:
		return types.EvaluatorLevelSession
	case evals.TriggerEveryTurn, evals.TriggerSampleTurns,
		evals.TriggerOnWorkflowStep:
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
	for range maxPollAttempts {
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

// ensureLogGroup creates the CloudWatch log group if it does not already
// exist, tagged as created for the online eval config oecName so destroy can
// tell it from a group that was there before (see deleteCreatedLogGroup).
func (c *realAWSClient) ensureLogGroup(ctx context.Context, logGroupName, oecName string, cfg *Config) error {
	_, err := c.logsClient.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(logGroupName),
		Tags:         createdLogGroupTags(cfg.ResourceTags, oecName),
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

// findLogGroup returns the log group named exactly name, or nil when there is
// none.
func (c *realAWSClient) findLogGroup(ctx context.Context, name string) (*cwltypes.LogGroup, error) {
	pages := cloudwatchlogs.NewDescribeLogGroupsPaginator(c.logsClient, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(name),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("DescribeLogGroups %q: %w", name, err)
		}
		for i := range page.LogGroups {
			if aws.ToString(page.LogGroups[i].LogGroupName) == name {
				return &page.LogGroups[i], nil
			}
		}
	}
	return nil, nil
}

// LogGroupExists reports whether the named CloudWatch log group exists.
func (c *realAWSClient) LogGroupExists(ctx context.Context, name string) (bool, error) {
	group, err := c.findLogGroup(ctx, name)
	return group != nil, err
}

// deleteCreatedLogGroup deletes the online eval config's log group when apply
// created it for that config, and leaves it alone otherwise.
//
// Apply creates a configured cloudwatch_log_group that does not exist yet, and
// destroy used to leave it behind. Only a group carrying this config's
// creation tag is deleted: one that existed before the deploy, or that another
// pack's config created, is not this deploy's to remove.
func (c *realAWSClient) deleteCreatedLogGroup(ctx context.Context, logGroupName, oecName string) error {
	if logGroupName == "" || logGroupName == defaultTraceLogGroup {
		return nil
	}
	group, err := c.findLogGroup(ctx, logGroupName)
	if err != nil || group == nil {
		return err
	}
	tags, err := c.logsClient.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{
		ResourceArn: group.LogGroupArn,
	})
	if err != nil {
		return fmt.Errorf("ListTagsForResource %q: %w", logGroupName, err)
	}
	if !createdForOnlineEvalConfig(tags.Tags, oecName) {
		log.Printf("agentcore: leaving log group %s in place: this deploy did not create it", logGroupName)
		return nil
	}
	_, err = c.logsClient.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	})
	var notFound *cwltypes.ResourceNotFoundException
	if err != nil && !errors.As(err, &notFound) {
		return fmt.Errorf("DeleteLogGroup %q: %w", logGroupName, err)
	}
	log.Printf("agentcore: deleted log group %s", logGroupName)
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
		if err := c.ensureLogGroup(ctx, logGroup, name, cfg); err != nil {
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

// UpdateOnlineEvalConfig points an existing online evaluation config at the
// evaluators this apply produced.
//
// Without it a re-apply adopted the existing config unchanged, so an
// evaluator dropped from the pack stayed referenced by it, and deleting that
// evaluator afterwards either failed or left the config pointing at nothing.
// A config that is gone is created again.
func (c *realAWSClient) UpdateOnlineEvalConfig(
	ctx context.Context, arn string, name string, cfg *Config,
) (string, error) {
	id := extractResourceID(arn, "online-evaluation-config")
	if id == "" {
		return c.CreateOnlineEvalConfig(ctx, name, cfg)
	}
	evalRefs := buildEvaluatorReferences(cfg.EvalARNs, cfg.BuiltinEvalIDs)
	if len(evalRefs) == 0 {
		return "", fmt.Errorf("UpdateOnlineEvaluationConfig %q: no evaluator references available", name)
	}
	if logGroup := resolveLogGroup(cfg); logGroup != defaultTraceLogGroup {
		if err := c.ensureLogGroup(ctx, logGroup, name, cfg); err != nil {
			return "", fmt.Errorf("UpdateOnlineEvaluationConfig %q: %w", name, err)
		}
	}
	_, err := c.client.UpdateOnlineEvaluationConfig(ctx, &bedrockagentcorecontrol.UpdateOnlineEvaluationConfigInput{
		OnlineEvaluationConfigId:   aws.String(id),
		EvaluationExecutionRoleArn: aws.String(cfg.RuntimeRoleARN),
		DataSourceConfig: &types.DataSourceConfigMemberCloudWatchLogs{
			Value: types.CloudWatchLogsInputConfig{
				LogGroupNames: []string{resolveLogGroup(cfg)},
				ServiceNames:  []string{cfg.ResourceTags[TagKeyPackID] + ".DEFAULT"},
			},
		},
		Evaluators: evalRefs,
		Rule: &types.Rule{
			SamplingConfig: &types.SamplingConfig{
				SamplingPercentage: aws.Float64(resolveSamplingPercentage(cfg)),
			},
		},
	})
	if err != nil {
		if isNotFound(err) {
			return c.CreateOnlineEvalConfig(ctx, name, cfg)
		}
		return "", fmt.Errorf("UpdateOnlineEvaluationConfig %q: %w", name, err)
	}
	if err := c.waitForOnlineEvalConfigReady(ctx, id); err != nil {
		return arn, fmt.Errorf("online eval config %q updated but not active: %w", name, err)
	}
	return arn, nil
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
	for name := range cfg.EvalDefs {
		if v, ok := cfg.EvalDefs[name].Params["sample_percentage"]; ok {
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
	for range maxPollAttempts {
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

// runtimeAccess is how the runtime authenticates its callers and which of
// their headers it receives: the JWT authorizer and the header allowlist, nil
// for either that is not configured.
func runtimeAccess(cfg *Config) (types.AuthorizerConfiguration, types.RequestHeaderConfiguration) {
	return buildAuthorizerConfig(cfg), buildRequestHeaderConfig(cfg)
}

// buildRequestHeaderConfig returns the runtime's request header allowlist,
// or nil when none is configured.
func buildRequestHeaderConfig(cfg *Config) types.RequestHeaderConfiguration {
	if len(cfg.RequestHeaderAllowlist) == 0 {
		return nil
	}
	return &types.RequestHeaderConfigurationMemberRequestHeaderAllowlist{
		Value: append([]string(nil), cfg.RequestHeaderAllowlist...),
	}
}

// defaultMemoryExpiryDays is the default event expiry duration for memory
// resources when not specified in config.
const defaultMemoryExpiryDays = 30

// SDK strategy name constants passed to the AWS API.
const (
	sdkStrategyNameEpisodic       = "episodic_memory"
	sdkStrategyNameSemantic       = "semantic_memory"
	sdkStrategyNameSummary        = "summary_memory"
	sdkStrategyNameUserPreference = "user_preference_memory"
)

// CreateMemory provisions a memory resource via the AWS API and polls until
// it reaches ACTIVE status. If a memory with the same name already exists,
// it is adopted.
func (c *realAWSClient) CreateMemory(
	ctx context.Context, name string, cfg *Config,
) (string, error) {
	expiryDays := resolveExpiryDays(cfg.Memory.EventExpiryDays)
	input := &bedrockagentcorecontrol.CreateMemoryInput{
		Name:                aws.String(name),
		EventExpiryDuration: aws.Int32(expiryDays),
	}

	if cfg.RuntimeRoleARN != "" {
		input.MemoryExecutionRoleArn = aws.String(cfg.RuntimeRoleARN)
	}

	if cfg.Memory.EncryptionKeyARN != "" {
		input.EncryptionKeyArn = aws.String(cfg.Memory.EncryptionKeyARN)
	}

	input.MemoryStrategies = memoryStrategies(cfg.Memory.Strategies)
	if len(cfg.ResourceTags) > 0 {
		input.Tags = cfg.ResourceTags
	}

	return c.createMemoryWithRetry(ctx, name, input)
}

// createMemoryWithRetry attempts to create a memory, handling the case where
// a memory with the same name already exists. If the existing memory is being
// deleted, it waits for deletion to complete and retries.
func (c *realAWSClient) createMemoryWithRetry(
	ctx context.Context, name string, input *bedrockagentcorecontrol.CreateMemoryInput,
) (string, error) {
	for range maxPollAttempts {
		out, err := c.client.CreateMemory(ctx, input)
		if err == nil {
			memoryID := aws.ToString(out.Memory.Id)
			if pollErr := c.waitForMemoryActive(ctx, memoryID); pollErr != nil {
				return aws.ToString(out.Memory.Arn),
					fmt.Errorf("memory %q created but not active: %w", name, pollErr)
			}
			return aws.ToString(out.Memory.Arn), nil
		}
		if !isMemoryAlreadyExists(err) {
			return "", fmt.Errorf("CreateMemory %q: %w", name, err)
		}
		// Memory already exists — try to adopt it.
		arn, findErr := c.findMemoryByName(ctx, name)
		if findErr == nil {
			log.Printf("agentcore: memory %q already exists, adopting", name)
			return arn, nil
		}
		// Not found means the old memory is still deleting. Wait for it to
		// finish before retrying, rather than looping on CreateMemory.
		log.Printf("agentcore: memory %q exists but is deleting, waiting for deletion", name)
		if waitErr := c.waitForDeletingMemoryGone(ctx, name); waitErr != nil {
			return "", fmt.Errorf("memory %q: waiting for deletion: %w", name, waitErr)
		}
	}
	return "", fmt.Errorf("memory %q: timed out waiting for previous deletion", name)
}

// waitForDeletingMemoryGone polls ListMemories until no memory with the given
// name prefix exists (i.e. the DELETING memory has been fully removed).
func (c *realAWSClient) waitForDeletingMemoryGone(ctx context.Context, name string) error {
	for range maxPollAttempts {
		out, err := c.client.ListMemories(ctx, &bedrockagentcorecontrol.ListMemoriesInput{
			MaxResults: aws.Int32(listPageSize),
		})
		if err != nil {
			return fmt.Errorf("ListMemories: %w", err)
		}
		found := false
		for _, m := range out.Memories {
			if strings.HasPrefix(aws.ToString(m.Id), name) {
				found = true
				log.Printf("agentcore: memory %q still %s, waiting", name, m.Status)
				break
			}
		}
		if !found {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("memory %q still exists after %d poll attempts", name, maxPollAttempts)
}

// isMemoryAlreadyExists checks for the "already exists" validation error
// returned by CreateMemory (unlike other resources that return ConflictException).
func isMemoryAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}

// findMemoryByName lists memories and returns the ARN of one matching name,
// waiting for it to reach ACTIVE status.
func (c *realAWSClient) findMemoryByName(ctx context.Context, name string) (string, error) {
	out, err := c.client.ListMemories(ctx, &bedrockagentcorecontrol.ListMemoriesInput{
		MaxResults: aws.Int32(listPageSize),
	})
	if err != nil {
		return "", err
	}
	for _, m := range out.Memories {
		id := aws.ToString(m.Id)
		// Memory IDs include the name as a prefix (e.g. "myname-AbCdEfGh").
		// Skip memories that are being deleted — they can't be adopted.
		if strings.HasPrefix(id, name) && m.Status != types.MemoryStatusDeleting {
			if err := c.waitForMemoryActive(ctx, id); err != nil {
				return aws.ToString(m.Arn), err
			}
			return aws.ToString(m.Arn), nil
		}
	}
	return "", fmt.Errorf("memory %q not found", name)
}

// waitForMemoryActive polls GetMemory until the status is ACTIVE or a
// terminal failure state.
func (c *realAWSClient) waitForMemoryActive(ctx context.Context, id string) error {
	for range maxPollAttempts {
		out, err := c.client.GetMemory(ctx, &bedrockagentcorecontrol.GetMemoryInput{
			MemoryId: aws.String(id),
		})
		if err != nil {
			return fmt.Errorf("polling memory %q: %w", id, err)
		}
		if out.Memory == nil {
			return fmt.Errorf("memory %q: nil response", id)
		}
		switch out.Memory.Status {
		case types.MemoryStatusActive:
			return nil
		case types.MemoryStatusFailed:
			return fmt.Errorf("memory %q entered status FAILED", id)
		case types.MemoryStatusDeleting:
			return fmt.Errorf("memory %q is being deleted", id)
		case types.MemoryStatusCreating:
			// Transitional — keep polling.
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("memory %q did not become active after %d attempts", id, maxPollAttempts)
}

// resolveExpiryDays returns the configured expiry or the default.
func resolveExpiryDays(configured int32) int32 {
	if configured > 0 {
		return configured
	}
	return defaultMemoryExpiryDays
}

// memoryStrategies returns the SDK strategy inputs for the given
// canonical strategy names.
func memoryStrategies(strategies []string) []types.MemoryStrategyInput {
	result := make([]types.MemoryStrategyInput, 0, len(strategies))
	for _, s := range strategies {
		if si := buildStrategyInput(s); si != nil {
			result = append(result, si)
		}
	}
	return result
}

// buildStrategyInput maps a canonical strategy name to its SDK type.
func buildStrategyInput(strategy string) types.MemoryStrategyInput {
	switch strategy {
	case StrategyEpisodic:
		return &types.MemoryStrategyInputMemberEpisodicMemoryStrategy{
			Value: types.EpisodicMemoryStrategyInput{
				Name: aws.String(sdkStrategyNameEpisodic),
			},
		}
	case StrategySemantic:
		return &types.MemoryStrategyInputMemberSemanticMemoryStrategy{
			Value: types.SemanticMemoryStrategyInput{
				Name: aws.String(sdkStrategyNameSemantic),
			},
		}
	case StrategySummary:
		return &types.MemoryStrategyInputMemberSummaryMemoryStrategy{
			Value: types.SummaryMemoryStrategyInput{
				Name: aws.String(sdkStrategyNameSummary),
			},
		}
	case StrategyUserPreference:
		return &types.MemoryStrategyInputMemberUserPreferenceMemoryStrategy{
			Value: types.UserPreferenceMemoryStrategyInput{
				Name: aws.String(sdkStrategyNameUserPreference),
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

// CreateCedarPolicy creates a Cedar policy within a policy engine and
// polls until the policy reaches ACTIVE or fails.
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
		// A blocklist entry is an unconditional forbid on one tool, which the
		// Cedar analyzer always reports as "Overly Restrictive: Policy Engine
		// will deny every request" for that action. That is the point of a
		// blocklist, and under the default FAIL_ON_ANY_FINDINGS no blocklist
		// policy could ever be created.
		ValidationMode: types.PolicyValidationModeIgnoreAllFindings,
	})
	if err != nil {
		if isConflictError(err) {
			log.Printf("agentcore: cedar policy %q already exists on engine %q, adopting", name, engineID)
			return adoptedPlaceholder, adoptedPlaceholder, nil
		}
		return "", "", fmt.Errorf("CreatePolicy %q on engine %q: %w", name, engineID, err)
	}

	pID := aws.ToString(out.PolicyId)
	if err := c.waitForPolicyActive(ctx, engineID, pID, name); err != nil {
		return aws.ToString(out.PolicyArn), pID, err
	}
	return aws.ToString(out.PolicyArn), pID, nil
}

// waitForPolicyActive polls GetPolicy until the policy leaves CREATING state.
func (c *realAWSClient) waitForPolicyActive(ctx context.Context, engineID, policyID, name string) error {
	for range maxPollAttempts {
		out, err := c.client.GetPolicy(ctx, &bedrockagentcorecontrol.GetPolicyInput{
			PolicyEngineId: aws.String(engineID),
			PolicyId:       aws.String(policyID),
		})
		if err != nil {
			return fmt.Errorf("polling policy %q: %w", name, err)
		}
		switch out.Status {
		case types.PolicyStatusActive:
			return nil
		case types.PolicyStatusCreateFailed, types.PolicyStatusUpdateFailed:
			reasons := strings.Join(out.StatusReasons, "; ")
			return fmt.Errorf("policy %q failed: %s", name, reasons)
		case types.PolicyStatusCreating, types.PolicyStatusUpdating:
			log.Printf("agentcore: waiting for policy %q (status: %s)", name, out.Status)
			time.Sleep(pollInterval)
		case types.PolicyStatusDeleting, types.PolicyStatusDeleteFailed:
			return fmt.Errorf("policy %q unexpected status: %s", name, out.Status)
		}
	}
	return fmt.Errorf("policy %q still CREATING after %d poll attempts", name, maxPollAttempts)
}

// waitForPolicyEngineActive polls GetPolicyEngine until the status is ACTIVE.
func (c *realAWSClient) waitForPolicyEngineActive(ctx context.Context, id string) error {
	for range maxPollAttempts {
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

	// Check current status — if already deleting, nothing to do.
	out, err := c.client.GetMemory(ctx, &bedrockagentcorecontrol.GetMemoryInput{
		MemoryId: aws.String(id),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("GetMemory %q before delete: %w", res.Name, err)
	}
	if out.Memory != nil && out.Memory.Status == types.MemoryStatusDeleting {
		log.Printf("agentcore: memory %q already deleting, skipping", res.Name)
		return nil
	}

	// If still creating, wait for it to finish before deleting.
	if out.Memory != nil && out.Memory.Status == types.MemoryStatusCreating {
		if waitErr := c.waitForMemoryActive(ctx, id); waitErr != nil && isNotFound(waitErr) {
			return nil
		}
	}

	_, err = c.client.DeleteMemory(ctx, &bedrockagentcorecontrol.DeleteMemoryInput{
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

	// Delete all targets before the gateway — AWS rejects gateway deletion
	// while targets are still associated.
	if err := c.purgeAllGatewayTargets(ctx, id); err != nil {
		return fmt.Errorf("purge targets before DeleteGateway %q: %w", res.Name, err)
	}

	_, err := c.client.DeleteGateway(ctx, &bedrockagentcorecontrol.DeleteGatewayInput{
		GatewayIdentifier: aws.String(id),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("DeleteGateway %q: %w", res.Name, err)
	}
	return nil
}

// DeleteGatewayTarget deletes one tool's target from a gateway that other
// tools still use, leaving the gateway and its other targets in place.
//
// Every tool_gateway entry in state carries the shared gateway's ARN, so
// DeleteResource on one of them removes the whole gateway. That is right for
// destroy and wrong for an apply that dropped a single tool. A gateway or
// target that is already gone counts as deleted.
func (c *realAWSClient) DeleteGatewayTarget(ctx context.Context, gatewayARN, toolName string) error {
	gatewayID := extractResourceID(gatewayARN, "gateway")
	if gatewayID == "" {
		return fmt.Errorf("DeleteGatewayTarget %q: no gateway id in ARN %q", toolName, gatewayARN)
	}
	targetName := sanitizeGatewayTargetName(toolName)

	var nextToken *string
	for {
		out, err := c.client.ListGatewayTargets(ctx, &bedrockagentcorecontrol.ListGatewayTargetsInput{
			GatewayIdentifier: aws.String(gatewayID),
			MaxResults:        aws.Int32(listPageSize),
			NextToken:         nextToken,
		})
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("ListGatewayTargets on gateway %q: %w", gatewayID, err)
		}
		for _, t := range out.Items {
			if aws.ToString(t.Name) == targetName {
				return c.deleteGatewayTargetBatch(ctx, gatewayID, []types.TargetSummary{t})
			}
		}
		if out.NextToken == nil {
			return nil
		}
		nextToken = out.NextToken
	}
}

// purgeAllGatewayTargets lists and deletes every target within a gateway,
// then waits for all deletions to complete before returning.
func (c *realAWSClient) purgeAllGatewayTargets(ctx context.Context, gatewayID string) error {
	var nextToken *string
	for {
		out, err := c.client.ListGatewayTargets(ctx, &bedrockagentcorecontrol.ListGatewayTargetsInput{
			GatewayIdentifier: aws.String(gatewayID),
			MaxResults:        aws.Int32(listPageSize),
			NextToken:         nextToken,
		})
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("ListGatewayTargets on gateway %q: %w", gatewayID, err)
		}
		if err := c.deleteGatewayTargetBatch(ctx, gatewayID, out.Items); err != nil {
			return err
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	// Target deletion is async — wait until all targets are fully removed
	// before returning, so the caller can safely delete the gateway.
	return c.waitForGatewayTargetsDrained(ctx, gatewayID)
}

// waitForGatewayTargetsDrained polls ListGatewayTargets until no targets
// remain (all DELETING targets have been fully removed).
func (c *realAWSClient) waitForGatewayTargetsDrained(ctx context.Context, gatewayID string) error {
	for range maxPollAttempts {
		out, err := c.client.ListGatewayTargets(ctx, &bedrockagentcorecontrol.ListGatewayTargetsInput{
			GatewayIdentifier: aws.String(gatewayID),
			MaxResults:        aws.Int32(listPageSize),
		})
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("polling gateway targets on %q: %w", gatewayID, err)
		}
		if len(out.Items) == 0 {
			return nil
		}
		log.Printf("agentcore: waiting for %d gateway target(s) to be deleted on %s", len(out.Items), gatewayID)
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("gateway %q still has targets after %d poll attempts", gatewayID, maxPollAttempts)
}

// deleteGatewayTargetBatch waits for targets to leave CREATING state, then
// deletes them. AWS rejects deletion of targets that are still being created.
func (c *realAWSClient) deleteGatewayTargetBatch(
	ctx context.Context, gatewayID string, targets []types.TargetSummary,
) error {
	for _, t := range targets {
		targetID := aws.ToString(t.TargetId)
		if targetID == "" {
			continue
		}
		if err := c.waitForTargetDeletable(ctx, gatewayID, targetID); err != nil {
			log.Printf("agentcore: target %s not deletable, attempting delete anyway: %v", targetID, err)
		}
		_, err := c.client.DeleteGatewayTarget(ctx, &bedrockagentcorecontrol.DeleteGatewayTargetInput{
			GatewayIdentifier: aws.String(gatewayID),
			TargetId:          aws.String(targetID),
		})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("DeleteGatewayTarget %q on gateway %q: %w", targetID, gatewayID, err)
		}
		log.Printf("agentcore: deleted gateway target %s from gateway %s", targetID, gatewayID)
	}
	return nil
}

// waitForTargetDeletable polls GetGatewayTarget until the target leaves
// CREATING state and can be safely deleted.
func (c *realAWSClient) waitForTargetDeletable(
	ctx context.Context, gatewayID, targetID string,
) error {
	for range maxPollAttempts {
		out, err := c.client.GetGatewayTarget(ctx, &bedrockagentcorecontrol.GetGatewayTargetInput{
			GatewayIdentifier: aws.String(gatewayID),
			TargetId:          aws.String(targetID),
		})
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("polling target %q: %w", targetID, err)
		}
		if out.Status != types.TargetStatusCreating {
			return nil
		}
		log.Printf("agentcore: target %s still CREATING, waiting", targetID)
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("target %q did not leave CREATING after %d attempts", targetID, maxPollAttempts)
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

	// Retry deletion — gateway association auto-generates policies that are
	// invisible to ListPolicies but block engine deletion. These are cleaned
	// up asynchronously after the gateway is deleted.
	return c.deletePolicyEngineWithRetry(ctx, engineID, res.Name)
}

// policyEngineDeleteRetries is the number of retry attempts for deleting a
// policy engine when auto-generated policies are still being cleaned up.
const policyEngineDeleteRetries = 12

// deletePolicyEngineWithRetry attempts to delete a policy engine, retrying
// on ConflictException (auto-generated policies still being removed).
func (c *realAWSClient) deletePolicyEngineWithRetry(
	ctx context.Context, engineID, name string,
) error {
	for range policyEngineDeleteRetries {
		_, err := c.client.DeletePolicyEngine(ctx, &bedrockagentcorecontrol.DeletePolicyEngineInput{
			PolicyEngineId: aws.String(engineID),
		})
		if err == nil || isNotFound(err) {
			return nil
		}
		if !isConflictError(err) {
			return fmt.Errorf("DeletePolicyEngine %q: %w", name, err)
		}
		log.Printf("agentcore: policy engine %q still has policies being cleaned up, retrying", engineID)
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("DeletePolicyEngine %q: still has policies after %d retries", name, policyEngineDeleteRetries)
}

// purgeAllPolicies lists and deletes every policy within a policy engine,
// paginating through all results.
func (c *realAWSClient) purgeAllPolicies(ctx context.Context, engineID string) error {
	var nextToken *string
	for {
		out, err := c.client.ListPolicies(ctx, &bedrockagentcorecontrol.ListPoliciesInput{
			PolicyEngineId: aws.String(engineID),
			MaxResults:     aws.Int32(listPageSize),
			NextToken:      nextToken,
		})
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("ListPolicies on engine %q: %w", engineID, err)
		}
		if err := c.deletePolicyBatch(ctx, engineID, out.Policies); err != nil {
			return err
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return nil
}

// deletePolicyBatch deletes a batch of policies from a policy engine.
func (c *realAWSClient) deletePolicyBatch(
	ctx context.Context, engineID string, policies []types.Policy,
) error {
	for _, p := range policies {
		policyID := aws.ToString(p.PolicyId)
		if policyID == "" {
			continue
		}
		_, err := c.client.DeletePolicy(ctx, &bedrockagentcorecontrol.DeletePolicyInput{
			PolicyEngineId: aws.String(engineID),
			PolicyId:       aws.String(policyID),
		})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("DeletePolicy %q on engine %q: %w", policyID, engineID, err)
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
	return c.deleteCreatedLogGroup(ctx, onlineEvalLogGroup(res, c.cfg), res.Name)
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
	if out.Memory != nil && out.Memory.Status == types.MemoryStatusDeleting {
		return StatusMissing, nil
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
	// A deleting resource still answers Get for a while after Destroy returns.
	// Reporting it as present makes Plan offer an UPDATE that Apply cannot
	// perform against a resource on its way out, so it counts as absent: the
	// honest plan is to create it again.
	if out.Status == types.AgentRuntimeStatusDeleting {
		return StatusMissing, nil
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

	// Check the engine is active.
	engineOut, err := c.client.GetPolicyEngine(ctx, &bedrockagentcorecontrol.GetPolicyEngineInput{
		PolicyEngineId: aws.String(engineID),
	})
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetPolicyEngine %q: %w", res.Name, err)
	}
	if engineOut.Status != types.PolicyEngineStatusActive {
		return StatusUnhealthy, nil
	}

	// Check the policy itself.
	policyID := res.Metadata["policy_id"]
	if policyID == "" {
		return StatusHealthy, nil // no individual policy to check
	}
	policyOut, err := c.client.GetPolicy(ctx, &bedrockagentcorecontrol.GetPolicyInput{
		PolicyEngineId: aws.String(engineID),
		PolicyId:       aws.String(policyID),
	})
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, fmt.Errorf("GetPolicy %q: %w", res.Name, err)
	}
	if policyOut.Status == types.PolicyStatusActive {
		return StatusHealthy, nil
	}
	reasons := strings.Join(policyOut.StatusReasons, "; ")
	return StatusUnhealthy, fmt.Errorf("policy %q status %s: %s", res.Name, policyOut.Status, reasons)
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
	for range maxPollAttempts {
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
	for range maxPollAttempts {
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
