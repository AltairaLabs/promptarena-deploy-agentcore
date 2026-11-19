package agentcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// logGroupProvider wires a simulated provider whose client reports the given
// log groups missing, or fails the lookup with lookupErr.
func logGroupProvider(missing map[string]bool, lookupErr error) *Provider {
	p := newSimulatedProvider()
	p.awsClientFunc = func(_ context.Context, cfg *Config) (awsClient, error) {
		c := newSimulatedAWSClient(cfg.Region)
		c.missingLogGroups = missing
		c.logGroupErr = lookupErr
		return c, nil
	}
	return p
}

// configWithLogGroup is validConfig plus observability.cloudwatch_log_group.
func configWithLogGroup(t *testing.T, group string) string {
	t.Helper()
	return fmt.Sprintf(`{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/test",
		"runtime_binary_path":%q,
		"observability":{"cloudwatch_log_group":%q}
	}`, testBinaryPath(t), group)
}

// Without Transaction Search there is no aws/spans group, and
// CreateOnlineEvaluationConfig used to fail on it in the last phase, after
// everything else was live. Apply must refuse before creating anything.
func TestApply_MissingSpansLogGroupFailsBeforeCreatingAnything(t *testing.T) {
	p := logGroupProvider(map[string]bool{defaultTraceLogGroup: true}, nil)

	events, _, err := collectEvents(t, p, applyRequest(t, evalToolPack([]string{"quality"}, nil), ""))
	if err == nil {
		t.Fatal("expected apply to refuse a missing aws/spans log group")
	}
	for _, want := range []string{"aws/spans", "Transaction Search", "cloudwatch_log_group"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	for _, e := range events {
		if e.Resource != nil && e.Resource.Status == ResStatusCreated {
			t.Errorf("nothing should be created when the pre-flight fails, got %+v", e.Resource)
		}
	}
}

func TestApply_SpansLogGroupCheck(t *testing.T) {
	missing := map[string]bool{defaultTraceLogGroup: true}
	cases := map[string]struct {
		provider *Provider
		pack     string
		config   func(*testing.T) string
	}{
		// Apply creates a configured group, so a missing aws/spans is moot.
		"configured log group": {
			logGroupProvider(missing, nil), evalToolPack([]string{"quality"}, nil),
			func(t *testing.T) string { return configWithLogGroup(t, "/custom/evals") },
		},
		// No eval, no online eval config, nothing reads the group.
		"no online eval": {logGroupProvider(missing, nil), evalToolPack(nil, nil), validConfig},
		// A failed lookup is unknown, not missing.
		"lookup fails": {
			logGroupProvider(nil, errors.New("AccessDenied: logs:DescribeLogGroups")),
			evalToolPack([]string{"quality"}, nil), validConfig,
		},
		"group exists": {logGroupProvider(nil, nil), evalToolPack([]string{"quality"}, nil), validConfig},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := applyRequest(t, tc.pack, "")
			req.DeployConfig = tc.config(t)
			if _, _, err := collectEvents(t, tc.provider, req); err != nil {
				t.Fatalf("apply should proceed: %v", err)
			}
		})
	}
}

// Apply records the log group the online eval config reads, so destroy can
// find the group it created even if the deploy config changes.
func TestApply_RecordsTheOnlineEvalLogGroup(t *testing.T) {
	for group, config := range map[string]func(*testing.T) string{
		defaultTraceLogGroup: validConfig,
		"/custom/evals":      func(t *testing.T) string { return configWithLogGroup(t, "/custom/evals") },
	} {
		req := applyRequest(t, evalToolPack([]string{"quality"}, nil), "")
		req.DeployConfig = config(t)
		_, stateStr, err := collectEvents(t, newSimulatedProvider(), req)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		oec, ok := stateByKey(decodeState(t, stateStr))[resourceKey(ResTypeOnlineEvalConfig, "evalpack_online_eval")]
		if !ok {
			t.Fatal("online eval config missing from state")
		}
		if got := oec.Metadata[metaLogGroup]; got != group {
			t.Errorf("recorded log group = %q, want %q", got, group)
		}
	}
}

