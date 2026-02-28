package main

import (
	"context"
	"log/slog"

	"github.com/AltairaLabs/PromptKit/runtime/telemetry"
)

// tracingShutdown flushes and shuts down the trace exporter.
type tracingShutdown func(context.Context) error

// setupTracing configures OTLP trace export if enabled.
// The A2A server already applies telemetry.TraceMiddleware for inbound header
// extraction, and the SDK propagates trace context on outbound calls.
func setupTracing(cfg *runtimeConfig, log *slog.Logger) tracingShutdown {
	if !cfg.TracingEnabled || cfg.OTLPEndpoint == "" {
		log.Info("tracing disabled")
		return func(context.Context) error { return nil }
	}

	tp, err := telemetry.NewTracerProvider(context.Background(), cfg.OTLPEndpoint, "agentcore-runtime")
	if err != nil {
		log.Error("failed to create tracer provider", "error", err)
		return func(context.Context) error { return nil }
	}

	telemetry.SetupPropagation()

	log.Info("tracing enabled", "endpoint", cfg.OTLPEndpoint)
	return tp.Shutdown
}
