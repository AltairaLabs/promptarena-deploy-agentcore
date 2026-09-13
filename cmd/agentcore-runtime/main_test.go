package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/AltairaLabs/PromptKit/sdk/v2"
	a2aserver "github.com/AltairaLabs/PromptKit/server/a2a/v2"
)

func TestRunWithShutdown_SignalTermination(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// Use a no-op opener since we won't send A2A requests.
	opener := sdk.A2AOpener("nonexistent.pack.json", "test")
	a2aSrv := a2aserver.NewServer(opener)

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
		errCh <- runWithShutdown(log, ln, mux, healthH, a2aSrv, nil)
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
	a2aSrv := a2aserver.NewServer(opener)

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
		errCh <- runWithShutdown(log, ln, mux, healthH, a2aSrv, nil)
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

// TestMaterializePack_WritesToAnUnpredictableTempFile covers materializePack,
// which had no test.
//
// It used to write to a fixed "/tmp/pack.json". /tmp is world-writable and the
// name was predictable, so anything else on the host could pre-create that
// path as a symlink and have the runtime write the pack through it — the 0600
// mode does not help, because the file already exists by then. os.CreateTemp
// opens with O_EXCL and a random suffix, which is what makes that impossible;
// asserting the path is unpredictable is asserting the fix.
func TestMaterializePack_WritesToAnUnpredictableTempFile(t *testing.T) {
	const packJSON = `{"prompts":{"a":{}}}`

	first := &runtimeConfig{PackJSON: packJSON}
	if err := materializePack(first); err != nil {
		t.Fatalf("materializePack: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(first.PackFile) })

	if first.PackFile == "" {
		t.Fatal("materializePack must set PackFile")
	}
	if first.PackFile == "/tmp/pack.json" {
		t.Error("the pack path must not be the old fixed, predictable location")
	}

	got, err := os.ReadFile(first.PackFile)
	if err != nil {
		t.Fatalf("reading the materialized pack: %v", err)
	}
	if string(got) != packJSON {
		t.Errorf("pack contents = %q, want %q", got, packJSON)
	}

	info, err := os.Stat(first.PackFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != tmpPackPerm {
		t.Errorf("permissions = %o, want %o — the pack can carry credentials", perm, tmpPackPerm)
	}

	// Two runs must not collide. A fixed name meant a second runtime on the
	// same host overwrote the first one's pack mid-flight.
	second := &runtimeConfig{PackJSON: packJSON}
	if err := materializePack(second); err != nil {
		t.Fatalf("second materializePack: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(second.PackFile) })

	if second.PackFile == first.PackFile {
		t.Errorf("two runs produced the same path %q; they must not collide", first.PackFile)
	}
}

// TestMaterializePack_NoopWhenNothingToMaterialize pins the two skip
// conditions: no inline JSON, or a pack file the caller already supplied.
// Writing in either case would discard the caller's own path.
func TestMaterializePack_NoopWhenNothingToMaterialize(t *testing.T) {
	empty := &runtimeConfig{}
	if err := materializePack(empty); err != nil {
		t.Fatalf("no pack JSON must be a no-op, got %v", err)
	}
	if empty.PackFile != "" {
		t.Errorf("PackFile = %q, want it left unset", empty.PackFile)
	}

	explicit := &runtimeConfig{PackJSON: `{"prompts":{}}`, PackFile: "/some/given/path.json"}
	if err := materializePack(explicit); err != nil {
		t.Fatalf("an explicit PackFile must be a no-op, got %v", err)
	}
	if explicit.PackFile != "/some/given/path.json" {
		t.Errorf("PackFile = %q, want the caller's path preserved", explicit.PackFile)
	}
}
