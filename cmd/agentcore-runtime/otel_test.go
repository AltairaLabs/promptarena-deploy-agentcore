package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

func TestSetupTracing_Disabled(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := &runtimeConfig{TracingEnabled: false}
	shutdown := setupTracing(cfg, log)

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
}

func TestSetupTracing_EnabledNoEndpoint(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := &runtimeConfig{TracingEnabled: true, OTLPEndpoint: ""}
	shutdown := setupTracing(cfg, log)

	// Should return no-op because endpoint is empty
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
}

func TestSetupTracing_Enabled(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := &runtimeConfig{
		TracingEnabled: true,
		OTLPEndpoint:   "http://localhost:4318",
	}
	shutdown := setupTracing(cfg, log)

	// shutdown function is from the exporter, calling it is safe even without a real collector
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}
}
