package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flare-foundation/tee-proxy/internal/initialize"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

const (
	cfgPath              = "./config/config.toml"
	forceShutdownTimeout = 15 * time.Second
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		initialize.Initialize(ctx, cfgPath)
		close(done)
	}()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-signalChan:
		logger.Infof("Received %v signal, shutting down", sig)
	case <-done:
		logger.Info("Initialization returned, shutting down")
	}
	cancel()

	// Give Initialize a bounded window to drain HTTP servers and exit.
	select {
	case <-done:
	case <-time.After(forceShutdownTimeout):
		logger.Warn("Shutdown timeout exceeded, forcing exit")
	}
}
