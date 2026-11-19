package agentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/AltairaLabs/promptarena/v2/deploy"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// envPrefixAdapter is the namespace the adapter owns on the runtime's
// environment. envPrefixAWS is the platform's: AgentCore and the AWS SDKs read
// AWS_* names, and the adapter sets AWS_REGION itself.
const (
	envPrefixAdapter = "PROMPTPACK_"
	envPrefixAWS     = "AWS_"
)

// maxEnvValueLen is the longest value CreateAgentRuntime accepts for one
// environment variable. A pack longer than this cannot ride inline in
// PROMPTPACK_PACK_JSON and is staged to S3 instead.
const maxEnvValueLen = 5000

// maxEnvVars is the most environment variables CreateAgentRuntime accepts.
// maxAdapterEnvVars is the most the adapter sets itself: every Env* name in
// envvars.go and toolenv.go, AWS_REGION, and the pack handoff. An override may
// add the rest.
const (
	maxEnvVars        = 50
	maxAdapterEnvVars = 21
)

// overridePackFile is the S3 object name a pack too large to pass inline is
// staged under, beside where the code package would go.
const overridePackFile = "pack.json"

// runtimeOverrideSupport is what AgentCore can apply of a runtime override.
// Its container artifact carries an image URI and nothing else, so the image's
// own entrypoint always runs, and its environment takes plain values only.
var runtimeOverrideSupport = adaptersdk.RuntimeOverrideSupport{
	ReservedEnvPrefixes: []string{envPrefixAdapter, envPrefixAWS},
	// Set from observability.otlp_endpoint.
	ReservedEnvNames: []string{EnvOTLPEndpoint},
}

// ecrImageRE is the pattern CreateAgentRuntime holds a container URI to: a
// private ECR repository (<account>.dkr.ecr.<region>.amazonaws.com/<repo>) or
// ECR Public (public.ecr.aws/<repo>), with an optional tag or digest. AgentCore
// pulls from nowhere else.
var ecrImageRE = regexp.MustCompile(
	`^(([0-9]{12})\.dkr\.ecr\.([a-z0-9-]+)\.([a-z0-9-]+(?:\.[a-z0-9-]+)+)|public\.ecr\.aws)/` +
		`((?:[a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*)(?::([^:@]{1,300}))?(?:@(.+))?$`)

// overrideImage returns the image runtime_override names, or "" when the
// adapter deploys its own runtime.
func (c *Config) overrideImage() string {
	if c.RuntimeOverride == nil {
		return ""
	}
	return c.RuntimeOverride.Image
}

// usesContainerImage reports whether the runtime deploys as a container image
// rather than as the code package built from runtime_binary_path.
func (c *Config) usesContainerImage() bool {
	return c.overrideImage() != ""
}

// validateRuntimeOverride checks the override against what AgentCore can
// apply. AgentCore pulls images from ECR only and caps each environment value
// at maxEnvValueLen, and both are refused here rather than at create time,
// after other resources already exist.
func validateRuntimeOverride(o *deploy.RuntimeOverride) []string {
	if o == nil {
		return nil
	}
	const key = deploy.RuntimeOverrideConfigKey
	// secret_env is refused by the SDK check: AgentCore's environment takes
	// plain values only, with no Secrets Manager reference.
	errs := adaptersdk.ValidateRuntimeOverride(o, runtimeOverrideSupport)
	if o.Image != "" && !ecrImageRE.MatchString(o.Image) {
		errs = append(errs, fmt.Sprintf(
			"%s.image %q is not an Amazon ECR image: AgentCore pulls from ECR only, so copy the "+
				"image into an ECR repository and name it as <account>.dkr.ecr.<region>.amazonaws.com/<repo>:<tag> "+
				"(or public.ecr.aws/<repo>:<tag>)",
			key, o.Image))
	}
	if limit := maxEnvVars - maxAdapterEnvVars; len(o.Env) > limit {
		errs = append(errs, fmt.Sprintf(
			"%s.env sets %d variables: AgentCore accepts %d in all and the adapter may set %d, so at most %d",
			key, len(o.Env), maxEnvVars, maxAdapterEnvVars, limit))
	}
	for _, name := range sortedStringKeys(o.Env) {
		errs = append(errs, checkEnvValue(name, o.Env[name])...)
	}
	return errs
}

