package agentcore

import (
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestBuildDashboardConfig(t *testing.T) {
	tests := []struct {
		name        string
		pack        *prompt.Pack
		region      string
		wantNil     bool
		wantWidgets int
		checkFunc   func(t *testing.T, dc *DashboardConfig)
	}{
		{
			name:        "pack with only ID produces one agent widget",
			pack:        &prompt.Pack{ID: "solo"},
			region:      "us-west-2",
			wantWidgets: 1,
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				assertStr(t, "title", dc.Widgets[0].Properties.Title, "Agent: solo")
			},
		},
		{
			name: "single agent pack produces one agent widget",
			pack: &prompt.Pack{
				ID: "my-agent",
				Prompts: map[string]*prompt.PackPrompt{
					"main": {},
				},
			},
			region:      "us-east-1",
			wantWidgets: 1,
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				w := dc.Widgets[0]
				assertStr(t, "type", w.Type, "metric")
				assertStr(t, "title", w.Properties.Title, "Agent: my-agent")
				assertStr(t, "region", w.Properties.Region, "us-east-1")
				if w.Properties.Period != dashboardPeriod {
					t.Errorf("period = %d, want %d", w.Properties.Period, dashboardPeriod)
				}
				if len(w.Properties.Metrics) != 3 {
					t.Errorf("got %d metric lines, want 3", len(w.Properties.Metrics))
				}
			},
		},
		{
			name: "multi-agent pack produces agent widgets plus A2A widget",
			pack: &prompt.Pack{
				ID: "multi",
				Agents: &prompt.AgentsConfig{
					Entry: "coordinator",
					Members: map[string]*prompt.AgentDef{
						"coordinator": {},
						"worker":      {},
					},
				},
				Prompts: map[string]*prompt.PackPrompt{
					"coordinator": {},
					"worker":      {},
				},
			},
			region:      "eu-west-1",
			wantWidgets: 3, // 2 agent + 1 A2A latency
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				// Agent widgets should be sorted by name.
				assertStr(t, "w0.title", dc.Widgets[0].Properties.Title, "Agent: coordinator")
				assertStr(t, "w1.title", dc.Widgets[1].Properties.Title, "Agent: worker")
				// A2A widget.
				assertStr(t, "w2.title", dc.Widgets[2].Properties.Title, "Inter-Agent A2A Call Latency")
				if dc.Widgets[2].Width != dashboardGridColumns {
					t.Errorf("A2A widget width = %d, want %d", dc.Widgets[2].Width, dashboardGridColumns)
				}
			},
		},
		{
			name: "eval metrics produce eval widgets",
			pack: &prompt.Pack{
				ID: "eval-pack",
				Evals: []evals.EvalDef{
					{
						ID: "accuracy",
						Metric: &evals.MetricDef{
							Name: "accuracy_score",
							Type: evals.MetricGauge,
						},
					},
					{
						ID: "latency",
						Metric: &evals.MetricDef{
							Name: "response_latency",
							Type: evals.MetricHistogram,
						},
					},
				},
			},
			region:      "us-west-2",
			wantWidgets: 3, // 1 agent (pack ID) + 2 eval
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				// First widget is the agent widget.
				assertStr(t, "w0.title", dc.Widgets[0].Properties.Title, "Agent: eval-pack")
				// Eval widgets.
				assertStr(t, "w1.title", dc.Widgets[1].Properties.Title, "Eval: accuracy_score")
				assertStr(t, "w2.title", dc.Widgets[2].Properties.Title, "Eval: response_latency")
			},
		},
		{
			name: "eval with range produces threshold annotations",
			pack: &prompt.Pack{
				ID: "threshold-pack",
				Evals: []evals.EvalDef{
					{
						ID: "bounded",
						Metric: &evals.MetricDef{
							Name: "bounded_score",
							Type: evals.MetricGauge,
							Range: &evals.Range{
								Min: floatPtr(0.5),
								Max: floatPtr(1.0),
							},
						},
					},
				},
			},
			region:      "us-west-2",
			wantWidgets: 2, // 1 agent + 1 eval
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				evalW := dc.Widgets[1]
				if evalW.Properties.Annotations == nil {
					t.Fatal("expected annotations on eval widget")
				}
				horiz := evalW.Properties.Annotations.Horizontal
				if len(horiz) != 2 {
					t.Fatalf("got %d thresholds, want 2", len(horiz))
				}
				if horiz[0].Label != "min" || horiz[0].Value != 0.5 {
					t.Errorf("min threshold = %+v", horiz[0])
				}
				if horiz[1].Label != "max" || horiz[1].Value != 1.0 {
					t.Errorf("max threshold = %+v", horiz[1])
				}
				assertStr(t, "min color", horiz[0].Color, colorThresholdMin)
				assertStr(t, "max color", horiz[1].Color, colorThresholdMax)
			},
		},
		{
			name: "eval with only min threshold",
			pack: &prompt.Pack{
				ID: "min-only",
				Evals: []evals.EvalDef{
					{
						ID: "e1",
						Metric: &evals.MetricDef{
							Name:  "score",
							Type:  evals.MetricGauge,
							Range: &evals.Range{Min: floatPtr(0.8)},
						},
					},
				},
			},
			region:      "us-west-2",
			wantWidgets: 2,
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				horiz := dc.Widgets[1].Properties.Annotations.Horizontal
				if len(horiz) != 1 {
					t.Fatalf("got %d thresholds, want 1", len(horiz))
				}
				assertStr(t, "label", horiz[0].Label, "min")
			},
		},
		{
			name: "eval with only max threshold",
			pack: &prompt.Pack{
				ID: "max-only",
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
			region:      "us-west-2",
			wantWidgets: 2,
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				horiz := dc.Widgets[1].Properties.Annotations.Horizontal
				if len(horiz) != 1 {
					t.Fatalf("got %d thresholds, want 1", len(horiz))
				}
				assertStr(t, "label", horiz[0].Label, "max")
			},
		},
		{
			name: "evals without metrics are skipped",
			pack: &prompt.Pack{
				ID: "mixed",
				Evals: []evals.EvalDef{
					{ID: "no-metric-1"},
					{
						ID:     "has-metric",
						Metric: &evals.MetricDef{Name: "m1", Type: evals.MetricGauge},
					},
					{ID: "no-metric-2"},
				},
			},
			region:      "us-west-2",
			wantWidgets: 2, // 1 agent + 1 eval
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				assertStr(t, "eval title", dc.Widgets[1].Properties.Title, "Eval: m1")
			},
		},
		{
			name: "widget layout positions are correct",
			pack: &prompt.Pack{
				ID: "layout",
				Agents: &prompt.AgentsConfig{
					Entry: "a",
					Members: map[string]*prompt.AgentDef{
						"a": {}, "b": {}, "c": {},
					},
				},
				Prompts: map[string]*prompt.PackPrompt{
					"a": {}, "b": {}, "c": {},
				},
			},
			region:      "us-west-2",
			wantWidgets: 4, // 3 agent + 1 A2A
			checkFunc: func(t *testing.T, dc *DashboardConfig) {
				t.Helper()
				// First two agents in row 0.
				if dc.Widgets[0].X != 0 || dc.Widgets[0].Y != 0 {
					t.Errorf("w0 pos = (%d,%d), want (0,0)", dc.Widgets[0].X, dc.Widgets[0].Y)
				}
				if dc.Widgets[1].X != dashboardWidgetWidth || dc.Widgets[1].Y != 0 {
					t.Errorf("w1 pos = (%d,%d), want (%d,0)", dc.Widgets[1].X, dc.Widgets[1].Y, dashboardWidgetWidth)
				}
				// Third agent wraps to next row.
				if dc.Widgets[2].X != 0 || dc.Widgets[2].Y != dashboardWidgetHeight {
					t.Errorf("w2 pos = (%d,%d), want (0,%d)", dc.Widgets[2].X, dc.Widgets[2].Y, dashboardWidgetHeight)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := buildDashboardConfig(tt.pack, tt.region)

			if tt.wantNil {
				if dc != nil {
					t.Fatalf("expected nil, got %+v", dc)
				}
				return
			}
			if dc == nil {
				t.Fatal("expected non-nil DashboardConfig")
			}

			if tt.wantWidgets > 0 && len(dc.Widgets) != tt.wantWidgets {
				t.Fatalf("got %d widgets, want %d", len(dc.Widgets), tt.wantWidgets)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, dc)
			}
		})
	}
}

