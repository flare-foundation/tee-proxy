package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flare-foundation/tee-proxy/internal/initialize"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

const cfgPath = "./config/config.toml"

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	go initialize.Initialize(ctx, cfgPath)

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-signalChan:
		logger.Infof("Received %v signal, shutting down", sig)
	case <-ctx.Done():
		logger.Infof("Context canceled %v signal, shutting down", ctx.Err())
	}
	cancel()
}
