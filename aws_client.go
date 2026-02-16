package main

import (
	"context"
	"fmt"
)

// awsClient abstracts AWS AgentCore API calls for testing.
type awsClient interface {
	CreateRuntime(ctx context.Context, name string, cfg *AgentCoreConfig) (arn string, err error)
	CreateGatewayTool(ctx context.Context, name string, cfg *AgentCoreConfig) (arn string, err error)
	CreateA2AWiring(ctx context.Context, name string, cfg *AgentCoreConfig) (arn string, err error)
	CreateEvaluator(ctx context.Context, name string, cfg *AgentCoreConfig) (arn string, err error)
}

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
