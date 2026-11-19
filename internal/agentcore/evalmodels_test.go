package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/v2/evals"
	"github.com/AltairaLabs/promptarena/v2/deploy"
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

// onDemandCatalog is a catalog offering exactly the given models on demand.
func onDemandCatalog(ids ...string) *evalModelCatalog {
	c := newEvalModelCatalog()
	for _, id := range ids {
		c.addFoundationModel(id, "", true)
	}
	c.finish()
	return c
}

const (
	haiku45Base    = "anthropic.claude-haiku-4-5-20251001-v1:0"
	haiku45US      = "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	haiku45Global  = "global.anthropic.claude-haiku-4-5-20251001-v1:0"
	sonnet45Base   = "anthropic.claude-sonnet-4-5-20250929-v1:0"
	sonnet45US     = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	novaLite       = "amazon.nova-lite-v1:0"
	foundationARNs = "arn:aws:bedrock:us-east-1::foundation-model/"
)

// usEast1Catalog is us-east-1 as the live deploy found it: current Claude
// models listed but invocable only through inference profiles, Claude
// Sonnet 4 not listed at all, and Nova Lite on demand.
func usEast1Catalog() *evalModelCatalog {
	c := newEvalModelCatalog()
	c.addFoundationModel(haiku45Base, foundationARNs+haiku45Base, false)
	c.addFoundationModel(sonnet45Base, foundationARNs+sonnet45Base, false)
	c.addFoundationModel(novaLite, foundationARNs+novaLite, true)
	c.addProfile(haiku45Global, "arn:aws:bedrock:us-east-1:123:inference-profile/"+haiku45Global,
		[]string{foundationARNs + haiku45Base})
	c.addProfile(haiku45US, "arn:aws:bedrock:us-east-1:123:inference-profile/"+haiku45US,
		[]string{foundationARNs + haiku45Base, "arn:aws:bedrock:us-west-2::foundation-model/" + haiku45Base})
	c.addProfile(sonnet45US, "", []string{foundationARNs + sonnet45Base})
	c.finish()
	return c
}

