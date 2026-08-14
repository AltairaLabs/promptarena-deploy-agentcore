package agentcore

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
)

// checkEvalModels reports llm_as_judge evals whose Bedrock model is not
// available in the target region.
//
// available maps model IDs that the region offers. An empty or nil map means
// availability could not be determined — a failed lookup is not evidence a
// model is missing — and the check is skipped rather than blocking a deploy
// that would otherwise succeed.
//
// Errors are sorted by eval name so the message is stable across runs.
func checkEvalModels(defs map[string]evals.EvalDef, available map[string]bool, region string) []string {
	if len(defs) == 0 || len(available) == 0 {
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
		modelID := evalParamString(def.Params, "model", defaultEvalModel)
		if available[modelID] {
			continue
		}
		errs = append(errs, fmt.Sprintf(
			"eval %q uses Bedrock model %q, which is not available in region %s",
			name, modelID, region))
	}
	return errs
}

// preflightEvalModels checks eval models against the region before Apply
// creates anything.
//
// Without it a bad model surfaces from CreateEvaluator in the second-to-last
// phase, after the memory, gateway and agent runtimes already exist — several
// minutes in, with a teardown to follow. Checking here costs one API call.
//
// A lookup failure (no permission, API error) is logged and ignored: the check
// is an early-warning convenience, never a new reason a deploy cannot run.
func preflightEvalModels(ctx context.Context, client awsClient, cfg *Config) []string {
	if len(cfg.EvalDefs) == 0 {
		return nil
	}

	available, err := client.ListAvailableModels(ctx)
	if err != nil {
		log.Printf("agentcore: could not check eval model availability in %s (%v) — "+
			"continuing; an unavailable model will surface at evaluator creation", cfg.Region, err)
		return nil
	}
	return checkEvalModels(cfg.EvalDefs, available, cfg.Region)
}
