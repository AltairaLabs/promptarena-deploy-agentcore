package agentcore

import (
	"context"
	"fmt"
	"log"
)

// simulatedAWSClient returns mock ARNs for all operations.
type simulatedAWSClient struct {
	region    string
	accountID string

	// availableModels overrides the model set returned by
	// ListAvailableModels. Nil means "availability unknown", which is what
	// the real client reports when the lookup fails.
	availableModels map[string]bool
	// listModelsErr, when set, makes ListAvailableModels fail.
	listModelsErr error
}

func newSimulatedAWSClient(region string) *simulatedAWSClient {
	return &simulatedAWSClient{
		region:    region,
		accountID: "123456789012",
	}
}

func (c *simulatedAWSClient) CreateRuntime(_ context.Context, name string, _ *Config) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock-agentcore:%s:%s:runtime/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) UpdateRuntime(_ context.Context, arn string, _ string, _ *Config) (string, error) {
	return arn, nil
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

func (c *simulatedAWSClient) ListAvailableModels(_ context.Context) (map[string]bool, error) {
	if c.listModelsErr != nil {
		return nil, c.listModelsErr
	}
	return c.availableModels, nil
}

// simulatedDestroyer is a placeholder that logs intent without calling AWS.
type simulatedDestroyer struct{}

func (s *simulatedDestroyer) DeleteResource(_ context.Context, res ResourceState) error {
	log.Printf("agentcore: simulated delete %s %q (arn=%s)", res.Type, res.Name, res.ARN)
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
