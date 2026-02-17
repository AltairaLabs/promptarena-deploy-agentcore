package agentcore

import "context"

// awsClient abstracts AWS AgentCore API calls for testing.
type awsClient interface {
	CreateRuntime(ctx context.Context, name string, cfg *Config) (arn string, err error)
	UpdateRuntime(ctx context.Context, arn string, name string, cfg *Config) (string, error)
	CreateGatewayTool(ctx context.Context, name string, cfg *Config) (arn string, err error)
	CreateA2AWiring(ctx context.Context, name string, cfg *Config) (arn string, err error)
	CreateEvaluator(ctx context.Context, name string, cfg *Config) (arn string, err error)
	CreateMemory(ctx context.Context, name string, cfg *Config) (arn string, err error)
}

// resourceDestroyer abstracts resource deletion so that real AWS calls
// can be swapped in later.
type resourceDestroyer interface {
	// DeleteResource simulates (or performs) deletion of a single resource.
	// It returns an error only for unexpected failures; already-deleted
	// resources should return nil.
	DeleteResource(ctx context.Context, res ResourceState) error
}

// resourceChecker abstracts resource health checks.
type resourceChecker interface {
	// CheckResource returns the health status of a single resource.
	// Returns one of "healthy", "unhealthy", or "missing".
	CheckResource(ctx context.Context, res ResourceState) (string, error)
}
