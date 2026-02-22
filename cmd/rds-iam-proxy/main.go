package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"rds-iam-proxy/internal/config"
	"rds-iam-proxy/internal/proxy"
	"rds-iam-proxy/internal/token"
)

func main() {
	var (
		configPath        string
		profileName       string
		profilesCSV       string
		allProfiles       bool
		verbose           bool
		logLevel          string
		dryRun            bool
		allowDevEmptyPass bool
		poolSize          int
		maxConns          int
		shutdownTimeout   time.Duration
		connectTimeout    time.Duration
	)

	flag.StringVar(&configPath, "config", "", "Path to config YAML")
	flag.StringVar(&profileName, "profile", "", "Profile name from config")
	flag.StringVar(&profilesCSV, "profiles", "", "Comma-separated profile names to run together")
	flag.BoolVar(&allProfiles, "all-profiles", false, "Run all configured profiles")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose structured logs")
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug|info|warn|error")
	flag.BoolVar(&dryRun, "dry-run", false, "Generate IAM token metadata and exit")
	flag.BoolVar(&allowDevEmptyPass, "allow-dev-empty-password", false, "Allow empty proxy_password (dev only)")
	flag.IntVar(&poolSize, "pool-size", 5, "Number of pre-warmed backend connections")
	flag.IntVar(&maxConns, "max-conns", 0, "Override max concurrent client connections (default uses profile max_conns or 100)")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", 30*time.Second, "Graceful shutdown timeout")
	flag.DurationVar(&connectTimeout, "connect-timeout", 8*time.Second, "Backend connect timeout")
	flag.Parse()

	logger := newLogger(logLevel, verbose)

	if maxConns > config.MaxConnsHardLimit() {
		logger.Error("max-conns override too high", "max_conns", maxConns, "hard_limit", config.MaxConnsHardLimit())
		os.Exit(1)
	}
	if countProvided(profileName, profilesCSV, allProfiles) > 1 {
		logger.Error("flags conflict: use only one of --profile, --profiles, or --all-profiles")
		os.Exit(1)
	}

	cfgResolution, err := config.ResolveConfigPathDetailed(configPath)
	if err != nil {
		logger.Error("resolve config", "error", err)
		os.Exit(1)
	}
	cfgPath := cfgResolution.Path
	logger.Info("config resolved", "path", cfgPath, "source", cfgResolution.Source)
	for _, checked := range cfgResolution.Checked {
		logger.Debug("config lookup checked", "path", checked)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("load config", "error", err, "path", cfgPath)
		os.Exit(1)
	}

	selected, err := resolveSelectedProfiles(cfg, profileName, profilesCSV, allProfiles)
	if err != nil {
		logger.Error("select profiles", "error", err)
		os.Exit(1)
	}
	if err := validateUniqueListenAddrs(selected); err != nil {
		logger.Error("listen address validation failed", "error", err)
		os.Exit(1)
	}

	for _, prof := range selected {
		if err := prof.ValidateRuntime(allowDevEmptyPass); err != nil {
			logger.Error("profile validation failed", "profile", prof.Name, "error", err)
			os.Exit(1)
		}
	}

	tokenCache := token.New(5*time.Minute, 15*time.Minute)

	if dryRun {
		runDryRun(logger, tokenCache, selected)
		return
	}

	ctx, stop := signalContext()
	defer stop()

	var (
		wg    sync.WaitGroup
		errCh = make(chan error, len(selected))
	)
	for _, prof := range selected {
		current := prof
		backendFactory, err := proxy.NewBackendFactory(current, tokenCache, connectTimeout)
		if err != nil {
			logger.Error("backend factory init failed", "profile", current.Name, "error", err)
			os.Exit(1)
		}
		pool := proxy.NewBackendPool(poolSize, 14*time.Minute, connectTimeout, logger.With("profile", current.Name), backendFactory.NewConn)
		pool.Start(ctx)

		resolvedMaxConns := current.MaxConns
		if maxConns > 0 {
			resolvedMaxConns = maxConns
		}
		instance := proxy.New(current, logger.With("profile", current.Name), pool, shutdownTimeout, resolvedMaxConns)

		wg.Add(1)
		go func(pf config.Profile, px *proxy.Proxy) {
			defer wg.Done()
			if err := px.Run(ctx); err != nil {
				errCh <- fmt.Errorf("profile %s: %w", pf.Name, err)
				stop()
			}
		}(current, instance)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case err := <-errCh:
		logger.Error("proxy stopped with error", "error", err)
		os.Exit(1)
	case <-done:
		return
	}
}

func runDryRun(logger *slog.Logger, cache *token.Cache, profiles []config.Profile) {
	for _, p := range profiles {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tok, err := cache.Get(ctx, p)
		cancel()
		if err != nil {
			logger.Error("dry-run failed", "profile", p.Name, "error", err)
			os.Exit(1)
		}

		sum := sha256.Sum256([]byte(tok.Value))
		fmt.Printf("profile=%s token_len=%d token_sha256_prefix=%s expires_at=%s\n",
			p.Name,
			len(tok.Value),
			hex.EncodeToString(sum[:])[:12],
			tok.ExpiresAt.Format(time.RFC3339),
		)
	}
}

