---
title: Set Up Observability
sidebar:
  order: 4
---

This guide covers how to configure logging, tracing, metrics, dashboards, and alarms for your AgentCore deployment.

## Prerequisites

- A working adapter configuration with `region` and `runtime_role_arn`.
- The runtime IAM role must have permissions to write to CloudWatch Logs, CloudWatch Metrics, and (if tracing is enabled) AWS X-Ray.
- If using a custom log group, it should already exist in your target region (the adapter does not create log groups).

## Goal

Enable comprehensive observability for your deployed agents so you can monitor runtime behavior, diagnose issues, and set up automated alerts.

## CloudWatch log group

Set the `cloudwatch_log_group` field to direct agent runtime logs to a specific CloudWatch Logs group:

```yaml
observability:
  cloudwatch_log_group: /aws/agentcore/my-agent
```

The adapter injects this value into the runtime as the `PROMPTPACK_LOG_GROUP` environment variable. The agent runtime SDK reads this variable and configures its log output accordingly.

If you omit this field, the runtime uses its default logging behavior (typically stdout, captured by the AgentCore service).

## Tracing with AWS X-Ray

Enable distributed tracing by setting `tracing_enabled`:

```yaml
observability:
  cloudwatch_log_group: /aws/agentcore/my-agent
  tracing_enabled: true
```

When enabled, the adapter sets the `PROMPTPACK_TRACING_ENABLED` environment variable to `"true"` on the runtime. The agent runtime SDK uses this to enable X-Ray trace propagation, which provides end-to-end visibility into request flows across agent invocations, tool calls, and A2A communication.

### Required IAM permissions for tracing

The runtime role needs the following X-Ray permissions:

```json
{
  "Effect": "Allow",
  "Action": [
    "xray:PutTraceSegments",
    "xray:PutTelemetryRecords",
    "xray:GetSamplingRules",
    "xray:GetSamplingTargets"
  ],
  "Resource": "*"
}
```

## CloudWatch metrics from evaluators

When your pack defines evaluators with `Metric` definitions, the adapter automatically generates a CloudWatch metrics configuration and injects it into the runtime. You do not need to configure this explicitly -- it is derived from the pack's eval definitions.

### How it works

1. The adapter scans each eval in the pack for a `Metric` field (which includes `name`, `type`, and optionally `range`).
2. It builds a `MetricsConfig` object with:
   - **Namespace**: `PromptPack/Evals`
   - **Dimensions**: `pack_id` (and `agent: multi` for multi-agent packs)
   - **Metrics**: one entry per eval metric, with the CloudWatch unit derived from the metric type
   - **Alarms**: one entry per metric that has a `range` (min/max thresholds)
3. The config is JSON-serialized and injected as the `PROMPTPACK_METRICS_CONFIG` environment variable.

### Metric type to CloudWatch unit mapping

| PromptKit metric type | CloudWatch unit |
|----------------------|-----------------|
| `counter` | `Count` |
| `histogram` | `Milliseconds` |
| `gauge` | `None` |
| `boolean` | `None` |

### Alarm generation from metric ranges

When an eval metric defines a `range` with `min` and/or `max` values, the adapter generates alarm entries in the metrics config. These tell the runtime SDK to create CloudWatch alarms that fire when the metric falls outside the defined range.

For example, a metric with `range: { min: 0.8, max: 1.0 }` produces an alarm entry:

```json
{
  "metric_name": "accuracy",
  "min": 0.8,
  "max": 1.0
}
```

## Auto-generated CloudWatch dashboard

The adapter generates a CloudWatch dashboard configuration from the pack structure and injects it via the `PROMPTPACK_DASHBOARD_CONFIG` environment variable. The dashboard is built from three types of widgets:

### Agent widgets

One widget per agent member (or one for single-agent packs). Each widget displays three metrics:

- **Invocations** -- total invocation count
- **Errors** -- error count
- **Duration** -- invocation duration

For a multi-agent pack with agents `coordinator` and `researcher`, you get two widgets titled "Agent: coordinator" and "Agent: researcher".

### A2A latency widget

For multi-agent packs only. A full-width widget titled "Inter-Agent A2A Call Latency" that plots the `A2ALatency` metric for each agent member, making it easy to spot communication bottlenecks between agents.

### Eval metric widgets

One widget per evaluator that defines a metric. Each widget plots the eval metric under the `PromptPack/Evals` namespace with the `pack_id` dimension.

When a metric has a `range` defined, the widget includes horizontal threshold annotations:

- **Green line** at the `min` value (labeled "min")
- **Red line** at the `max` value (labeled "max")

These annotations make it visually clear when a metric is inside or outside its expected range.

### Dashboard layout

Widgets are arranged in a two-column grid, each 12 units wide and 6 units tall. The A2A latency widget spans the full 24-unit width. The layout order is:

1. Agent widgets (one row per two agents)
2. A2A latency widget (multi-agent only)
3. Eval metric widgets (one row per two evals)

## Complete observability example

```yaml
region: us-west-2
runtime_role_arn: arn:aws:iam::123456789012:role/AgentCoreExecutionRole

observability:
  cloudwatch_log_group: /aws/agentcore/support-bot
  tracing_enabled: true

tags:
  environment: production
```

With a pack that defines two agents and an eval with a metric, this configuration results in:

| Environment variable | Content |
|---------------------|---------|
| `PROMPTPACK_LOG_GROUP` | `/aws/agentcore/support-bot` |
| `PROMPTPACK_TRACING_ENABLED` | `true` |
| `PROMPTPACK_METRICS_CONFIG` | JSON with namespace, dimensions, metric entries, and alarm thresholds |
| `PROMPTPACK_DASHBOARD_CONFIG` | JSON with agent widgets, A2A latency widget, and eval metric widgets |

## Injected environment variables reference

| Variable | Set when | Description |
|----------|----------|-------------|
| `PROMPTPACK_LOG_GROUP` | `cloudwatch_log_group` is set | CloudWatch Logs group name for agent logs. |
| `PROMPTPACK_TRACING_ENABLED` | `tracing_enabled` is `true` | Enables X-Ray trace propagation in the runtime. |
| `PROMPTPACK_METRICS_CONFIG` | Pack evals define metrics | JSON-encoded metrics config (namespace, dimensions, metric entries, alarms). |
| `PROMPTPACK_DASHBOARD_CONFIG` | Pack has agents or eval metrics | JSON-encoded CloudWatch dashboard body (widgets with layout). |

## Troubleshooting

**No metrics appearing in CloudWatch** -- Verify that your pack's eval definitions include a `Metric` field. The adapter only generates metrics config when at least one eval has a metric. Also check that the runtime role has `cloudwatch:PutMetricData` permission.

**Dashboard config not generated** -- The adapter returns `nil` (and skips injection) when the pack has no agents and no eval metrics. Ensure your pack defines at least one agent or one eval with a metric.

**Log group not found errors at runtime** -- The adapter injects the log group name but does not create the CloudWatch Logs group. Create it manually or via your infrastructure-as-code tool before deploying.

**X-Ray traces not appearing** -- Confirm that `tracing_enabled: true` is set in the `observability` block and that the runtime role has the required X-Ray permissions. Also verify that the X-Ray service is available in your target region.
