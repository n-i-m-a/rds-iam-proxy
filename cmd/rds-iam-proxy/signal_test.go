package main

import (
	"strings"
	"testing"
	"time"
)

func TestFormatSignalMessage(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 2, 22, 12, 34, 56, 0, time.UTC)
	got := formatSignalMessage(ts, "interrupt received, starting graceful shutdown")

	if !strings.HasPrefix(got, "2026-02-22T12:34:56Z ") {
		t.Fatalf("expected RFC3339 timestamp prefix, got: %s", got)
	}
	if !strings.HasSuffix(got, "interrupt received, starting graceful shutdown") {
		t.Fatalf("expected message suffix, got: %s", got)
	}
}
