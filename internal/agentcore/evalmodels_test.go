package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/promptarena/deploy"
)

// packWithJudgeEval returns a pack JSON containing one llm_as_judge eval
// pinned to the given model.
func packWithJudgeEval(model string) string {
	return `{"id":"mypack","version":"v1.0.0",
		"prompts":{"default":{"id":"default","system_template":"hello"}},
		"evals":[{"id":"quality","type":"llm_as_judge","trigger":"every_turn",
		          "params":{"model":"` + model + `","instructions":"judge it"}}]}`
}

// providerWithModels wires a simulated provider whose client reports exactly
// the given models as available.
func providerWithModels(models map[string]bool, listErr error) *Provider {
	p := newSimulatedProvider()
	p.awsClientFunc = func(_ context.Context, cfg *Config) (awsClient, error) {
		c := newSimulatedAWSClient(cfg.Region)
		c.availableModels = models
		c.listModelsErr = listErr
		return c, nil
	}
	return p
}

// Apply must reject an unavailable eval model before creating anything.
// Otherwise it surfaces from CreateEvaluator in the second-to-last phase,
// minutes in, with a memory, gateway and runtime already live.
func TestApply_FailsFastOnUnavailableEvalModel(t *testing.T) {
	p := providerWithModels(map[string]bool{"anthropic.claude-sonnet-4-20250514-v1:0": true}, nil)

	var events []*deploy.ApplyEvent
	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     packWithJudgeEval("anthropic.claude-3-5-haiku-20241022-v1:0"),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
	}, func(e *deploy.ApplyEvent) error {
		events = append(events, e)
		return nil
	})

	if err == nil {
		t.Fatal("expected Apply to reject the unavailable eval model, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic.claude-3-5-haiku-20241022-v1:0") {
		t.Errorf("error %q should name the unavailable model", err)
	}
	for _, e := range events {
		if e.Resource != nil && e.Resource.Status == ResStatusCreated {
			t.Errorf("nothing should be created when the pre-flight fails, got %+v", e.Resource)
		}
	}
}

func TestApply_ProceedsWhenEvalModelAvailable(t *testing.T) {
	model := "anthropic.claude-sonnet-4-20250514-v1:0"
	p := providerWithModels(map[string]bool{model: true}, nil)

	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     packWithJudgeEval(model),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
	}, func(_ *deploy.ApplyEvent) error { return nil })
	if err != nil {
		t.Fatalf("Apply should proceed when the model is available: %v", err)
	}
}

// A failed lookup means unknown, so the deploy must not be blocked by it.
func TestApply_ProceedsWhenModelLookupFails(t *testing.T) {
	p := providerWithModels(nil, errors.New("AccessDenied: bedrock:ListFoundationModels"))

	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     packWithJudgeEval("anything"),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
	}, func(_ *deploy.ApplyEvent) error { return nil })
	if err != nil {
		t.Fatalf("a failed availability lookup must not block the deploy: %v", err)
	}
}

func judgeEval(id, model string) evals.EvalDef {
	def := evals.EvalDef{ID: id, Type: evalTypeLLMAsJudge}
	if model != "" {
		def.Params = map[string]any{"model": model}
	}
	return def
}

func TestCheckEvalModels_UnavailableModelIsReported(t *testing.T) {
	defs := map[string]evals.EvalDef{
		"quality": judgeEval("quality", "anthropic.claude-3-5-haiku-20241022-v1:0"),
	}
	available := map[string]bool{"anthropic.claude-sonnet-4-20250514-v1:0": true}

	errs := checkEvalModels(defs, available, "us-west-2")
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
	joined := errs[0]
	for _, want := range []string{"quality", "anthropic.claude-3-5-haiku-20241022-v1:0", "us-west-2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("error %q should mention %q", joined, want)
		}
	}
}

func TestCheckEvalModels_AvailableModelPasses(t *testing.T) {
	defs := map[string]evals.EvalDef{
		"quality": judgeEval("quality", "anthropic.claude-sonnet-4-20250514-v1:0"),
	}
	available := map[string]bool{"anthropic.claude-sonnet-4-20250514-v1:0": true}

	if errs := checkEvalModels(defs, available, "us-west-2"); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

// An eval with no explicit model uses the adapter's default, which must be
// checked too — otherwise the most common case goes unvalidated.
func TestCheckEvalModels_DefaultModelIsChecked(t *testing.T) {
	defs := map[string]evals.EvalDef{"quality": judgeEval("quality", "")}

	errs := checkEvalModels(defs, map[string]bool{"something-else": true}, "eu-west-1")
	if len(errs) != 1 {
		t.Fatalf("expected the default model to be checked, got %v", errs)
	}
	if !strings.Contains(errs[0], defaultEvalModel) {
		t.Errorf("error %q should name the default model %q", errs[0], defaultEvalModel)
	}
}

// When availability could not be determined the deploy must proceed — a
// failed lookup is not evidence the model is missing.
func TestCheckEvalModels_UnknownAvailabilitySkipsCheck(t *testing.T) {
	defs := map[string]evals.EvalDef{
		"quality": judgeEval("quality", "anthropic.claude-3-5-haiku-20241022-v1:0"),
	}
	if errs := checkEvalModels(defs, nil, "us-west-2"); len(errs) != 0 {
		t.Errorf("nil availability means unknown, not unavailable; got %v", errs)
	}
	if errs := checkEvalModels(defs, map[string]bool{}, "us-west-2"); len(errs) != 0 {
		t.Errorf("empty availability means unknown, not unavailable; got %v", errs)
	}
}

func TestCheckEvalModels_NoEvalsIsFine(t *testing.T) {
	if errs := checkEvalModels(nil, map[string]bool{"x": true}, "us-west-2"); len(errs) != 0 {
		t.Errorf("expected no errors with no evals, got %v", errs)
	}
}

// Several evals sharing one bad model should each be reported, in a stable
// order so the message does not churn between runs.
func TestCheckEvalModels_DeterministicOrdering(t *testing.T) {
	defs := map[string]evals.EvalDef{
		"zeta":  judgeEval("zeta", "missing-model"),
		"alpha": judgeEval("alpha", "missing-model"),
	}
	available := map[string]bool{"other": true}

	first := checkEvalModels(defs, available, "us-west-2")
	if len(first) != 2 {
		t.Fatalf("expected 2 errors, got %v", first)
	}
	if !strings.Contains(first[0], "alpha") {
		t.Errorf("errors should be sorted by eval name, got %v", first)
	}
	for range 20 {
		if got := checkEvalModels(defs, available, "us-west-2"); got[0] != first[0] {
			t.Fatalf("ordering is not deterministic: %v vs %v", got, first)
		}
	}
}
