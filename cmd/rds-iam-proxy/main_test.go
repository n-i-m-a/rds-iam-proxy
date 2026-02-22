package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"rds-iam-proxy/internal/config"
)

func TestSplitCSV(t *testing.T) {
	t.Parallel()

	got := splitCSV(" one, two ,,three ")
	if len(got) != 3 || got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Fatalf("unexpected split result: %#v", got)
	}
}

func TestCountProvided(t *testing.T) {
	t.Parallel()

	if got := countProvided("p1", "", false); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
	if got := countProvided("p1", "p2", true); got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}

func TestResolveSelectedProfilesByCSV(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Profiles: []config.Profile{
			{Name: "p1", ListenAddr: "127.0.0.1:3307"},
			{Name: "p2", ListenAddr: "127.0.0.1:3308"},
		},
	}

	selected, err := resolveSelectedProfiles(cfg, "", "p1,p2", false)
	if err != nil {
		t.Fatalf("resolveSelectedProfiles: %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("expected 2 selected profiles, got %d", len(selected))
	}
}

func TestResolveSelectedProfilesAll(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Profiles: []config.Profile{
			{Name: "p1"},
			{Name: "p2"},
		},
	}

	selected, err := resolveSelectedProfiles(cfg, "", "", true)
	if err != nil {
		t.Fatalf("resolveSelectedProfiles: %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("expected all profiles selected, got %d", len(selected))
	}
}

func TestValidateUniqueListenAddrs(t *testing.T) {
	t.Parallel()

	err := validateUniqueListenAddrs([]config.Profile{
		{Name: "p1", ListenAddr: "127.0.0.1:3307"},
		{Name: "p2", ListenAddr: "127.0.0.1:3307"},
	})
	if err == nil {
		t.Fatal("expected duplicate listen address error")
	}
	if !strings.Contains(err.Error(), "reused") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewLoggerDefaultModeIsCompactWithTimestamp(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newLoggerWithWriter("info", false, &buf)
	logger.Info("hello world", "k", "v")

	out := buf.String()
	if !strings.Contains(out, "time=") {
		t.Fatalf("expected timestamp in output, got: %s", out)
	}
	if strings.Contains(out, "level=") {
		t.Fatalf("did not expect level in compact output, got: %s", out)
	}
	if !strings.Contains(out, `msg="hello world"`) {
		t.Fatalf("expected message in output, got: %s", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("expected key-value attribute in output, got: %s", out)
	}
}

func TestNewLoggerVerboseModeIncludesLevelAndSource(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newLoggerWithWriter("debug", true, &buf)
	logger.LogAttrs(context.Background(), slog.LevelDebug, "verbose message")

	out := buf.String()
	if !strings.Contains(out, "time=") {
		t.Fatalf("expected timestamp in output, got: %s", out)
	}
	if !strings.Contains(out, "level=DEBUG") {
		t.Fatalf("expected level in verbose output, got: %s", out)
	}
	if !strings.Contains(out, "source=") {
		t.Fatalf("expected source in verbose output, got: %s", out)
	}
}
