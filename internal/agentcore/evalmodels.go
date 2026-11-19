package agentcore

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/v2/evals"
)

// evalModelCatalog is what a region offers an llm_as_judge evaluator.
//
// CreateEvaluator takes either a foundation model that can be invoked on
// demand or an inference profile. Current Claude models are offered only
// through inference profiles: their base ids are listed by ListFoundationModels
// but refused by CreateEvaluator ("on-demand throughput isn't supported"), and
// the profile ids it asks for instead (us.anthropic...) are not foundation
// models at all. Checking against foundation models alone therefore refused
// every model that would have worked and passed one that would not.
type evalModelCatalog struct {
	// onDemand holds foundation model ids and ARNs invocable on demand.
	onDemand map[string]bool
	// profiles holds inference profile ids and ARNs.
	profiles map[string]bool
	// profilesFor maps a foundation model id to the inference profiles that
	// serve it, preferred first (see sortProfiles).
	profilesFor map[string][]string
	// known holds every foundation model id the region lists, however it is
	// invoked.
	known map[string]bool
}

// newEvalModelCatalog returns an empty catalog ready to fill.
func newEvalModelCatalog() *evalModelCatalog {
	return &evalModelCatalog{
		onDemand:    map[string]bool{},
		profiles:    map[string]bool{},
		profilesFor: map[string][]string{},
		known:       map[string]bool{},
	}
}

// addFoundationModel records one foundation model and whether it can be
// invoked on demand.
func (c *evalModelCatalog) addFoundationModel(id, arn string, onDemand bool) {
	if id == "" {
		return
	}
	c.known[id] = true
	if !onDemand {
		return
	}
	c.onDemand[id] = true
	if arn != "" {
		c.onDemand[arn] = true
	}
}

// addProfile records one inference profile and the foundation models it
// serves, given as model ARNs.
func (c *evalModelCatalog) addProfile(id, arn string, modelARNs []string) {
	if id == "" {
		return
	}
	c.profiles[id] = true
	if arn != "" {
		c.profiles[arn] = true
	}
	for _, modelARN := range modelARNs {
		base := foundationModelID(modelARN)
		if base != "" && !slices.Contains(c.profilesFor[base], id) {
			c.profilesFor[base] = append(c.profilesFor[base], id)
		}
	}
}

// finish orders each model's profiles so the preferred one comes first.
func (c *evalModelCatalog) finish() {
	for base := range c.profilesFor {
		sortProfiles(c.profilesFor[base])
	}
}

// empty reports whether the catalog says nothing, which means availability is
// unknown rather than that nothing is available.
func (c *evalModelCatalog) empty() bool {
	return c == nil || (len(c.onDemand) == 0 && len(c.profiles) == 0)
}

// invocable reports whether CreateEvaluator can use model in this region.
func (c *evalModelCatalog) invocable(model string) bool {
	return c.onDemand[model] || c.profiles[model]
}

// pick returns the id to use for a foundation model: the model itself when it
// runs on demand, otherwise its preferred inference profile, or "" when the
// region cannot serve it.
func (c *evalModelCatalog) pick(base string) string {
	if c.onDemand[base] {
		return base
	}
	if profiles := c.profilesFor[base]; len(profiles) > 0 {
		return profiles[0]
	}
	return ""
}

// foundationModelID extracts the model id from a foundation model ARN
// (arn:aws:bedrock:<region>::foundation-model/<id>).
func foundationModelID(arn string) string {
	const marker = "foundation-model/"
	if i := strings.Index(arn, marker); i >= 0 {
		return arn[i+len(marker):]
	}
	return ""
}

// globalProfilePrefix marks a cross-region profile that may route anywhere.
const globalProfilePrefix = "global."

// sortProfiles puts geographic profiles (us., eu., ...) ahead of global ones,
// which may route a request to any region, then sorts by id.
func sortProfiles(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		gi := strings.HasPrefix(ids[i], globalProfilePrefix)
		gj := strings.HasPrefix(ids[j], globalProfilePrefix)
		if gi != gj {
			return !gi
		}
		return ids[i] < ids[j]
	})
}

// defaultEvalModelCandidates are the judge models tried, in order, for an
// llm_as_judge eval that names none. Each is used directly when the region
// runs it on demand, or through its inference profile otherwise.
var defaultEvalModelCandidates = []string{
	"anthropic.claude-sonnet-4-5-20250929-v1:0",
	"anthropic.claude-sonnet-4-20250514-v1:0",
	"anthropic.claude-haiku-4-5-20251001-v1:0",
	"amazon.nova-pro-v1:0",
	"amazon.nova-lite-v1:0",
}

