package agentcore

// Resource type constants used across plan, apply, destroy, and status.
const (
	ResTypeMemory       = "memory"
	ResTypeAgentRuntime = "agent_runtime"
	ResTypeToolGateway  = "tool_gateway"
	ResTypeA2AEndpoint  = "a2a_endpoint"
	ResTypeEvaluator    = "evaluator"
	ResTypeCedarPolicy  = "cedar_policy"
)

// Resource lifecycle status constants used in ResourceState.Status.
const (
	ResStatusCreated = "created"
	ResStatusUpdated = "updated"
	ResStatusFailed  = "failed"
	ResStatusPlanned = "planned"
)

// Health status constants returned by resource checks.
const (
	StatusHealthy   = "healthy"
	StatusUnhealthy = "unhealthy"
	StatusMissing   = "missing"
)

// AdapterState holds resource info from previous deploys. It is serialized
// as the opaque "prior_state" string exchanged between Plan, Apply, and Status.
type AdapterState struct {
	Resources  []ResourceState `json:"resources"`
	PackID     string          `json:"pack_id,omitempty"`
	Version    string          `json:"version,omitempty"`
	DeployedAt string          `json:"deployed_at,omitempty"`
}

// ResourceState describes a single deployed resource.
type ResourceState struct {
	Type     string            `json:"type"`
	Name     string            `json:"name"`
	ARN      string            `json:"arn,omitempty"`
	Status   string            `json:"status,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
