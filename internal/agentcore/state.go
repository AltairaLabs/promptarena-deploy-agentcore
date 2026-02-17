package agentcore

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