func TestCheckEvalModels_UnavailableModelIsReported(t *testing.T) {
	defs := map[string]evals.EvalDef{
		"quality": judgeEval("quality", "anthropic.claude-3-5-haiku-20241022-v1:0"),
	}
	available := onDemandCatalog("anthropic.claude-sonnet-4-20250514-v1:0")

	errs := checkEvalModels(defs, available, "us-west-2", sonnet45US)
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
	available := onDemandCatalog("anthropic.claude-sonnet-4-20250514-v1:0")

	if errs := checkEvalModels(defs, available, "us-west-2", sonnet45US); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

// The model CreateEvaluator asks for in us-east-1 is an inference profile,
// which the pre-flight used to refuse because it knew only foundation models.
func TestCheckEvalModels_InferenceProfileIsAccepted(t *testing.T) {
	catalog := usEast1Catalog()
	for _, model := range []string{
		haiku45US,
		haiku45Global,
		"arn:aws:bedrock:us-east-1:123:inference-profile/" + haiku45US,
	} {
		defs := map[string]evals.EvalDef{"quality": judgeEval("quality", model)}
		if errs := checkEvalModels(defs, catalog, "us-east-1", sonnet45US); len(errs) != 0 {
			t.Errorf("inference profile %q should be accepted, got %v", model, errs)
		}
	}
}

// A base id the region serves only through a profile passed the pre-flight
// and then failed CreateEvaluator ("on-demand throughput isn't supported").
// It must be refused up front, naming the profile to use instead.
func TestCheckEvalModels_ProfileOnlyBaseIDIsRefusedWithTheProfile(t *testing.T) {
	defs := map[string]evals.EvalDef{"quality": judgeEval("quality", haiku45Base)}

	errs := checkEvalModels(defs, usEast1Catalog(), "us-east-1", sonnet45US)
	if len(errs) != 1 {
		t.Fatalf("expected the base id to be refused, got %v", errs)
	}
	for _, want := range []string{haiku45Base, "inference profile", haiku45US} {
		if !strings.Contains(errs[0], want) {
			t.Errorf("error %q should mention %q", errs[0], want)
		}
	}
	// The geographic profile is suggested ahead of the global one.
	if strings.Index(errs[0], haiku45US) > strings.Index(errs[0], haiku45Global) {
		t.Errorf("error %q should suggest %q first", errs[0], haiku45US)
	}
}

func TestCheckEvalModels_KnownButUninvocableModel(t *testing.T) {
	c := newEvalModelCatalog()
	c.addFoundationModel("provisioned.only-v1:0", "", false)
	c.addFoundationModel(novaLite, "", true)
	defs := map[string]evals.EvalDef{"quality": judgeEval("quality", "provisioned.only-v1:0")}

	errs := checkEvalModels(defs, c, "us-east-1", novaLite)
	if len(errs) != 1 || !strings.Contains(errs[0], "does not offer on demand") {
		t.Fatalf("expected a not-on-demand error, got %v", errs)
	}
}

// An eval with no explicit model uses the adapter's default, which must be
// checked too — otherwise the most common case goes unvalidated.
func TestCheckEvalModels_DefaultModelIsChecked(t *testing.T) {
	defs := map[string]evals.EvalDef{"quality": judgeEval("quality", "")}

	errs := checkEvalModels(defs, onDemandCatalog("something-else"), "eu-west-1", sonnet45US)
	if len(errs) != 1 {
		t.Fatalf("expected the default model to be checked, got %v", errs)
	}
	if !strings.Contains(errs[0], sonnet45US) {
		t.Errorf("error %q should name the default model %q", errs[0], sonnet45US)
	}
}

// When availability could not be determined the deploy must proceed — a
// failed lookup is not evidence the model is missing.
func TestCheckEvalModels_UnknownAvailabilitySkipsCheck(t *testing.T) {
	defs := map[string]evals.EvalDef{
		"quality": judgeEval("quality", "anthropic.claude-3-5-haiku-20241022-v1:0"),
	}
	if errs := checkEvalModels(defs, nil, "us-west-2", sonnet45US); len(errs) != 0 {
		t.Errorf("nil availability means unknown, not unavailable; got %v", errs)
	}
	if errs := checkEvalModels(defs, newEvalModelCatalog(), "us-west-2", sonnet45US); len(errs) != 0 {
		t.Errorf("empty availability means unknown, not unavailable; got %v", errs)
	}
}

func TestCheckEvalModels_NoEvalsIsFine(t *testing.T) {
	if errs := checkEvalModels(nil, onDemandCatalog("x"), "us-west-2", "x"); len(errs) != 0 {
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
	available := onDemandCatalog("other")

	first := checkEvalModels(defs, available, "us-west-2", "other")
	if len(first) != 2 {
		t.Fatalf("expected 2 errors, got %v", first)
	}
	if !strings.Contains(first[0], "alpha") {
		t.Errorf("errors should be sorted by eval name, got %v", first)
	}
	for range 20 {
		if got := checkEvalModels(defs, available, "us-west-2", "other"); got[0] != first[0] {
			t.Fatalf("ordering is not deterministic: %v vs %v", got, first)
		}
	}
}

// The default judge model must be one the region can serve. In us-east-1
// that is a Claude inference profile, not the base id.
func TestResolveDefaultEvalModel_PrefersAServableProfile(t *testing.T) {
	if got := resolveDefaultEvalModel(usEast1Catalog(), "us-east-1"); got != sonnet45US {
		t.Errorf("default = %q, want %q", got, sonnet45US)
	}
}

func TestResolveDefaultEvalModel_PrefersOnDemandBaseID(t *testing.T) {
	c := onDemandCatalog(sonnet45Base, novaLite)
	if got := resolveDefaultEvalModel(c, "us-west-2"); got != sonnet45Base {
		t.Errorf("default = %q, want the on-demand base id %q", got, sonnet45Base)
	}
}

func TestResolveDefaultEvalModel_FallsBackDownTheCandidates(t *testing.T) {
	if got := resolveDefaultEvalModel(onDemandCatalog(novaLite), "ap-south-1"); got != novaLite {
		t.Errorf("default = %q, want %q", got, novaLite)
	}
}

func TestResolveDefaultEvalModel_UnknownCatalogUsesRegionalProfile(t *testing.T) {
	cases := map[string]string{
		"us-east-1":      "us." + sonnet45Base,
		"ca-central-1":   "us." + sonnet45Base,
		"eu-central-1":   "eu." + sonnet45Base,
		"ap-southeast-2": "global." + sonnet45Base,
	}
	for region, want := range cases {
		if got := resolveDefaultEvalModel(nil, region); got != want {
			t.Errorf("%s: default = %q, want %q", region, got, want)
		}
		if got := resolveDefaultEvalModel(onDemandCatalog("unrelated"), region); got != want {
			t.Errorf("%s with no candidate: default = %q, want %q", region, got, want)
		}
	}
}

// The resolved default reaches the evaluator: an eval without params.model
// must be created with the model the pre-flight picked, not a fixed id.
func TestPreflightEvalModels_ResolvesDefaultIntoConfig(t *testing.T) {
	c := newSimulatedAWSClient("us-east-1")
	c.modelCatalog = usEast1Catalog()
	cfg := &Config{Region: "us-east-1", EvalDefs: map[string]evals.EvalDef{
		"pack_quality": judgeEval("quality", ""),
	}}

	if errs := preflightEvalModels(context.Background(), c, cfg); len(errs) != 0 {
		t.Fatalf("pre-flight should pass with a servable default: %v", errs)
	}
	if cfg.DefaultEvalModel != sonnet45US {
		t.Errorf("DefaultEvalModel = %q, want %q", cfg.DefaultEvalModel, sonnet45US)
	}
	def := cfg.EvalDefs["pack_quality"]
	if got := evalModelFor(&def, cfg.defaultEvalModel()); got != sonnet45US {
		t.Errorf("eval model = %q, want %q", got, sonnet45US)
	}
}

func TestPreflightEvalModels_LookupFailureStillSetsDefault(t *testing.T) {
	c := newSimulatedAWSClient("eu-west-1")
	c.listModelsErr = errors.New("AccessDenied")
	cfg := &Config{Region: "eu-west-1", EvalDefs: map[string]evals.EvalDef{"x": judgeEval("x", "")}}

	if errs := preflightEvalModels(context.Background(), c, cfg); len(errs) != 0 {
		t.Fatalf("a failed lookup must not block: %v", errs)
	}
	if want := "eu." + sonnet45Base; cfg.DefaultEvalModel != want {
		t.Errorf("DefaultEvalModel = %q, want %q", cfg.DefaultEvalModel, want)
	}
}

func TestConfigDefaultEvalModel_FallsBackWhenUnresolved(t *testing.T) {
	cfg := &Config{Region: "us-west-2"}
	if got := cfg.defaultEvalModel(); got != "us."+sonnet45Base {
		t.Errorf("defaultEvalModel() = %q", got)
	}
}

func TestFoundationModelID(t *testing.T) {
	if got := foundationModelID(foundationARNs + novaLite); got != novaLite {
		t.Errorf("foundationModelID = %q", got)
	}
	if got := foundationModelID("not-an-arn"); got != "" {
		t.Errorf("foundationModelID(non-ARN) = %q, want empty", got)
	}
}

func TestEvalModelCatalog_IgnoresEmptyIDs(t *testing.T) {
	c := newEvalModelCatalog()
	c.addFoundationModel("", "arn", true)
	c.addProfile("", "arn", nil)
	if !c.empty() {
		t.Error("entries without an id should be ignored")
	}
	var nilCatalog *evalModelCatalog
	if !nilCatalog.empty() {
		t.Error("a nil catalog is empty")
	}
}