// checkEnvValue refuses a value CreateAgentRuntime would: one longer than
// maxEnvValueLen, or one holding a control character (0x00-0x1F, 0x7F).
func checkEnvValue(name, value string) []string {
	const key = deploy.RuntimeOverrideConfigKey
	if len(value) > maxEnvValueLen {
		return []string{fmt.Sprintf("%s.env %q is %d characters: AgentCore accepts at most %d",
			key, name, len(value), maxEnvValueLen)}
	}
	if strings.ContainsFunc(value, isControlChar) {
		return []string{fmt.Sprintf("%s.env %q contains a control character, which AgentCore refuses "+
			"(write JSON values on one line)", key, name)}
	}
	return nil
}

// isControlChar reports the characters AgentCore refuses in an environment value.
func isControlChar(r rune) bool {
	const del = 0x7F
	return r < ' ' || r == del
}

// applyRuntimeOverrideEnv adds the override's variables to the runtime
// environment. Their names were validated against the adapter's reserved
// names, so none displaces a variable the adapter set.
func applyRuntimeOverrideEnv(env map[string]string, o *deploy.RuntimeOverride) {
	if o == nil {
		return
	}
	for name, value := range o.Env {
		env[name] = value
	}
}

// stageOverridePack hands the pack to a replacement container under the
// deploy SDK's names: inline in PROMPTPACK_PACK_JSON when it fits one
// environment value, otherwise uploaded beside the code package and named by
// PROMPTPACK_PACK_URI. The runtime role needs s3:GetObject on it to read it.
func stageOverridePack(ctx context.Context, client awsClient, cfg *Config, packJSON string) error {
	// AgentCore refuses an environment value holding a control character, and
	// a compiled pack is indented JSON. Compacting drops only the whitespace
	// between tokens: a newline inside a JSON string is already escaped.
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(packJSON)); err != nil {
		return fmt.Errorf("compact pack for runtime_override: %w", err)
	}
	packJSON = compact.String()
	if len(packJSON) <= maxEnvValueLen {
		cfg.RuntimeEnvVars[deploy.EnvPackJSON] = packJSON
		return nil
	}
	bucket := codeDeployS3Bucket(extractAccountFromARN(cfg.RuntimeRoleARN), cfg.Region)
	key := codeDeployS3Prefix(cfg.ResourceTags[TagKeyPackID], cfg.ResourceTags[TagKeyVersion]) + overridePackFile
	if err := client.UploadCodePackage(ctx, []byte(packJSON), bucket, key); err != nil {
		return fmt.Errorf("stage pack for runtime_override: %w", err)
	}
	cfg.RuntimeEnvVars[deploy.EnvPackURI] = "s3://" + bucket + "/" + key
	return nil
}

// soleAgentName returns the one prompt in a pack with exactly one, so a
// replacement container is told which agent it serves even when the runtime
// is named after the pack.
func soleAgentName(names map[string]bool) string {
	if len(names) != 1 {
		return ""
	}
	for name := range names {
		return name
	}
	return ""
}

// annotateRuntimeOverride notes the override image on every runtime the plan
// creates or updates, so a plan that switches what runs says so. The adapter
// updates every existing runtime on apply, so the override takes effect on the
// next apply whatever else changed.
func annotateRuntimeOverride(changes []deploy.ResourceChange, cfg *Config) {
	image := cfg.overrideImage()
	if image == "" {
		return
	}
	for i := range changes {
		c := &changes[i]
		if c.Type != ResTypeAgentRuntime {
			continue
		}
		if c.Action != deploy.ActionCreate && c.Action != deploy.ActionUpdate {
			continue
		}
		c.Detail = fmt.Sprintf("%s (runtime_override image %s)", c.Detail, image)
	}
}

// withRuntimeOverride adds the SDK's runtime_override schema to a config
// schema, so the block reads the same in every adapter's schema. Both inputs
// are constants of this package, so a failure is a programming error and
// panics at start-up, where a test sees it.
func withRuntimeOverride(schema string) string {
	var doc map[string]any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		panic("agentcore: config schema is not JSON: " + err.Error())
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		panic("agentcore: config schema has no properties")
	}
	props[deploy.RuntimeOverrideConfigKey] = adaptersdk.RuntimeOverrideSchema()
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic("agentcore: config schema does not marshal: " + err.Error())
	}
	return string(out)
}

// sortedStringKeys returns a map's keys in a stable order.
func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
