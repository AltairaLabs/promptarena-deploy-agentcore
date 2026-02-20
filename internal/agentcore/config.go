package agentcore

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
)

// DefaultContainerImage is empty — the user must provide an ECR URI.
// AgentCore only supports images hosted in Amazon ECR.
const DefaultContainerImage = ""

// Config holds AWS Bedrock AgentCore-specific configuration.
type Config struct {
	Region         string                    `json:"region"`
	RuntimeRoleARN string                    `json:"runtime_role_arn"`
	MemoryStore    string                    `json:"memory_store,omitempty"`
	ContainerImage string                    `json:"container_image,omitempty"`
	Tags           map[string]string         `json:"tags,omitempty"`
	DryRun         bool                      `json:"dry_run,omitempty"`
	Tools          *ToolsConfig              `json:"tools,omitempty"`
	Observability  *ObservabilityConfig      `json:"observability,omitempty"`
	A2AAuth        *A2AAuthConfig            `json:"a2a_auth,omitempty"`
	AgentOverrides map[string]*AgentOverride `json:"agent_overrides,omitempty"`

	// RuntimeEnvVars is populated at apply-time from config fields.
	// It is NOT serialized — it is a transient, computed field.
	RuntimeEnvVars map[string]string `json:"-"`

	// ResourceTags is populated at apply-time by merging default pack
	// metadata tags with user-defined tags. It is NOT serialized.
	ResourceTags map[string]string `json:"-"`

	// EvalDefs is populated at apply-time from pack evals. It maps
	// evaluator resource names to their definitions. NOT serialized.
	EvalDefs map[string]evals.EvalDef `json:"-"`

	// EvalARNs maps evaluator resource names to their ARNs, populated
	// at apply-time after the evaluator phase. NOT serialized.
	EvalARNs map[string]string `json:"-"`

	// BuiltinEvalIDs lists built-in evaluator IDs (e.g. "Builtin.Helpfulness")
	// from the pack. These are passed directly to the online eval config
	// without creating evaluator resources. NOT serialized.
	BuiltinEvalIDs []string `json:"-"`

	// GatewayARN is populated at apply-time after the tool gateway phase.
	// Used by Cedar tool policies that need a specific gateway resource. NOT serialized.
	GatewayARN string `json:"-"`

	// ArenaConfig is the parsed arena configuration, populated from
	// PlanRequest.ArenaConfig. NOT part of the deploy config JSON.
	ArenaConfig *ArenaConfig `json:"-"`
}

// AgentOverride holds per-agent configuration overrides.
type AgentOverride struct {
	ContainerImage string `json:"container_image,omitempty"`
}

// containerImageForAgent returns the container image to use for the given
// agent. It checks agent_overrides[name] first, then the global
// container_image, and finally falls back to DefaultContainerImage.
func (c *Config) containerImageForAgent(name string) string {
	if c.AgentOverrides != nil {
		if ov, ok := c.AgentOverrides[name]; ok && ov.ContainerImage != "" {
			return ov.ContainerImage
		}
	}
	if c.ContainerImage != "" {
		return c.ContainerImage
	}
	return DefaultContainerImage
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
	ecrURIRE  = regexp.MustCompile(`^\d{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/.+$`)
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

	if c.ContainerImage == "" {
		errs = append(errs,
			"container_image is required"+
				" (must be an ECR URI, e.g. 123456789012.dkr.ecr.us-west-2.amazonaws.com/my-repo:latest)")
	} else {
		errs = append(errs, validateContainerImage(c.ContainerImage)...)
	}

	errs = append(errs, validateA2AAuth(c.A2AAuth)...)
	errs = append(errs, validateTags(c.Tags)...)
	for name, ov := range c.AgentOverrides {
		for _, e := range validateContainerImage(ov.ContainerImage) {
			errs = append(errs, fmt.Sprintf("agent_overrides[%s].%s", name, e))
		}
	}

	return errs
}

// maxTagKeyLen is the maximum allowed length for a tag key.
const maxTagKeyLen = 128

// maxTagValueLen is the maximum allowed length for a tag value.
const maxTagValueLen = 256

// maxTagCount is the maximum number of user-defined tags.
const maxTagCount = 50

// validateTags checks user-defined tags for valid keys and values.
func validateTags(tags map[string]string) []string {
	if len(tags) == 0 {
		return nil
	}
	var errs []string
	if len(tags) > maxTagCount {
		errs = append(errs, fmt.Sprintf("tags: at most %d tags allowed, got %d", maxTagCount, len(tags)))
	}
	for k, v := range tags {
		if k == "" {
			errs = append(errs, "tags: key must not be empty")
		}
		if len(k) > maxTagKeyLen {
			errs = append(errs, fmt.Sprintf("tags: key %q exceeds max length %d", k, maxTagKeyLen))
		}
		if len(v) > maxTagValueLen {
			errs = append(errs, fmt.Sprintf("tags: value for key %q exceeds max length %d", k, maxTagValueLen))
		}
	}
	return errs
}

// validateContainerImage checks that a container image reference is valid.
// AgentCore requires ECR URIs in the format:
// {account}.dkr.ecr.{region}.amazonaws.com/{repo}[:{tag}]
func validateContainerImage(image string) []string {
	if image == "" {
		return nil
	}
	if !ecrURIRE.MatchString(image) {
		return []string{fmt.Sprintf(
			"container_image %q is not a valid ECR URI (expected {account}.dkr.ecr.{region}.amazonaws.com/{repo}:{tag})",
			image,
		)}
	}
	return nil
}

// minARNParts is the minimum number of colon-separated segments in a valid ARN
// (arn:partition:service:region:account-id:resource).
const minARNParts = 5

// arnAccountIndex is the zero-based index of the account-id segment in an ARN.
const arnAccountIndex = 4

// extractAccountFromARN extracts the AWS account ID from an ARN string.
// ARN format: arn:partition:service:region:account-id:resource
// Returns an empty string if the ARN is malformed.
func extractAccountFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < minARNParts {
		return ""
	}
	return parts[arnAccountIndex]
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
