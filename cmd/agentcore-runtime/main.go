package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/sdk"
)

const (
	shutdownTimeout        = 10 * time.Second
	defaultReadHeaderTmout = 10 * time.Second
	tmpPackPath            = "/tmp/pack.json"
	tmpPackPerm            = 0o600
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if cfg.PackJSON != "" && cfg.PackFile == "" {
		if writeErr := os.WriteFile(tmpPackPath, []byte(cfg.PackJSON), tmpPackPerm); writeErr != nil {
			return fmt.Errorf("write pack JSON to temp file: %w", writeErr)
		}
		cfg.PackFile = tmpPackPath
	}

	pack, err := prompt.LoadPack(cfg.PackFile)
	if err != nil {
		return fmt.Errorf("load pack: %w", err)
	}

	agentName, err := resolveAgentName(cfg, pack)
	if err != nil {
		return err
	}
	log.Info("resolved agent", "name", agentName, "pack", cfg.PackFile)

	shutdownTracing := setupTracing(cfg, log)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = shutdownTracing(ctx)
	}()

	sdkOpts := buildSDKOptions(cfg)
	opener := sdk.A2AOpener(cfg.PackFile, agentName, sdkOpts...)

	card := buildAgentCard(pack, agentName)
	a2aSrv := sdk.NewA2AServer(opener, sdk.WithA2ACard(card))

	healthH := newHealthHandler()
	mux := buildMux(a2aSrv.Handler(), healthH)

	addr := fmt.Sprintf(":%d", cfg.Port)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	log.Info("listening", "addr", ln.Addr().String(), "version", version)

	return runWithShutdown(log, ln, mux, healthH, a2aSrv)
}

// runWithShutdown starts the HTTP server and handles graceful shutdown on SIGTERM/SIGINT.
func runWithShutdown(
	log *slog.Logger,
	ln net.Listener,
	mux *http.ServeMux,
	healthH *healthHandler,
	a2aSrv *sdk.A2AServer,
) error {
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTmout,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		log.Info("received signal, shutting down", "signal", sig)
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	}

	healthH.setUnhealthy()

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := a2aSrv.Shutdown(ctx); err != nil {
		log.Error("a2a server shutdown", "error", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}
