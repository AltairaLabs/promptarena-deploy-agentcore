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
	A2AAuth        *A2AAuthConfig       `json:"a2a_auth,omitempty"`

	// RuntimeEnvVars is populated at apply-time from config fields.
	// It is NOT serialized — it is a transient, computed field.
	RuntimeEnvVars map[string]string `json:"-"`
}

// A2AAuthConfig holds A2A authentication settings.
type A2AAuthConfig struct {
	Mode         string   `json:"mode"`                       // "iam" or "jwt"
	DiscoveryURL string   `json:"discovery_url,omitempty"`    // required for jwt
	AllowedAud   []string `json:"allowed_audience,omitempty"` // JWT audiences
	AllowedClts  []string `json:"allowed_clients,omitempty"`  // JWT client IDs
}

// A2A auth mode constants.
const (
	A2AAuthModeIAM = "iam"
	A2AAuthModeJWT = "jwt"
)

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

	errs = append(errs, validateA2AAuth(c.A2AAuth)...)

	return errs
}

// validateA2AAuth checks A2A auth configuration.
func validateA2AAuth(auth *A2AAuthConfig) []string {
	if auth == nil {
		return nil
	}
	var errs []string
	switch auth.Mode {
	case A2AAuthModeIAM:
		// IAM mode requires no extra fields.
	case A2AAuthModeJWT:
		if auth.DiscoveryURL == "" {
			errs = append(errs, "a2a_auth.discovery_url is required when mode is \"jwt\"")
		}
	case "":
		errs = append(errs, "a2a_auth.mode is required (\"iam\" or \"jwt\")")
	default:
		errs = append(errs, fmt.Sprintf("a2a_auth.mode %q must be \"iam\" or \"jwt\"", auth.Mode))
	}
	return errs
}
