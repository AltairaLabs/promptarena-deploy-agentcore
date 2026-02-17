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
}

func newSimulatedAWSClient(region string) *simulatedAWSClient {
	return &simulatedAWSClient{
		region:    region,
		accountID: "123456789012",
	}
}

func (c *simulatedAWSClient) CreateRuntime(_ context.Context, name string, _ *AgentCoreConfig) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:agent-runtime/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreateGatewayTool(_ context.Context, name string, _ *AgentCoreConfig) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:gateway-tool/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreateA2AWiring(_ context.Context, name string, _ *AgentCoreConfig) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:a2a-wiring/%s", c.region, c.accountID, name), nil
}

func (c *simulatedAWSClient) CreateEvaluator(_ context.Context, name string, _ *AgentCoreConfig) (string, error) {
	return fmt.Sprintf("arn:aws:bedrock:%s:%s:evaluator/%s", c.region, c.accountID, name), nil
}

// simulatedDestroyer is a placeholder that logs intent without calling AWS.
type simulatedDestroyer struct{}

func (s *simulatedDestroyer) DeleteResource(_ context.Context, res ResourceState) error {
	log.Printf("agentcore: simulated delete %s %q (arn=%s)", res.Type, res.Name, res.ARN)
	return nil
}

// simulatedChecker is a placeholder that assumes all existing resources are healthy.
type simulatedChecker struct{}

func (s *simulatedChecker) CheckResource(_ context.Context, res ResourceState) (string, error) {
	log.Printf("agentcore: simulated health check %s %q (arn=%s)", res.Type, res.Name, res.ARN)
	return "healthy", nil
}

// newSimulatedProvider creates an AgentCoreProvider wired with simulated
// (in-memory) clients for unit testing. No AWS credentials are required.
func newSimulatedProvider() *AgentCoreProvider {
	return &AgentCoreProvider{
		awsClientFunc: func(_ context.Context, cfg *AgentCoreConfig) (awsClient, error) {
			return newSimulatedAWSClient(cfg.Region), nil
		},
		destroyerFunc: func(_ context.Context, _ *AgentCoreConfig) (resourceDestroyer, error) {
			return &simulatedDestroyer{}, nil
		},
		checkerFunc: func(_ context.Context, _ *AgentCoreConfig) (resourceChecker, error) {
			return &simulatedChecker{}, nil
		},
	}
}
