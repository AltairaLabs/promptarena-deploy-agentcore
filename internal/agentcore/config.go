package agentcore

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// Config holds AWS Bedrock AgentCore-specific configuration.
type Config struct {
	Region            string               `json:"region"`
	RuntimeRoleARN    string               `json:"runtime_role_arn"`
	Memory            MemoryConfig         `json:"memory_store,omitempty"`
	RuntimeBinaryPath string               `json:"runtime_binary_path,omitempty"`
	Tags              map[string]string    `json:"tags,omitempty"`
	DryRun            bool                 `json:"dry_run,omitempty"`
	Tools             *ToolsConfig         `json:"tools,omitempty"`
	Observability     *ObservabilityConfig `json:"observability,omitempty"`
	A2AAuth           *A2AAuthConfig       `json:"a2a_auth,omitempty"`

	// ToolTargets maps tool names to provider-specific target config
	// (e.g. lambda_arn). These are merged into ArenaConfig.ToolSpecs
	// so that buildTargetConfig can find Lambda ARNs and other
	// target configuration supplied via the deploy section.
	ToolTargets map[string]*ArenaToolSpec `json:"tool_targets,omitempty"`

	// PackJSON holds the raw pack JSON content to inject as an env var
	// on the runtime container. Populated at apply-time from PlanRequest.
	// NOT serialized — it is a transient, computed field.
	PackJSON string `json:"-"`

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

	// PackTools holds pack tool definitions, populated at apply-time.
	// Used to build inline tool schemas for Lambda gateway targets.
	PackTools map[string]*prompt.PackTool `json:"-"`

	// PromptNames is a set of prompt names from the pack. Used to
	// determine if a runtime name matches an actual prompt (multi-agent)
	// vs the pack ID (single-agent). NOT serialized.
	PromptNames map[string]bool `json:"-"`
}

// Valid memory strategy names.
const (
	StrategyEpisodic       = "episodic"
	StrategySemantic       = "semantic"
	StrategySummary        = "summary"
	StrategyUserPreference = "user_preference"
)

// Legacy alias mappings for backward compatibility.
var legacyStrategyAliases = map[string]string{
	"session":    StrategyEpisodic,
	"persistent": StrategySemantic,
}

// validStrategies lists the canonical strategy names.
var validStrategies = map[string]bool{
	StrategyEpisodic:       true,
	StrategySemantic:       true,
	StrategySummary:        true,
	StrategyUserPreference: true,
}

// Memory event expiry range constants.
const (
	minEventExpiryDays = 3
	maxEventExpiryDays = 365
)

// arnRE matches an AWS ARN prefix.
var arnRE = regexp.MustCompile(`^arn:aws:[a-z0-9-]+:[a-z0-9-]*:\d{12}:.+$`)

// MemoryConfig holds memory configuration for the deployment.
type MemoryConfig struct {
	Strategies       []string `json:"strategies"`
	EventExpiryDays  int32    `json:"event_expiry_days,omitempty"`
	EncryptionKeyARN string   `json:"encryption_key_arn,omitempty"`
}

// HasMemory returns true if any memory strategies are configured.
func (c *Config) HasMemory() bool {
	return len(c.Memory.Strategies) > 0
}

// MemoryStrategiesCSV returns the configured strategies as a
// comma-separated string, suitable for environment variable injection.
func (c *Config) MemoryStrategiesCSV() string {
	return strings.Join(c.Memory.Strategies, ",")
}

// UnmarshalJSON implements custom JSON unmarshalling for Config to handle
// the polymorphic memory_store field (string, array, or object).
func (c *Config) UnmarshalJSON(data []byte) error {
	type configAlias Config
	type rawConfig struct {
		configAlias
		RawMemory json.RawMessage `json:"memory_store,omitempty"`
	}

	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = Config(raw.configAlias)

	if len(raw.RawMemory) == 0 || string(raw.RawMemory) == "null" {
		return nil
	}

	mem, err := parseMemoryField(raw.RawMemory)
	if err != nil {
		return fmt.Errorf("memory_store: %w", err)
	}
	c.Memory = mem
	return nil
}

// parseMemoryField handles the three forms of memory_store.
func parseMemoryField(data json.RawMessage) (MemoryConfig, error) {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return parseMemoryString(s)
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		normalized, nErr := normalizeStrategies(arr)
		if nErr != nil {
			return MemoryConfig{}, nErr
		}
		return MemoryConfig{Strategies: normalized}, nil
	}

	var mc MemoryConfig
	if err := json.Unmarshal(data, &mc); err != nil {
		return MemoryConfig{}, fmt.Errorf("must be a string, array, or object")
	}
	normalized, err := normalizeStrategies(mc.Strategies)
	if err != nil {
		return MemoryConfig{}, err
	}
	mc.Strategies = normalized
	return mc, nil
}

// parseMemoryString converts a strategy string into a MemoryConfig.
func parseMemoryString(s string) (MemoryConfig, error) {
	if s == "" {
		return MemoryConfig{}, nil
	}
	canonical := resolveAlias(s)
	if !validStrategies[canonical] {
		return MemoryConfig{}, fmt.Errorf("invalid strategy %q", s)
	}
	return MemoryConfig{Strategies: []string{canonical}}, nil
}

// normalizeStrategies resolves aliases and deduplicates strategies.
func normalizeStrategies(strategies []string) ([]string, error) {
	seen := make(map[string]bool, len(strategies))
	result := make([]string, 0, len(strategies))
	for _, s := range strategies {
		canonical := resolveAlias(s)
		if !validStrategies[canonical] {
			return nil, fmt.Errorf("invalid strategy %q", s)
		}
		if !seen[canonical] {
			seen[canonical] = true
			result = append(result, canonical)
		}
	}
	return result, nil
}

// resolveAlias maps a legacy alias to its canonical name.
func resolveAlias(s string) string {
	if canonical, ok := legacyStrategyAliases[s]; ok {
		return canonical
	}
	return s
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

	if c.RuntimeBinaryPath == "" {
		errs = append(errs,
			"runtime_binary_path is required (path to pre-compiled Go runtime binary)")
	}

	errs = append(errs, validateMemory(&c.Memory)...)
	errs = append(errs, validateA2AAuth(c.A2AAuth)...)
	errs = append(errs, validateTags(c.Tags)...)

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

// validateMemory checks the memory configuration for errors.
func validateMemory(m *MemoryConfig) []string {
	if len(m.Strategies) == 0 {
		return nil
	}
	var errs []string
	for _, s := range m.Strategies {
		if !validStrategies[s] {
			errs = append(errs, fmt.Sprintf("memory_store: invalid strategy %q", s))
		}
	}
	if m.EventExpiryDays != 0 {
		if m.EventExpiryDays < minEventExpiryDays || m.EventExpiryDays > maxEventExpiryDays {
			errs = append(errs, fmt.Sprintf(
				"memory_store: event_expiry_days %d must be between %d and %d",
				m.EventExpiryDays, minEventExpiryDays, maxEventExpiryDays))
		}
	}
	if m.EncryptionKeyARN != "" && !arnRE.MatchString(m.EncryptionKeyARN) {
		errs = append(errs, fmt.Sprintf(
			"memory_store: encryption_key_arn %q is not a valid ARN", m.EncryptionKeyARN))
	}
	return errs
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
