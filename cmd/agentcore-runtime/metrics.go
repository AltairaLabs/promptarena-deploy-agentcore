package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/sdk"
)

// metricsConfig is the eval-to-metric mapping the adapter injects.
//
// It mirrors the adapter's own type. The adapter has always built and injected
// this; nothing read it, so every eval the deployed agent ran was computed and
// discarded — an eval nobody can see is indistinguishable from no eval.
type metricsConfig struct {
	Namespace  string            `json:"namespace"`
	Dimensions map[string]string `json:"dimensions"`
	Metrics    []metricEntry     `json:"metrics"`
}

// metricEntry names the metric one eval publishes under.
type metricEntry struct {
	EvalID     string `json:"eval_id"`
	MetricName string `json:"metric_name"`
	Unit       string `json:"unit,omitempty"`
}

// parseMetricsConfig decodes the injected config. Empty means the pack declares
// no evals worth publishing, which is not an error.
func parseMetricsConfig(raw string) (*metricsConfig, error) {
	if raw == "" {
		return nil, nil
	}
	var cfg metricsConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", envMetricsConfig, err)
	}
	if cfg.Namespace == "" || len(cfg.Metrics) == 0 {
		return nil, nil
	}
	return &cfg, nil
}

// cloudWatchPutter is the one CloudWatch call this needs.
type cloudWatchPutter interface {
	PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput,
		opts ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error)
}

// cloudWatchRecorder publishes eval results as CloudWatch metrics.
//
// It satisfies PromptKit's evals.MetricRecorder, which the pipeline calls for
// every eval result, so the results reach an operator without the runtime
// having to intercept turns itself.
type cloudWatchRecorder struct {
	client     cloudWatchPutter
	namespace  string
	dimensions []cwtypes.Dimension
	// names maps an eval id to the metric name it publishes under. An eval
	// absent from the map is still recorded, under its own id, so adding an
	// eval to a pack does not silently drop it.
	names map[string]metricEntry
	log   *slog.Logger

	// mu guards failed, which exists to keep a broken metrics pipeline from
	// writing a line per eval per turn.
	mu     sync.Mutex
	failed bool
}

// newCloudWatchRecorder builds a recorder from the injected config.
func newCloudWatchRecorder(
	client cloudWatchPutter, cfg *metricsConfig, log *slog.Logger,
) *cloudWatchRecorder {
	names := make(map[string]metricEntry, len(cfg.Metrics))
	for _, m := range cfg.Metrics {
		names[m.EvalID] = m
	}

	keys := make([]string, 0, len(cfg.Dimensions))
	for k := range cfg.Dimensions {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	dims := make([]cwtypes.Dimension, 0, len(keys))
	for _, k := range keys {
		dims = append(dims, cwtypes.Dimension{
			Name:  aws.String(k),
			Value: aws.String(cfg.Dimensions[k]),
		})
	}

	return &cloudWatchRecorder{
		client:     client,
		namespace:  cfg.Namespace,
		dimensions: dims,
		names:      names,
		log:        log,
	}
}

// Record publishes one eval result.
//
// A failure here is logged once and swallowed. The turn has already produced an
// answer by the time an eval runs, and losing a metric is not worth failing the
// conversation the metric describes.
//
//nolint:gocritic // the MetricRecorder interface takes the result by value.
func (r *cloudWatchRecorder) Record(result evals.EvalResult, metric *evals.MetricDef) error {
	value, ok := metricValue(&result)
	if !ok {
		return nil
	}

	_, err := r.client.PutMetricData(context.Background(), &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(r.namespace),
		MetricData: []cwtypes.MetricDatum{{
			MetricName: aws.String(r.metricName(&result, metric)),
			Value:      aws.Float64(value),
			Unit:       cwtypes.StandardUnitNone,
			Timestamp:  aws.Time(time.Now()),
			Dimensions: r.dimensions,
		}},
	})
	if err != nil {
		r.reportOnce(result.EvalID, err)
	}
	return nil
}

// metricName is what this eval publishes under.
//
// The pack's own metric definition wins, then the adapter's mapping, then the
// eval's id — so an eval that nobody named still appears rather than vanishing.
func (r *cloudWatchRecorder) metricName(result *evals.EvalResult, metric *evals.MetricDef) string {
	if metric != nil && metric.Name != "" {
		return metric.Name
	}
	if entry, ok := r.names[result.EvalID]; ok && entry.MetricName != "" {
		return entry.MetricName
	}
	return result.EvalID
}

// reportOnce logs the first publish failure and stays quiet after it.
func (r *cloudWatchRecorder) reportOnce(evalID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return
	}
	r.failed = true
	r.log.Error("eval metrics are not reaching CloudWatch; "+
		"evals still run but nobody will see them",
		"eval", evalID, "namespace", r.namespace, "error", err)
}

// metricValue is the number to publish for a result.
//
// MetricValue is what an eval sets deliberately; Score is the fallback for the
// ones that only produce one. A result carrying neither is not a number and is
// skipped rather than published as zero, which would read as a real low score.
func metricValue(result *evals.EvalResult) (float64, bool) {
	if result.MetricValue != nil {
		return *result.MetricValue, true
	}
	if result.Score != nil {
		return *result.Score, true
	}
	return 0, false
}

// withMetricRecorder publishes eval results when the pack declares metrics.
//
// PromptKit calls a MetricRecorder for every eval result, so this is the seam
// that turns evals the deployed agent already computes into something an
// operator can see.
func withMetricRecorder(
	ctx context.Context, opts []sdk.Option, cfg *runtimeConfig, log *slog.Logger,
) ([]sdk.Option, error) {
	metrics, err := parseMetricsConfig(cfg.MetricsConfig)
	if err != nil {
		return nil, err
	}
	if metrics == nil {
		return opts, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		// The agent serves without metrics rather than refusing to start:
		// losing observability is not worth losing the deployment.
		log.Warn("no AWS credentials for eval metrics; evals will run unreported",
			"error", err)
		return opts, nil
	}

	log.Info("publishing eval metrics",
		"namespace", metrics.Namespace, "metrics", len(metrics.Metrics))

	// WithMetricRecorder over the newer WithMetrics: the replacement builds a
	// Prometheus collector to be scraped, and nothing scrapes an Agent Runtime
	// container. CloudWatch is a push, which is what a per-result recorder is.
	//nolint:staticcheck // SA1019: the replacement is pull-based; this is not.
	return append(opts, sdk.WithMetricRecorder(
		newCloudWatchRecorder(cloudwatch.NewFromConfig(awsCfg), metrics, log),
	)), nil
}
