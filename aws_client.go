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

// simulatedAWSClient returns mock ARNs for all operations. It will be
// replaced by a real AWS SDK implementation once the AgentCore APIs are
// available.
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
