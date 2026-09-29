package kernel

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// defaultShutdownTimeout is how long Run/RunUntilSignal wait for Container.Stop
// to complete after the run context is canceled. A second interrupt signal
// while stopping bypasses this grace period and terminates the process (the
// default signal behavior after signal.NotifyContext deregisters).
const defaultShutdownTimeout = 15 * time.Second

// Run starts every Startable singleton, blocks until ctx is canceled, then
// stops the container within defaultShutdownTimeout. It is the orchestration
// layer over Start and Stop: Start failures are returned immediately, a
// graceful shutdown returns nil, and Stop failures are returned as an
// errors.Join aggregate. The container cannot be reused after Run returns.
func (c *Container) Run(ctx context.Context) error {
	if err := c.Start(context.Background()); err != nil {
		return err
	}
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	return c.Stop(shutdownCtx)
}

// RunUntilSignal is Run driven by OS signals: it waits for the given signals
// (default os.Interrupt and syscall.SIGTERM) and then shuts the container down
// gracefully. A second signal during shutdown forces immediate termination.
func (c *Container) RunUntilSignal(signals ...os.Signal) error {
	if len(signals) == 0 {
		signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	return c.Run(ctx)
}
