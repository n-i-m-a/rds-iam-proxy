package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, formatSignalMessage(time.Now(), "interrupt received, starting graceful shutdown (press Ctrl+C again to force exit)"))
		cancel()
		// If graceful shutdown is blocked, a second Ctrl+C forces immediate exit.
		<-sigCh
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, formatSignalMessage(time.Now(), "second interrupt received, forcing exit"))
		os.Exit(130)
	}()

	return ctx, func() {
		signal.Stop(sigCh)
		cancel()
	}
}

func formatSignalMessage(ts time.Time, msg string) string {
	return fmt.Sprintf("%s %s", ts.Format(time.RFC3339), msg)
}