func resolveSelectedProfiles(cfg *config.Config, profileName, profilesCSV string, allProfiles bool) ([]config.Profile, error) {
	switch {
	case profileName != "":
		p, err := config.SelectProfile(cfg, profileName)
		if err != nil {
			return nil, err
		}
		return []config.Profile{*p}, nil
	case profilesCSV != "":
		names := splitCSV(profilesCSV)
		if len(names) == 0 {
			return nil, errors.New("--profiles provided but empty")
		}
		return selectByNames(cfg, names)
	case allProfiles:
		return cloneProfiles(cfg.Profiles), nil
	default:
		if len(cfg.Profiles) == 1 {
			return cloneProfiles(cfg.Profiles), nil
		}
		if !isInteractiveTerminal() {
			return nil, errors.New("multiple profiles configured; pass --profile, --profiles, or --all-profiles")
		}
		return interactiveSelectProfiles(cfg.Profiles)
	}
}

func interactiveSelectProfiles(profiles []config.Profile) ([]config.Profile, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Select startup mode:")
	fmt.Println("  1) Run one profile")
	fmt.Println("  2) Run multiple profiles")
	fmt.Println("  3) Run all profiles")
	fmt.Print("Choice [1/2/3]: ")

	choiceRaw, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read choice: %w", err)
	}
	choice := strings.TrimSpace(choiceRaw)
	if choice == "" {
		choice = "1"
	}

	fmt.Println("Available profiles:")
	for i, p := range profiles {
		fmt.Printf("  %d) %s (%s)\n", i+1, p.Name, p.ListenAddr)
	}

	switch choice {
	case "1":
		fmt.Print("Select profile number: ")
		raw, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read profile number: %w", err)
		}
		idx, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || idx < 1 || idx > len(profiles) {
			return nil, errors.New("invalid profile selection")
		}
		return []config.Profile{profiles[idx-1]}, nil
	case "2":
		fmt.Print("Select profile numbers (comma-separated, e.g. 1,3): ")
		raw, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read profile list: %w", err)
		}
		parts := splitCSV(raw)
		if len(parts) == 0 {
			return nil, errors.New("no profiles selected")
		}
		seen := map[int]struct{}{}
		out := make([]config.Profile, 0, len(parts))
		for _, part := range parts {
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 1 || idx > len(profiles) {
				return nil, fmt.Errorf("invalid profile index: %s", part)
			}
			if _, ok := seen[idx]; ok {
				continue
			}
			seen[idx] = struct{}{}
			out = append(out, profiles[idx-1])
		}
		return out, nil
	case "3":
		return cloneProfiles(profiles), nil
	default:
		return nil, errors.New("invalid choice; expected 1, 2, or 3")
	}
}

func validateUniqueListenAddrs(profiles []config.Profile) error {
	seen := map[string]string{}
	for _, p := range profiles {
		if prev, ok := seen[p.ListenAddr]; ok {
			return fmt.Errorf("listen_addr %q is reused by profiles %q and %q", p.ListenAddr, prev, p.Name)
		}
		seen[p.ListenAddr] = p.Name
	}
	return nil
}

func selectByNames(cfg *config.Config, names []string) ([]config.Profile, error) {
	index := make(map[string]config.Profile, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		index[p.Name] = p
	}
	out := make([]config.Profile, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		p, ok := index[name]
		if !ok {
			return nil, fmt.Errorf("profile %q not found", name)
		}
		seen[name] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

func cloneProfiles(in []config.Profile) []config.Profile {
	out := make([]config.Profile, len(in))
	copy(out, in)
	return out
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func isInteractiveTerminal() bool {
	in, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	out, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (in.Mode()&os.ModeCharDevice) != 0 && (out.Mode()&os.ModeCharDevice) != 0
}

func countProvided(profileName, profilesCSV string, allProfiles bool) int {
	count := 0
	if strings.TrimSpace(profileName) != "" {
		count++
	}
	if strings.TrimSpace(profilesCSV) != "" {
		count++
	}
	if allProfiles {
		count++
	}
	return count
}

func newLogger(levelText string, verbose bool) *slog.Logger {
	return newLoggerWithWriter(levelText, verbose, os.Stdout)
}

func newLoggerWithWriter(levelText string, verbose bool, out io.Writer) *slog.Logger {
	var level slog.Level
	switch levelText {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: verbose,
	}
	if !verbose {
		opts.ReplaceAttr = func(_ []string, a slog.Attr) slog.Attr {
			// Keep default logs compact and human-readable, while preserving timestamp.
			if a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return a
		}
	}
	return slog.New(slog.NewTextHandler(out, opts))
}
