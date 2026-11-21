package agentcore

import (
	"context"
	"fmt"
	"log"
	"strconv"
)

// simulatedAWSClient returns mock ARNs for all operations.
type simulatedAWSClient struct {
	region    string
	accountID string

	// availableModels is the set of on-demand models EvalModelCatalog
	// reports. Nil means "availability unknown", which is what
	// the real client reports when the lookup fails.
	availableModels map[string]bool
	// modelCatalog, when set, is returned by EvalModelCatalog in place of
	// one built from availableModels.
	modelCatalog *evalModelCatalog
	// listModelsErr, when set, makes EvalModelCatalog fail.
	listModelsErr error

	// missingLogGroups names log groups LogGroupExists reports absent; every
	// other group exists.
	missingLogGroups map[string]bool
	// logGroupErr, when set, makes LogGroupExists fail.
	logGroupErr error

	// versions holds each runtime's latest version, keyed by ARN, and
	// endpoints the version each named endpoint serves, keyed by
	// "<runtime ARN>|<endpoint>". Both are created on first use.
	versions  map[string]int
	endpoints map[string]string
	// pointed records each PointRuntimeEndpoint call as "<endpoint>=<version>".
	pointed []string
}

func newSimulatedAWSClient(region string) *simulatedAWSClient {
	return &simulatedAWSClient{
		region:    region,
		accountID: "123456789012",
	}
}

func (c *simulatedAWSClient) CreateRuntime(_ context.Context, name string, _ *Config) (string, error) {
	arn := fmt.Sprintf("arn:aws:bedrock-agentcore:%s:%s:runtime/%s", c.region, c.accountID, name)
	c.bumpVersion(arn)
	return arn, nil
}

func (c *simulatedAWSClient) UpdateRuntime(_ context.Context, arn string, _ string, _ *Config) (string, error) {
	c.bumpVersion(arn)
	return arn, nil
}

func (c *simulatedAWSClient) bumpVersion(arn string) {
	if c.versions == nil {
		c.versions = map[string]int{}
	}
	c.versions[arn]++
}

func (c *simulatedAWSClient) RuntimeVersion(_ context.Context, arn string) (string, error) {
	if c.versions[arn] == 0 {
		return "1", nil
	}
	return strconv.Itoa(c.versions[arn]), nil
}

func (c *simulatedAWSClient) RuntimeEndpointVersion(_ context.Context, arn, endpoint string) (string, error) {
	return c.endpoints[arn+"|"+endpoint], nil
}

func (c *simulatedAWSClient) PointRuntimeEndpoint(_ context.Context, arn, endpoint, version string) (string, error) {
	if c.endpoints == nil {
		c.endpoints = map[string]string{}
	}
	c.endpoints[arn+"|"+endpoint] = version
	c.pointed = append(c.pointed, endpoint+"="+version)
	return arn + "/runtime-endpoint/" + endpoint, nil
}

func (c *simulatedAWSClient) GatewayEndpoint() (url, authorizer string) {
	return fmt.Sprintf("https://gw-sim.gateway.bedrock-agentcore.%s.amazonaws.com", c.region), "AWS_IAM"
}

func (c *simulatedAWSClient) CreateGatewayTool(_ context.Context, name string, _ *Config) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:gateway-tool/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreateA2AWiring(_ context.Context, name string, _ *Config) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:a2a-wiring/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreateEvaluator(_ context.Context, name string, _ *Config) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:evaluator/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreateOnlineEvalConfig(_ context.Context, name string, _ *Config) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:online-evaluation-config/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) UpdateOnlineEvalConfig(_ context.Context, arn string, _ string, _ *Config) (string, error) {
	return arn, nil
}

func (c *simulatedAWSClient) DisassociatePolicyEngine(_ context.Context, _ *Config) error {
	return nil
}

func (c *simulatedAWSClient) CreateMemory(_ context.Context, name string, _ *Config) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:memory/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreatePolicyEngine(
	_ context.Context, name string, _ *Config,
) (string, string, error) {
	arn := fmt.Sprintf("arn:aws:bedrock:%s:%s:policy-engine/%s", c.region, c.accountID, name)
	engineID := "pe-" + name
	return arn, engineID, nil
}

func (c *simulatedAWSClient) AssociatePolicyEngine(
	_ context.Context, _ string, _ *Config,
) error {
	return nil
}

func (c *simulatedAWSClient) CreateCedarPolicy(
	_ context.Context, engineID string, name string, _ string, _ *Config,
) (string, string, error) {
	arn := fmt.Sprintf("arn:aws:bedrock:%s:%s:policy/%s/%s", c.region, c.accountID, engineID, name)
	policyID := "pol-" + name
	return arn, policyID, nil
}

func (c *simulatedAWSClient) UploadCodePackage(
	_ context.Context, _ []byte, _, _ string,
) error {
	log.Printf("agentcore: simulated S3 upload")
	return nil
}

func (c *simulatedAWSClient) EvalModelCatalog(_ context.Context) (*evalModelCatalog, error) {
	if c.listModelsErr != nil {
		return nil, c.listModelsErr
	}
	if c.modelCatalog != nil {
		return c.modelCatalog, nil
	}
	if c.availableModels == nil {
		return nil, nil
	}
	catalog := newEvalModelCatalog()
	for id := range c.availableModels {
		catalog.addFoundationModel(id, "", true)
	}
	return catalog, nil
}

func (c *simulatedAWSClient) LogGroupExists(_ context.Context, name string) (bool, error) {
	if c.logGroupErr != nil {
		return false, c.logGroupErr
	}
	return !c.missingLogGroups[name], nil
}

// simulatedDestroyer is a placeholder that logs intent without calling AWS.
type simulatedDestroyer struct{}

func (s *simulatedDestroyer) DeleteResource(_ context.Context, res ResourceState) error {
	log.Printf("agentcore: simulated delete %s %q (arn=%s)", res.Type, res.Name, res.ARN)
	return nil
}

func (s *simulatedDestroyer) DeleteGatewayTarget(_ context.Context, gatewayARN, toolName string) error {
	log.Printf("agentcore: simulated delete gateway target %q (gateway=%s)", toolName, gatewayARN)
	return nil
}

// simulatedChecker is a placeholder that assumes all existing resources are
// healthy unless a per-resource status or error is configured.
type simulatedChecker struct {
	// statusByName overrides the reported status, keyed by resource name.
	statusByName map[string]string
	// errByName makes CheckResource fail for the named resource.
	errByName map[string]error
	// calls records every resource checked, so tests can assert the checker
	// was (or was not) consulted.
	calls []string
}

func (s *simulatedChecker) CheckResource(_ context.Context, res ResourceState) (string, error) {
	log.Printf("agentcore: simulated health check %s %q (arn=%s)", res.Type, res.Name, res.ARN)
	s.calls = append(s.calls, res.Name)
	if err, ok := s.errByName[res.Name]; ok {
		return "", err
	}
	if status, ok := s.statusByName[res.Name]; ok {
		return status, nil
	}
	return StatusHealthy, nil
}

// newSimulatedProvider creates an Provider wired with simulated
// (in-memory) clients for unit testing. No AWS credentials are required.
func newSimulatedProvider() *Provider {
	return &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return newSimulatedAWSClient(cfg.Region), nil
		},
		destroyerFunc: func(_ context.Context, _ *Config) (resourceDestroyer, error) {
			return &simulatedDestroyer{}, nil
		},
		checkerFunc: func(_ context.Context, _ *Config) (resourceChecker, error) {
			return &simulatedChecker{}, nil
		},
	}
}