// fallbackEvalModel is the default judge model when the region's models
// could not be listed: the first candidate through the inference profile for
// the region's geography.
//
// It used to be a bare foundation model id, anthropic.claude-sonnet-4-20250514-v1:0,
// which us-east-1 does not list at all — so every llm_as_judge eval without
// params.model was refused there.
func fallbackEvalModel(region string) string {
	prefix := globalProfilePrefix
	switch {
	case strings.HasPrefix(region, "us-"), strings.HasPrefix(region, "ca-"):
		prefix = "us."
	case strings.HasPrefix(region, "eu-"):
		prefix = "eu."
	}
	return prefix + defaultEvalModelCandidates[0]
}

// resolveDefaultEvalModel picks the default judge model for the region: the
// first candidate the catalog can serve, or fallbackEvalModel when it can
// serve none or is unknown.
func resolveDefaultEvalModel(catalog *evalModelCatalog, region string) string {
	if !catalog.empty() {
		for _, base := range defaultEvalModelCandidates {
			if id := catalog.pick(base); id != "" {
				return id
			}
		}
	}
	return fallbackEvalModel(region)
}

// evalModelFor returns the judge model an eval uses.
func evalModelFor(def *evals.EvalDef, defaultModel string) string {
	return evalParamString(def.Params, "model", defaultModel)
}

// checkEvalModels reports llm_as_judge evals whose Bedrock model the target
// region cannot serve to an evaluator.
//
// An empty or nil catalog means availability could not be determined — a
// failed lookup is not evidence a model is missing — and the check is skipped
// rather than blocking a deploy that would otherwise succeed.
//
// Errors are sorted by eval name so the message is stable across runs.
func checkEvalModels(
	defs map[string]evals.EvalDef, catalog *evalModelCatalog, region, defaultModel string,
) []string {
	if len(defs) == 0 || catalog.empty() {
		return nil
	}

	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []string
	for _, name := range names {
		def := defs[name]
		modelID := evalModelFor(&def, defaultModel)
		if catalog.invocable(modelID) {
			continue
		}
		errs = append(errs, evalModelError(name, modelID, catalog, region))
	}
	return errs
}

// evalModelError says why an eval's model cannot be used and, where the
// region offers one, what to use instead.
func evalModelError(evalName, modelID string, catalog *evalModelCatalog, region string) string {
	if profiles := catalog.profilesFor[modelID]; len(profiles) > 0 {
		return fmt.Sprintf(
			"eval %q uses Bedrock model %q, which region %s serves only through an "+
				"inference profile: set params.model to %s",
			evalName, modelID, region, strings.Join(profiles, " or "))
	}
	if catalog.known[modelID] {
		return fmt.Sprintf(
			"eval %q uses Bedrock model %q, which region %s does not offer on demand "+
				"or through an inference profile", evalName, modelID, region)
	}
	return fmt.Sprintf(
		"eval %q uses Bedrock model %q, which is not available in region %s",
		evalName, modelID, region)
}

// preflightEvalModels resolves the default judge model and checks every eval
// model against the region before Apply creates anything.
//
// Without it a bad model surfaces from CreateEvaluator in the second-to-last
// phase, after the memory, gateway and agent runtimes already exist — several
// minutes in, with a teardown to follow. Checking here costs two API calls.
//
// A lookup failure (no permission, API error) is logged and ignored: the check
// is an early-warning convenience, never a new reason a deploy cannot run.
func preflightEvalModels(ctx context.Context, client awsClient, cfg *Config) []string {
	if len(cfg.EvalDefs) == 0 {
		return nil
	}

	catalog, err := client.EvalModelCatalog(ctx)
	if err != nil {
		log.Printf("agentcore: could not check eval model availability in %s (%v) — "+
			"continuing; an unavailable model will surface at evaluator creation", cfg.Region, err)
		catalog = nil
	}
	cfg.DefaultEvalModel = resolveDefaultEvalModel(catalog, cfg.Region)
	return checkEvalModels(cfg.EvalDefs, catalog, cfg.Region, cfg.DefaultEvalModel)
}

// defaultEvalModel returns the resolved default judge model, or the region's
// fallback when none was resolved.
func (c *Config) defaultEvalModel() string {
	if c.DefaultEvalModel != "" {
		return c.DefaultEvalModel
	}
	return fallbackEvalModel(c.Region)
}
