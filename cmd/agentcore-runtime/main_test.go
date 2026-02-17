package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/AltairaLabs/PromptKit/sdk"
)

func TestRunWithShutdown_SignalTermination(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// Use a no-op opener since we won't send A2A requests.
	opener := sdk.A2AOpener("nonexistent.pack.json", "test")
	a2aSrv := sdk.NewA2AServer(opener)

	healthH := newHealthHandler()
	mux := buildMux(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		healthH,
	)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithShutdown(log, ln, mux, healthH, a2aSrv)
	}()

	// Give the server a moment to start
	time.Sleep(50 * time.Millisecond)

	// Send SIGTERM to trigger shutdown
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runWithShutdown returned error: %v", err)
		}
	case <-time.After(shutdownTimeout + time.Second):
		t.Fatal("runWithShutdown did not return within timeout")
	}

	// Health should be unhealthy after shutdown
	if healthH.ready.Load() {
		t.Error("expected health handler to be unhealthy after shutdown")
	}
}

func TestRunWithShutdown_HealthDuringOperation(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	opener := sdk.A2AOpener("nonexistent.pack.json", "test")
	a2aSrv := sdk.NewA2AServer(opener)

	healthH := newHealthHandler()
	mux := buildMux(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		healthH,
	)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithShutdown(log, ln, mux, healthH, a2aSrv)
	}()

	// Give the server a moment to start
	time.Sleep(50 * time.Millisecond)

	// Verify health endpoint is reachable while running
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Trigger shutdown
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runWithShutdown returned error: %v", err)
		}
	case <-time.After(shutdownTimeout + time.Second):
		t.Fatal("runWithShutdown did not return within timeout")
	}
}

func TestRun_MissingPackFile(t *testing.T) {
	t.Setenv(envPackFile, "nonexistent.pack.json")
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := run(log)
	if err == nil {
		t.Fatal("expected error for missing pack file")
	}
}

func TestRun_MissingConfig(t *testing.T) {
	t.Setenv(envPackFile, "")

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := run(log)
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestRun_ValidPackWithSignal(t *testing.T) {
	// Create a minimal pack file for testing.
	packContent := `{
		"id": "test",
		"name": "Test",
		"version": "1.0.0",
		"template_engine": {"version": "v1", "syntax": "{{variable}}"},
		"prompts": {
			"agent": {
				"id": "agent",
				"name": "Agent",
				"version": "1.0.0",
				"system_template": "You are a test agent."
			}
		}
	}`
	packFile := t.TempDir() + "/test.pack.json"
	if err := os.WriteFile(packFile, []byte(packContent), 0o644); err != nil {
		t.Fatalf("write pack: %v", err)
	}

	t.Setenv(envPackFile, packFile)
	t.Setenv(envPort, "0") // random port
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")
	t.Setenv(envAWSRegion, "")
	t.Setenv(envAgentName, "")

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(log)
	}()

	// Give the server time to start listening
	time.Sleep(100 * time.Millisecond)

	// Send SIGTERM to trigger graceful shutdown
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(shutdownTimeout + time.Second):
		t.Fatal("run did not return within timeout")
	}
}

func TestRun_AmbiguousAgent(t *testing.T) {
	packContent := `{
		"id": "test",
		"name": "Test",
		"version": "1.0.0",
		"template_engine": {"version": "v1", "syntax": "{{variable}}"},
		"prompts": {
			"a": {"id": "a", "name": "A", "version": "1.0.0", "system_template": "A"},
			"b": {"id": "b", "name": "B", "version": "1.0.0", "system_template": "B"}
		}
	}`
	packFile := t.TempDir() + "/test.pack.json"
	if err := os.WriteFile(packFile, []byte(packContent), 0o644); err != nil {
		t.Fatalf("write pack: %v", err)
	}

	t.Setenv(envPackFile, packFile)
	t.Setenv(envPort, "0")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")
	t.Setenv(envAWSRegion, "")
	t.Setenv(envAgentName, "")

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := run(log)
	if err == nil {
		t.Fatal("expected error for ambiguous agent")
	}
}
