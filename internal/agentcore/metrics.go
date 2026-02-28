package agentcore

import (
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// CloudWatch namespace for eval metrics.
const metricsNamespace = "PromptPack/Evals"

// CloudWatch unit strings mapped from MetricType.
const (
	unitNone         = "None"
	unitCount        = "Count"
	unitMilliseconds = "Milliseconds"
)

// MetricsConfig is the top-level CloudWatch metrics configuration
// injected into runtimes via PROMPTPACK_METRICS_CONFIG.
type MetricsConfig struct {
	Namespace  string            `json:"namespace"`
	Dimensions map[string]string `json:"dimensions"`
	Metrics    []MetricEntry     `json:"metrics"`
	Alarms     []AlarmEntry      `json:"alarms,omitempty"`
}

// MetricEntry describes a single eval metric for CloudWatch.
type MetricEntry struct {
	EvalID     string `json:"eval_id"`
	MetricName string `json:"metric_name"`
	MetricType string `json:"metric_type"`
	Unit       string `json:"unit"`
}

// AlarmEntry describes a CloudWatch alarm threshold for a metric.
type AlarmEntry struct {
	MetricName string   `json:"metric_name"`
	Min        *float64 `json:"min,omitempty"`
	Max        *float64 `json:"max,omitempty"`
}

// buildMetricsConfig iterates pack evals and builds a MetricsConfig for
// evals that define a Metric. Returns nil if no evals have metrics.
func buildMetricsConfig(pack *prompt.Pack) *MetricsConfig {
	var metrics []MetricEntry
	var alarms []AlarmEntry

	for i := range pack.Evals {
		if pack.Evals[i].Metric == nil {
			continue
		}

		evalID := pack.Evals[i].ID
		entry := MetricEntry{
			EvalID:     evalID,
			MetricName: pack.Evals[i].Metric.Name,
			MetricType: string(pack.Evals[i].Metric.Type),
			Unit:       metricTypeToUnit(pack.Evals[i].Metric.Type),
		}
		metrics = append(metrics, entry)

		if pack.Evals[i].Metric.Range != nil {
			alarms = append(alarms, AlarmEntry{
				MetricName: pack.Evals[i].Metric.Name,
				Min:        pack.Evals[i].Metric.Range.Min,
				Max:        pack.Evals[i].Metric.Range.Max,
			})
		}
	}

	if len(metrics) == 0 {
		return nil
	}

	dims := map[string]string{
		"pack_id": pack.ID,
	}
	if adaptersdk.IsMultiAgent(pack) {
		dims["agent"] = "multi"
	}

	return &MetricsConfig{
		Namespace:  metricsNamespace,
		Dimensions: dims,
		Metrics:    metrics,
		Alarms:     alarms,
	}
}

// metricTypeToUnit maps a PromptKit MetricType to a CloudWatch unit string.
func metricTypeToUnit(mt evals.MetricType) string {
	switch mt {
	case evals.MetricCounter:
		return unitCount
	case evals.MetricHistogram:
		return unitMilliseconds
	case evals.MetricGauge, evals.MetricBoolean:
		return unitNone
	default:
		return unitNone
	}
}
