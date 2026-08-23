package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/AltairaLabs/promptarena-deploy-agentcore/internal/agentcore"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}

// TestTracingEnvNamesMatchTheAdapter is the regression test for the defect that
// made tracing_enabled do nothing: the adapter injected one variable name and
// the runtime read another, so the flag never arrived. Neither side's tests
// could see it, because each was right on its own. Pin them together.
func TestTracingEnvNamesMatchTheAdapter(t *testing.T) {
	if envTracingEnabled != agentcore.EnvTracingEnabled {
		t.Errorf("runtime reads %q but the adapter injects %q; tracing_enabled will not arrive",
			envTracingEnabled, agentcore.EnvTracingEnabled)
	}
	if envOTLPEndpoint != agentcore.EnvOTLPEndpoint {
		t.Errorf("runtime reads %q but the adapter injects %q; the endpoint will not arrive",
			envOTLPEndpoint, agentcore.EnvOTLPEndpoint)
	}
}

func TestSetupTracing_Disabled(t *testing.T) {
	shutdown, opts := setupTracing(&runtimeConfig{TracingEnabled: false}, quietLogger())

	if len(opts) != 0 {
		t.Errorf("disabled tracing should attach no SDK options, got %d", len(opts))
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
}

// Propagation is useful on AgentCore without an endpoint, since the platform
// collects X-Ray traces itself. No endpoint means no *export*, not no tracing.
func TestSetupTracing_EnabledNoEndpoint(t *testing.T) {
	shutdown, opts := setupTracing(
		&runtimeConfig{TracingEnabled: true, OTLPEndpoint: ""}, quietLogger())

	if len(opts) != 0 {
		t.Errorf("without an endpoint there is no provider to attach, got %d options", len(opts))
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
}

// The original defect: setupTracing built a provider and returned only its
// Shutdown, so nothing ever registered it and every span went to the global
// no-op provider while the log said "tracing enabled".
func TestSetupTracing_EnabledRegistersTheProvider(t *testing.T) {
	shutdown, opts := setupTracing(&runtimeConfig{
		TracingEnabled: true,
		OTLPEndpoint:   "http://localhost:4318",
	}, quietLogger())

	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}
	if len(opts) == 0 {
		t.Fatal("a configured exporter must be attached to the SDK, or spans go nowhere")
	}
}