func TestBuildThresholdAnnotations(t *testing.T) {
	tests := []struct {
		name      string
		metric    *evals.MetricDef
		wantNil   bool
		wantCount int
	}{
		{
			name:    "nil range returns nil",
			metric:  &evals.MetricDef{Name: "m", Type: evals.MetricGauge},
			wantNil: true,
		},
		{
			name: "both min and max",
			metric: &evals.MetricDef{
				Name:  "m",
				Type:  evals.MetricGauge,
				Range: &evals.Range{Min: floatPtr(0), Max: floatPtr(1)},
			},
			wantCount: 2,
		},
		{
			name: "only min",
			metric: &evals.MetricDef{
				Name:  "m",
				Type:  evals.MetricGauge,
				Range: &evals.Range{Min: floatPtr(0.5)},
			},
			wantCount: 1,
		},
		{
			name: "only max",
			metric: &evals.MetricDef{
				Name:  "m",
				Type:  evals.MetricGauge,
				Range: &evals.Range{Max: floatPtr(100)},
			},
			wantCount: 1,
		},
		{
			name: "range with no min or max returns nil",
			metric: &evals.MetricDef{
				Name:  "m",
				Type:  evals.MetricGauge,
				Range: &evals.Range{},
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := buildThresholdAnnotations(tt.metric)
			if tt.wantNil {
				if a != nil {
					t.Fatalf("expected nil, got %+v", a)
				}
				return
			}
			if a == nil {
				t.Fatal("expected non-nil annotations")
			}
			if len(a.Horizontal) != tt.wantCount {
				t.Fatalf("got %d thresholds, want %d", len(a.Horizontal), tt.wantCount)
			}
		})
	}
}

func TestAgentWidgetNames(t *testing.T) {
	tests := []struct {
		name string
		pack *prompt.Pack
		want []string
	}{
		{
			name: "single agent uses pack ID",
			pack: &prompt.Pack{ID: "solo"},
			want: []string{"solo"},
		},
		{
			name: "multi-agent returns sorted member names",
			pack: &prompt.Pack{
				ID: "multi",
				Agents: &prompt.AgentsConfig{
					Entry: "z",
					Members: map[string]*prompt.AgentDef{
						"z": {}, "a": {}, "m": {},
					},
				},
			},
			want: []string{"a", "m", "z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentWidgetNames(tt.pack)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d names, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("name[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}
