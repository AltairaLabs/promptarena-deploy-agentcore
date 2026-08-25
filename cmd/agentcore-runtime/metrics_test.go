package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
)

// fakeCloudWatch captures what would be published.
type fakeCloudWatch struct {
	puts []*cloudwatch.PutMetricDataInput
	err  error
}

func (f *fakeCloudWatch) PutMetricData(
	_ context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options),
) (*cloudwatch.PutMetricDataOutput, error) {
	f.puts = append(f.puts, in)
	return &cloudwatch.PutMetricDataOutput{}, f.err
}

func testMetricsConfig() *metricsConfig {
	return &metricsConfig{
		Namespace:  "PromptArena/agents",
		Dimensions: map[string]string{"PackID": "support", "Agent": "main"},
		Metrics: []metricEntry{
			{EvalID: "tone", MetricName: "ToneScore"},
		},
	}
}

func ptr(f float64) *float64 { return &f }

// TestCloudWatchRecorder_PublishesAnEvalResult is the whole point: an eval the
// deployed agent already runs becomes something an operator can see.
func TestCloudWatchRecorder_PublishesAnEvalResult(t *testing.T) {
	fake := &fakeCloudWatch{}
	rec := newCloudWatchRecorder(fake, testMetricsConfig(), slog.Default())

	err := rec.Record(evals.EvalResult{EvalID: "tone", Score: ptr(0.75)}, nil)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if len(fake.puts) != 1 {
		t.Fatalf("published %d metrics, want 1", len(fake.puts))
	}
	put := fake.puts[0]
	if *put.Namespace != "PromptArena/agents" {
		t.Errorf("namespace = %q", *put.Namespace)
	}
	datum := put.MetricData[0]
	if *datum.MetricName != "ToneScore" {
		t.Errorf("metric name = %q, want the adapter's mapping", *datum.MetricName)
	}
	if *datum.Value != 0.75 {
		t.Errorf("value = %v, want 0.75", *datum.Value)
	}
	if len(datum.Dimensions) != 2 {
		t.Errorf("dimensions = %d, want 2", len(datum.Dimensions))
	}
}

// TestCloudWatchRecorder_NamesAnEvalNobodyMapped keeps a new eval visible.
//
// Adding an eval to a pack without naming a metric should still publish it,
// under its own id — dropping it would make the pack's newest eval the one
// nobody can see.
func TestCloudWatchRecorder_NamesAnEvalNobodyMapped(t *testing.T) {
	fake := &fakeCloudWatch{}
	rec := newCloudWatchRecorder(fake, testMetricsConfig(), slog.Default())

	if err := rec.Record(evals.EvalResult{EvalID: "unmapped", Score: ptr(1)}, nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(fake.puts) != 1 {
		t.Fatalf("an unmapped eval was dropped")
	}
	if got := *fake.puts[0].MetricData[0].MetricName; got != "unmapped" {
		t.Errorf("metric name = %q, want the eval id", got)
	}
}

// TestCloudWatchRecorder_PrefersThePacksOwnMetricName checks precedence.
func TestCloudWatchRecorder_PrefersThePacksOwnMetricName(t *testing.T) {
	fake := &fakeCloudWatch{}
	rec := newCloudWatchRecorder(fake, testMetricsConfig(), slog.Default())

	err := rec.Record(
		evals.EvalResult{EvalID: "tone", Score: ptr(1)},
		&evals.MetricDef{Name: "FromThePack"},
	)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := *fake.puts[0].MetricData[0].MetricName; got != "FromThePack" {
		t.Errorf("metric name = %q, want the pack's own definition", got)
	}
}

// TestCloudWatchRecorder_SkipsResultsThatAreNotNumbers keeps a missing score
// from being published as a real zero.
func TestCloudWatchRecorder_SkipsResultsThatAreNotNumbers(t *testing.T) {
	fake := &fakeCloudWatch{}
	rec := newCloudWatchRecorder(fake, testMetricsConfig(), slog.Default())

	if err := rec.Record(evals.EvalResult{EvalID: "tone"}, nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(fake.puts) != 0 {
		t.Errorf("a result with no score published %v — zero would read as a "+
			"genuine low score", fake.puts)
	}
}

// TestCloudWatchRecorder_APublishFailureDoesNotFailTheTurn covers the ordering:
// the answer is already sent by the time an eval runs.
func TestCloudWatchRecorder_APublishFailureDoesNotFailTheTurn(t *testing.T) {
	fake := &fakeCloudWatch{err: context.DeadlineExceeded}
	rec := newCloudWatchRecorder(fake, testMetricsConfig(), slog.Default())

	if err := rec.Record(evals.EvalResult{EvalID: "tone", Score: ptr(1)}, nil); err != nil {
		t.Errorf("a metrics failure should not surface as an eval error: %v", err)
	}
}

// TestParseMetricsConfig covers what counts as "nothing to publish".
func TestParseMetricsConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", "", false},
		{"no namespace", `{"metrics":[{"eval_id":"a"}]}`, false},
		{"no metrics", `{"namespace":"n"}`, false},
		{"usable", `{"namespace":"n","metrics":[{"eval_id":"a","metric_name":"A"}]}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseMetricsConfig(tt.raw)
			if err != nil {
				t.Fatalf("parseMetricsConfig: %v", err)
			}
			if (cfg != nil) != tt.want {
				t.Errorf("got config %v, want usable=%v", cfg, tt.want)
			}
		})
	}
}

// TestParseMetricsConfig_ReportsBadJSON names the variable so a malformed
// injection is traceable.
func TestParseMetricsConfig_ReportsBadJSON(t *testing.T) {
	_, err := parseMetricsConfig(`{"namespace":`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), envMetricsConfig) {
		t.Errorf("error should name the variable, got %v", err)
	}
}
