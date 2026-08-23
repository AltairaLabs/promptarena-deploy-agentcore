package main

import (
	"context"
	"log/slog"

	"github.com/AltairaLabs/PromptKit/runtime/telemetry"
	"github.com/AltairaLabs/PromptKit/sdk"
)

// serviceName identifies this runtime in traces.
const serviceName = "agentcore-runtime"

// tracingShutdown flushes and shuts down the trace exporter.
type tracingShutdown func(context.Context) error

// noopShutdown is returned whenever there is no exporter to close.
func noopShutdown(context.Context) error { return nil }

// setupTracing turns on trace propagation and, if an OTLP endpoint is
// configured, span export. It returns a shutdown function and the SDK options
// that attach the tracer to conversations.
//
// Propagation and export are deliberately separate. On AgentCore the platform
// collects X-Ray traces itself, so installing the propagator is worthwhile with
// no endpoint at all — that is what tracing_enabled means here, and what the
// docs describe. An endpoint additionally exports spans to a collector you run.
//
// The options are returned together with the shutdown so a caller cannot start
// an exporter and then forget to register it. Returning the provider without
// registering it was the original defect: the runtime logged "tracing enabled"
// while every span went to the global no-op provider.
//
// A misconfiguration disables tracing rather than failing the container: an
// agent that serves without traces is better than one that does not serve.
func setupTracing(cfg *runtimeConfig, log *slog.Logger) (tracingShutdown, []sdk.Option) {
	if !cfg.TracingEnabled {
		log.Info("tracing disabled")
		return noopShutdown, nil
	}

	telemetry.SetupPropagation()

	if cfg.OTLPEndpoint == "" {
		log.Info("trace propagation enabled; no OTLP endpoint set, so spans are not exported",
			"variable", envOTLPEndpoint)
		return noopShutdown, nil
	}

	provider, err := telemetry.NewTracerProvider(
		context.Background(), cfg.OTLPEndpoint, serviceName)
	if err != nil {
		log.Error("failed to create tracer provider; propagation stays on, export is off",
			"endpoint", cfg.OTLPEndpoint, "error", err)
		return noopShutdown, nil
	}

	log.Info("tracing enabled", "endpoint", cfg.OTLPEndpoint, "service", serviceName)
	return provider.Shutdown, []sdk.Option{sdk.WithTracerProvider(provider)}
}