func TestRecordOnlineEvalLogGroup_SkipsFailedAndOtherTypes(t *testing.T) {
	resources := []ResourceState{
		{Type: ResTypeOnlineEvalConfig, Name: "failed", Status: ResStatusFailed},
		{Type: ResTypeEvaluator, Name: "e", Status: ResStatusCreated},
		{Type: ResTypeOnlineEvalConfig, Name: "ok", Status: ResStatusUpdated,
			Metadata: map[string]string{"other": "kept"}},
	}
	recordOnlineEvalLogGroup(resources, &Config{})
	if resources[0].Metadata != nil || resources[1].Metadata != nil {
		t.Errorf("only a live online eval config is annotated: %+v", resources[:2])
	}
	if resources[2].Metadata[metaLogGroup] != defaultTraceLogGroup || resources[2].Metadata["other"] != "kept" {
		t.Errorf("metadata = %v", resources[2].Metadata)
	}
}

func TestOnlineEvalLogGroup_PrefersStateOverConfig(t *testing.T) {
	cfg := &Config{Observability: &ObservabilityConfig{CloudWatchLogGroup: "/from/config"}}
	recorded := ResourceState{Metadata: map[string]string{metaLogGroup: "/from/state"}}
	if got := onlineEvalLogGroup(recorded, cfg); got != "/from/state" {
		t.Errorf("got %q, want the recorded group", got)
	}
	// State written before the group was recorded falls back to the config.
	if got := onlineEvalLogGroup(ResourceState{}, cfg); got != "/from/config" {
		t.Errorf("got %q, want the configured group", got)
	}
	if got := onlineEvalLogGroup(ResourceState{}, nil); got != defaultTraceLogGroup {
		t.Errorf("got %q, want the default", got)
	}
}

// Destroy deletes only the group apply created for this config: the tag
// carries the config's name, so a group that was there before, or that
// another pack's config created, is left alone.
func TestCreatedLogGroupTags(t *testing.T) {
	base := map[string]string{TagKeyPackID: "evalpack"}
	tags := createdLogGroupTags(base, "evalpack_online_eval")
	if tags[TagKeyPackID] != "evalpack" || tags[TagKeyOnlineEvalConfig] != "evalpack_online_eval" {
		t.Errorf("tags = %v", tags)
	}
	if _, leaked := base[TagKeyOnlineEvalConfig]; leaked {
		t.Error("the pack's resource tags must not be modified")
	}

	if !createdForOnlineEvalConfig(tags, "evalpack_online_eval") {
		t.Error("the group apply created for this config should be deleted")
	}
	for name, tc := range map[string]struct {
		tags map[string]string
		oec  string
	}{
		"pre-existing group":      {map[string]string{"team": "x"}, "evalpack_online_eval"},
		"another pack's group":    {map[string]string{TagKeyOnlineEvalConfig: "other_online_eval"}, "evalpack_online_eval"},
		"no config name in state": {tags, ""},
		"no tags":                 {nil, "evalpack_online_eval"},
	} {
		if createdForOnlineEvalConfig(tc.tags, tc.oec) {
			t.Errorf("%s: must not be deleted", name)
		}
	}
}

func TestHasOnlineEval(t *testing.T) {
	for packJSON, want := range map[string]bool{
		evalToolPack([]string{"quality"}, nil): true,
		evalToolPack(nil, nil):                 false,
		`{"id":"p","prompts":{"d":{"id":"d","system_template":"x"}},"evals":[{"id":"Builtin.Helpfulness","type":"builtin","trigger":"every_turn"}]}`: true,
		`{"id":"p","prompts":{"d":{"id":"d","system_template":"x"}},"evals":[{"id":"x","type":"exact_match","trigger":"every_turn"}]}`:               false,
	} {
		if got := hasOnlineEval(parseTestPack(t, packJSON)); got != want {
			t.Errorf("hasOnlineEval(%s) = %v, want %v", packJSON, got, want)
		}
	}
}

// The apply events still report the online eval config, the resource the
// pre-flight protects.
func TestApply_OnlineEvalConfigCreatedWhenSpansExist(t *testing.T) {
	events, _, err := collectEvents(t, logGroupProvider(nil, nil),
		applyRequest(t, evalToolPack([]string{"quality"}, nil), ""))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, e := range events {
		if e.Resource != nil && e.Resource.Type == ResTypeOnlineEvalConfig &&
			e.Resource.Action == deploy.ActionCreate {
			return
		}
	}
	t.Error("expected the online eval config to be created")
}
