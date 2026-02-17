package agentcore

import (
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func floatPtr(f float64) *float64 { return &f }

func TestBuildMetricsConfig(t *testing.T) {
	tests := []struct {
		name       string
		pack       *prompt.Pack
		wantNil    bool
		wantCount  int // expected number of MetricEntry items
		wantAlarms int // expected number of AlarmEntry items
		wantDims   map[string]string
		checkFunc  func(t *testing.T, mc *MetricsConfig)
	}{
		{
			name: "gauge metric produces correct entry",
			pack: &prompt.Pack{
				ID: "test-pack",
				Evals: []evals.EvalDef{
					{
						ID: "accuracy",
						Metric: &evals.MetricDef{
							Name: "accuracy_score",
							Type: evals.MetricGauge,
						},
					},
				},
			},
			wantCount:  1,
			wantAlarms: 0,
			wantDims:   map[string]string{"pack_id": "test-pack"},
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				m := mc.Metrics[0]
				assertStr(t, "EvalID", m.EvalID, "accuracy")
				assertStr(t, "MetricName", m.MetricName, "accuracy_score")
				assertStr(t, "MetricType", m.MetricType, "gauge")
				assertStr(t, "Unit", m.Unit, unitNone)
			},
		},
		{
			name: "counter with range produces alarm",
			pack: &prompt.Pack{
				ID: "test-pack",
				Evals: []evals.EvalDef{
					{
						ID: "retries",
						Metric: &evals.MetricDef{
							Name: "retry_count",
							Type: evals.MetricCounter,
							Range: &evals.Range{
								Min: floatPtr(0),
								Max: floatPtr(10),
							},
						},
					},
				},
			},
			wantCount:  1,
			wantAlarms: 1,
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				assertStr(t, "Unit", mc.Metrics[0].Unit, unitCount)
				a := mc.Alarms[0]
				assertStr(t, "AlarmMetricName", a.MetricName, "retry_count")
				if a.Min == nil || *a.Min != 0 {
					t.Errorf("alarm Min = %v, want 0", a.Min)
				}
				if a.Max == nil || *a.Max != 10 {
					t.Errorf("alarm Max = %v, want 10", a.Max)
				}
			},
		},
		{
			name: "no evals with metrics returns nil",
			pack: &prompt.Pack{
				ID: "test-pack",
				Evals: []evals.EvalDef{
					{ID: "no-metric"},
				},
			},
			wantNil: true,
		},
		{
			name:    "empty evals returns nil",
			pack:    &prompt.Pack{ID: "test-pack"},
			wantNil: true,
		},
		{
			name: "multi-agent pack includes agent dimension",
			pack: &prompt.Pack{
				ID: "multi-pack",
				Agents: &prompt.AgentsConfig{
					Entry:   "coordinator",
					Members: map[string]*prompt.AgentDef{"coordinator": {}, "worker": {}},
				},
				Evals: []evals.EvalDef{
					{
						ID:     "latency",
						Metric: &evals.MetricDef{Name: "p99_latency", Type: evals.MetricHistogram},
					},
				},
			},
			wantCount: 1,
			wantDims:  map[string]string{"pack_id": "multi-pack", "agent": "multi"},
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				assertStr(t, "Unit", mc.Metrics[0].Unit, unitMilliseconds)
			},
		},
		{
			name: "mixed evals only includes those with metrics",
			pack: &prompt.Pack{
				ID: "mixed-pack",
				Evals: []evals.EvalDef{
					{ID: "eval-no-metric"},
					{
						ID:     "eval-with-metric",
						Metric: &evals.MetricDef{Name: "score", Type: evals.MetricBoolean},
					},
					{ID: "another-no-metric"},
				},
			},
			wantCount: 1,
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				assertStr(t, "EvalID", mc.Metrics[0].EvalID, "eval-with-metric")
				assertStr(t, "Unit", mc.Metrics[0].Unit, unitNone)
			},
		},
		{
			name: "all four metric types map to correct units",
			pack: &prompt.Pack{
				ID: "unit-pack",
				Evals: []evals.EvalDef{
					{ID: "e1", Metric: &evals.MetricDef{Name: "m1", Type: evals.MetricGauge}},
					{ID: "e2", Metric: &evals.MetricDef{Name: "m2", Type: evals.MetricCounter}},
					{ID: "e3", Metric: &evals.MetricDef{Name: "m3", Type: evals.MetricHistogram}},
					{ID: "e4", Metric: &evals.MetricDef{Name: "m4", Type: evals.MetricBoolean}},
				},
			},
			wantCount: 4,
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				wantUnits := []string{unitNone, unitCount, unitMilliseconds, unitNone}
				for i, want := range wantUnits {
					assertStr(t, mc.Metrics[i].MetricName, mc.Metrics[i].Unit, want)
				}
			},
		},
		{
			name: "alarm with only min",
			pack: &prompt.Pack{
				ID: "min-pack",
				Evals: []evals.EvalDef{
					{
						ID: "e1",
						Metric: &evals.MetricDef{
							Name:  "score",
							Type:  evals.MetricGauge,
							Range: &evals.Range{Min: floatPtr(0.5)},
						},
					},
				},
			},
			wantAlarms: 1,
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				a := mc.Alarms[0]
				if a.Min == nil || *a.Min != 0.5 {
					t.Errorf("alarm Min = %v, want 0.5", a.Min)
				}
				if a.Max != nil {
					t.Errorf("alarm Max = %v, want nil", a.Max)
				}
			},
		},
		{
			name: "alarm with only max",
			pack: &prompt.Pack{
				ID: "max-pack",
				Evals: []evals.EvalDef{
					{
						ID: "e1",
						Metric: &evals.MetricDef{
							Name:  "errors",
							Type:  evals.MetricCounter,
							Range: &evals.Range{Max: floatPtr(100)},
						},
					},
				},
			},
			wantAlarms: 1,
			checkFunc: func(t *testing.T, mc *MetricsConfig) {
				t.Helper()
				a := mc.Alarms[0]
				if a.Min != nil {
					t.Errorf("alarm Min = %v, want nil", a.Min)
				}
				if a.Max == nil || *a.Max != 100 {
					t.Errorf("alarm Max = %v, want 100", a.Max)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := buildMetricsConfig(tt.pack)

			if tt.wantNil {
				if mc != nil {
					t.Fatalf("expected nil, got %+v", mc)
				}
				return
			}
			if mc == nil {
				t.Fatal("expected non-nil MetricsConfig")
			}

			assertStr(t, "Namespace", mc.Namespace, metricsNamespace)

			if tt.wantCount > 0 && len(mc.Metrics) != tt.wantCount {
				t.Fatalf("got %d metrics, want %d", len(mc.Metrics), tt.wantCount)
			}
			if tt.wantAlarms > 0 && len(mc.Alarms) != tt.wantAlarms {
				t.Fatalf("got %d alarms, want %d", len(mc.Alarms), tt.wantAlarms)
			}
			if tt.wantDims != nil {
				for k, want := range tt.wantDims {
					got, ok := mc.Dimensions[k]
					if !ok {
						t.Errorf("missing dimension %q", k)
					} else if got != want {
						t.Errorf("dimension %q = %q, want %q", k, got, want)
					}
				}
			}
			if tt.checkFunc != nil {
				tt.checkFunc(t, mc)
			}
		})
	}
}

func TestMetricTypeToUnit(t *testing.T) {
	tests := []struct {
		mt   evals.MetricType
		want string
	}{
		{evals.MetricGauge, unitNone},
		{evals.MetricCounter, unitCount},
		{evals.MetricHistogram, unitMilliseconds},
		{evals.MetricBoolean, unitNone},
		{evals.MetricType("unknown"), unitNone},
	}
	for _, tt := range tests {
		t.Run(string(tt.mt), func(t *testing.T) {
			got := metricTypeToUnit(tt.mt)
			if got != tt.want {
				t.Errorf("metricTypeToUnit(%q) = %q, want %q", tt.mt, got, tt.want)
			}
		})
	}
}

func assertStr(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", field, got, want)
	}
}
