package agentcore

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Config holds AWS Bedrock AgentCore-specific configuration.
type Config struct {
	Region         string               `json:"region"`
	RuntimeRoleARN string               `json:"runtime_role_arn"`
	MemoryStore    string               `json:"memory_store,omitempty"`
	Tools          *ToolsConfig         `json:"tools,omitempty"`
	Observability  *ObservabilityConfig `json:"observability,omitempty"`

	// RuntimeEnvVars is populated at apply-time from config fields.
	// It is NOT serialized — it is a transient, computed field.
	RuntimeEnvVars map[string]string `json:"-"`
}

// ToolsConfig holds tool-related settings for the AgentCore runtime.
type ToolsConfig struct {
	CodeInterpreter bool `json:"code_interpreter,omitempty"`
}

// ObservabilityConfig holds observability settings.
type ObservabilityConfig struct {
	CloudWatchLogGroup string `json:"cloudwatch_log_group,omitempty"`
	TracingEnabled     bool   `json:"tracing_enabled,omitempty"`
}

var (
	regionRE  = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d+$`)
	roleARNRE = regexp.MustCompile(`^arn:aws:iam::\d{12}:role/.+$`)
)

// validMemoryStores lists allowed values for MemoryStore.
var validMemoryStores = map[string]bool{
	"session":    true,
	"persistent": true,
}

// parseConfig unmarshals JSON config into Config.
func parseConfig(raw string) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON: %w", err)
	}
	return &cfg, nil
}

// validate checks the config and returns any validation errors.
func (c *Config) validate() []string {
	var errs []string

	if c.Region == "" {
		errs = append(errs, "region is required")
	} else if !regionRE.MatchString(c.Region) {
		errs = append(errs, fmt.Sprintf("region %q does not match expected format (e.g. us-west-2)", c.Region))
	}

	if c.RuntimeRoleARN == "" {
		errs = append(errs, "runtime_role_arn is required")
	} else if !roleARNRE.MatchString(c.RuntimeRoleARN) {
		errs = append(errs, fmt.Sprintf("runtime_role_arn %q is not a valid IAM role ARN", c.RuntimeRoleARN))
	}

	if c.MemoryStore != "" && !validMemoryStores[c.MemoryStore] {
		errs = append(errs, fmt.Sprintf("memory_store %q must be \"session\" or \"persistent\"", c.MemoryStore))
	}

	return errs
}
