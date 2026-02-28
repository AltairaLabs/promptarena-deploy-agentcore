package agentcore

import (
	"sort"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// CloudWatch dashboard layout constants.
const (
	dashboardWidgetWidth  = 12
	dashboardWidgetHeight = 6
	dashboardPeriod       = 300
	dashboardGridColumns  = 24
	dashboardColumns      = 2
)

// DashboardConfig is the top-level CloudWatch dashboard body
// injected into runtimes via PROMPTPACK_DASHBOARD_CONFIG.
type DashboardConfig struct {
	Widgets []DashboardWidget `json:"widgets"`
}

// DashboardWidget represents a single widget in the dashboard.
type DashboardWidget struct {
	Type       string               `json:"type"`
	X          int                  `json:"x"`
	Y          int                  `json:"y"`
	Width      int                  `json:"width"`
	Height     int                  `json:"height"`
	Properties DashboardWidgetProps `json:"properties"`
}

// DashboardWidgetProps holds widget display properties.
type DashboardWidgetProps struct {
	Title       string                `json:"title"`
	View        string                `json:"view,omitempty"`
	Stacked     bool                  `json:"stacked,omitempty"`
	Region      string                `json:"region,omitempty"`
	Metrics     [][]string            `json:"metrics,omitempty"`
	Annotations *DashboardAnnotations `json:"annotations,omitempty"`
	Period      int                   `json:"period,omitempty"`
}

// DashboardAnnotations holds horizontal threshold lines.
type DashboardAnnotations struct {
	Horizontal []DashboardThreshold `json:"horizontal,omitempty"`
}

// DashboardThreshold represents a threshold line on a widget.
type DashboardThreshold struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Color string  `json:"color"`
}

// Threshold line colors.
const (
	colorThresholdMin = "#2ca02c" // green
	colorThresholdMax = "#d62728" // red
)

// buildDashboardConfig generates a CloudWatch dashboard body from the pack
// structure. Returns nil if no widgets are generated (no agents and no evals
// with metrics).
func buildDashboardConfig(pack *prompt.Pack, region string) *DashboardConfig {
	var widgets []DashboardWidget
	row := 0

	// Agent widgets — one per agent member (or one for single-agent pack).
	widgets, row = appendAgentWidgets(widgets, pack, region, row)

	// A2A latency widget for multi-agent packs.
	widgets, row = appendA2ALatencyWidget(widgets, pack, region, row)

	// Eval metric widgets — one per eval with a metric.
	widgets = appendEvalWidgets(widgets, pack, region, row)

	if len(widgets) == 0 {
		return nil
	}

	return &DashboardConfig{Widgets: widgets}
}

// appendAgentWidgets adds one widget per agent showing key runtime metrics.
func appendAgentWidgets(
	widgets []DashboardWidget, pack *prompt.Pack, region string, row int,
) (result []DashboardWidget, nextRow int) {
	names := agentWidgetNames(pack)
	result = widgets
	nextRow = row

	for i, name := range names {
		col := (i % dashboardColumns) * dashboardWidgetWidth
		if i > 0 && i%dashboardColumns == 0 {
			nextRow += dashboardWidgetHeight
		}
		w := DashboardWidget{
			Type:   "metric",
			X:      col,
			Y:      nextRow,
			Width:  dashboardWidgetWidth,
			Height: dashboardWidgetHeight,
			Properties: DashboardWidgetProps{
				Title:  "Agent: " + name,
				Region: region,
				Period: dashboardPeriod,
				Metrics: [][]string{
					{metricsNamespace, "Invocations", "agent", name},
					{metricsNamespace, "Errors", "agent", name},
					{metricsNamespace, "Duration", "agent", name},
				},
			},
		}
		result = append(result, w)
	}
	if len(names) > 0 {
		nextRow += dashboardWidgetHeight
	}
	return result, nextRow
}

// appendA2ALatencyWidget adds an inter-agent call latency widget for
// multi-agent packs.
func appendA2ALatencyWidget(
	widgets []DashboardWidget, pack *prompt.Pack, region string, row int,
) (result []DashboardWidget, nextRow int) {
	if !adaptersdk.IsMultiAgent(pack) {
		return widgets, row
	}

	agents := adaptersdk.ExtractAgents(pack)
	var metrics [][]string
	for _, ag := range agents {
		metrics = append(metrics, []string{
			metricsNamespace, "A2ALatency", "agent", ag.Name,
		})
	}

	w := DashboardWidget{
		Type:   "metric",
		X:      0,
		Y:      row,
		Width:  dashboardGridColumns,
		Height: dashboardWidgetHeight,
		Properties: DashboardWidgetProps{
			Title:   "Inter-Agent A2A Call Latency",
			Region:  region,
			Period:  dashboardPeriod,
			Metrics: metrics,
		},
	}
	return append(widgets, w), row + dashboardWidgetHeight
}

// appendEvalWidgets adds one widget per eval metric with optional threshold
// lines from the metric range.
func appendEvalWidgets(
	widgets []DashboardWidget, pack *prompt.Pack, region string, row int,
) []DashboardWidget {
	for i := range pack.Evals {
		if pack.Evals[i].Metric == nil {
			continue
		}
		col := (i % dashboardColumns) * dashboardWidgetWidth
		if i > 0 && i%dashboardColumns == 0 {
			row += dashboardWidgetHeight
		}
		w := buildEvalWidget(&pack.Evals[i], pack.ID, region, col, row)
		widgets = append(widgets, w)
	}
	return widgets
}

// buildEvalWidget creates a single eval metric widget.
func buildEvalWidget(
	ev *evals.EvalDef, packID, region string, col, row int,
) DashboardWidget {
	w := DashboardWidget{
		Type:   "metric",
		X:      col,
		Y:      row,
		Width:  dashboardWidgetWidth,
		Height: dashboardWidgetHeight,
		Properties: DashboardWidgetProps{
			Title:  "Eval: " + ev.Metric.Name,
			Region: region,
			Period: dashboardPeriod,
			Metrics: [][]string{
				{metricsNamespace, ev.Metric.Name, "pack_id", packID},
			},
		},
	}

	annotations := buildThresholdAnnotations(ev.Metric)
	if annotations != nil {
		w.Properties.Annotations = annotations
	}

	return w
}

// buildThresholdAnnotations creates horizontal threshold lines from a metric
// range definition. Returns nil if no range is defined.
func buildThresholdAnnotations(metric *evals.MetricDef) *DashboardAnnotations {
	if metric.Range == nil {
		return nil
	}

	var thresholds []DashboardThreshold
	if metric.Range.Min != nil {
		thresholds = append(thresholds, DashboardThreshold{
			Label: "min",
			Value: *metric.Range.Min,
			Color: colorThresholdMin,
		})
	}
	if metric.Range.Max != nil {
		thresholds = append(thresholds, DashboardThreshold{
			Label: "max",
			Value: *metric.Range.Max,
			Color: colorThresholdMax,
		})
	}

	if len(thresholds) == 0 {
		return nil
	}
	return &DashboardAnnotations{Horizontal: thresholds}
}

// agentWidgetNames returns sorted agent names for widget generation.
// For multi-agent packs, each member gets a widget; for single-agent
// packs, the pack ID is used.
func agentWidgetNames(pack *prompt.Pack) []string {
	if adaptersdk.IsMultiAgent(pack) {
		agents := adaptersdk.ExtractAgents(pack)
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = a.Name
		}
		sort.Strings(names)
		return names
	}
	return []string{pack.ID}
}
