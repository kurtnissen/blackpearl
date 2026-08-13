package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/config"
	"github.com/blackpearl-media/blackpearl/internal/platform"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx); err != nil {
		if _, writeErr := fmt.Fprintf(os.Stderr, "blackpearl: %v\n", err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func execute(ctx context.Context) (executeErr error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := platform.NewLogger(cfg.LogLevel, os.Stdout)
	if err != nil {
		return err
	}
	shutdownTelemetry, err := platform.InitTelemetry(ctx, "blackpearl")
	if err != nil {
		return err
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		executeErr = errors.Join(executeErr, shutdownTelemetry(shutdownContext))
	}()
	return run(ctx, cfg, logger, defaultDependencies())
}
