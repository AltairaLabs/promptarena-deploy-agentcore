package agentcore

import (
	"context"
	"fmt"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
)

// DataPlaneClient abstracts the AWS Bedrock AgentCore data-plane
// API for testing.
type DataPlaneClient interface {
	CreateEvent(
		ctx context.Context,
		input *bedrockagentcore.CreateEventInput,
		opts ...func(*bedrockagentcore.Options),
	) (*bedrockagentcore.CreateEventOutput, error)

	ListEvents(
		ctx context.Context,
		input *bedrockagentcore.ListEventsInput,
		opts ...func(*bedrockagentcore.Options),
	) (*bedrockagentcore.ListEventsOutput, error)
}

// realDataPlaneClient wraps the bedrockagentcore SDK client.
type realDataPlaneClient struct {
	client *bedrockagentcore.Client
}

// NewDataPlaneClient creates a DataPlaneClient backed by the
// real AWS Bedrock AgentCore data-plane SDK.
func NewDataPlaneClient(
	region string,
) (DataPlaneClient, error) {
	cfg, err := awscfg.LoadDefaultConfig(
		context.Background(),
		awscfg.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"load AWS config for data-plane: %w", err,
		)
	}
	return &realDataPlaneClient{
		client: bedrockagentcore.NewFromConfig(cfg),
	}, nil
}

// CreateEvent delegates to the underlying SDK client.
func (c *realDataPlaneClient) CreateEvent(
	ctx context.Context,
	input *bedrockagentcore.CreateEventInput,
	opts ...func(*bedrockagentcore.Options),
) (*bedrockagentcore.CreateEventOutput, error) {
	return c.client.CreateEvent(ctx, input, opts...)
}

// ListEvents delegates to the underlying SDK client.
func (c *realDataPlaneClient) ListEvents(
	ctx context.Context,
	input *bedrockagentcore.ListEventsInput,
	opts ...func(*bedrockagentcore.Options),
) (*bedrockagentcore.ListEventsOutput, error) {
	return c.client.ListEvents(ctx, input, opts...)
}
