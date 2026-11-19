package agentcore

import (
	"context"
	"fmt"
	"log"

	"github.com/AltairaLabs/PromptKit/runtime/v2/prompt"
)

// defaultTraceLogGroup is the CloudWatch log group where AgentCore runtimes
// emit OTEL traces via Transaction Search. Online eval configs read from this
// group to evaluate agent interactions.
//
// AWS creates it when CloudWatch Transaction Search is enabled, and it cannot
// be created by hand, so a region without Transaction Search has no such group
// (see preflightEvalLogGroup).
const defaultTraceLogGroup = "aws/spans"

// TagKeyOnlineEvalConfig marks a log group apply created for an online eval
// config. Its value is the config's name, so destroy deletes only the group it
// created for that config.
const TagKeyOnlineEvalConfig = "promptpack:online-eval-config"

// metaLogGroup is the online_eval_config state metadata key that records the
// log group the config reads, so destroy finds it even if the deploy config
// has changed since.
const metaLogGroup = "log_group"

// resolveLogGroup returns the CloudWatch log group for online eval config.
// Defaults to "aws/spans" (the Transaction Search spans log group) which is
// where OTEL-instrumented AgentCore runtimes write their traces.
func resolveLogGroup(cfg *Config) string {
	if cfg != nil && cfg.Observability != nil && cfg.Observability.CloudWatchLogGroup != "" {
		return cfg.Observability.CloudWatchLogGroup
	}
	return defaultTraceLogGroup
}

// onlineEvalLogGroup is the log group an online eval config in state reads:
// the one apply recorded, or, for state written before it was recorded, the
// one the deploy config names.
func onlineEvalLogGroup(res ResourceState, cfg *Config) string {
	if lg := res.Metadata[metaLogGroup]; lg != "" {
		return lg
	}
	return resolveLogGroup(cfg)
}

// createdLogGroupTags are the tags of a log group apply creates for the online
// eval config oecName: the pack's resource tags and the creation marker.
func createdLogGroupTags(resourceTags map[string]string, oecName string) map[string]string {
	tags := make(map[string]string, len(resourceTags)+1)
	for k, v := range resourceTags {
		tags[k] = v
	}
	tags[TagKeyOnlineEvalConfig] = oecName
	return tags
}

// createdForOnlineEvalConfig reports whether a log group's tags say apply
// created it for the online eval config oecName.
func createdForOnlineEvalConfig(tags map[string]string, oecName string) bool {
	return oecName != "" && tags[TagKeyOnlineEvalConfig] == oecName
}

// hasOnlineEval reports whether the pack gets an online eval config: any
// llm_as_judge or builtin eval.
func hasOnlineEval(pack *prompt.Pack) bool {
	for i := range pack.Evals {
		if t := pack.Evals[i].Type; t == evalTypeLLMAsJudge || t == evalTypeBuiltin {
			return true
		}
	}
	return false
}

// recordOnlineEvalLogGroup notes in each online eval config's state the log
// group it reads.
func recordOnlineEvalLogGroup(resources []ResourceState, cfg *Config) {
	for i := range resources {
		r := &resources[i]
		if r.Type != ResTypeOnlineEvalConfig || r.Status == ResStatusFailed {
			continue
		}
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		r.Metadata[metaLogGroup] = resolveLogGroup(cfg)
	}
}

// preflightEvalLogGroup refuses an apply whose online eval config would read
// the default aws/spans log group in a region that does not have it.
//
// Without it CreateOnlineEvaluationConfig fails with "One or more specified
// log groups do not exist" in the last phase, after every other resource is
// live. A configured cloudwatch_log_group is not checked: apply creates it.
//
// A lookup failure is logged and ignored, like the eval model pre-flight.
func preflightEvalLogGroup(ctx context.Context, client awsClient, cfg *Config, pack *prompt.Pack) error {
	if !hasOnlineEval(pack) || resolveLogGroup(cfg) != defaultTraceLogGroup {
		return nil
	}
	exists, err := client.LogGroupExists(ctx, defaultTraceLogGroup)
	if err != nil {
		log.Printf("agentcore: could not check for log group %s in %s (%v) — continuing",
			defaultTraceLogGroup, cfg.Region, err)
		return nil
	}
	if exists {
		return nil
	}
	return fmt.Errorf(
		"online evaluation reads agent spans from the CloudWatch log group %q, which does not "+
			"exist in %s: enable CloudWatch Transaction Search in that region (it creates the group), "+
			"or set observability.cloudwatch_log_group to the log group your runtime's spans go to",
		defaultTraceLogGroup, cfg.Region)
}
