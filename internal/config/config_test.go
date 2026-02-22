package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaultsAndResolvesRelativeCA(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "certs", "global-bundle.pem")
	if err := os.MkdirAll(filepath.Dir(caPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(caPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	cfgPath := filepath.Join(tmp, "config.yaml")
	content := `
profiles:
  - name: p1
    proxy_user: local_proxy_1
    proxy_password: s3cret
    rds_host: db.example
    rds_region: eu-west-1
    rds_db_user: db_user_1
    ca_bundle: ./certs/global-bundle.pem
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(cfg.Profiles))
	}

	p := cfg.Profiles[0]
	if p.ListenAddr != "127.0.0.1:3307" {
		t.Fatalf("unexpected default listen addr: %s", p.ListenAddr)
	}
	if p.RDSPort != 3306 {
		t.Fatalf("unexpected default rds port: %d", p.RDSPort)
	}
	if p.MaxConns != 20 {
		t.Fatalf("unexpected default max conns: %d", p.MaxConns)
	}
	if p.CABundle != caPath {
		t.Fatalf("expected resolved ca path %s, got %s", caPath, p.CABundle)
	}
}

func TestLoadRejectsDuplicateUsernamesAcrossProfiles(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "ca.pem")
	if err := os.WriteFile(caPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	cfgPath := filepath.Join(tmp, "config.yaml")
	content := `
profiles:
  - name: p1
    listen_addr: "127.0.0.1:3307"
    proxy_user: shared_proxy
    proxy_password: one
    rds_host: db-1
    rds_region: eu-west-1
    rds_db_user: db_user_1
    ca_bundle: ` + caPath + `
  - name: p2
    listen_addr: "127.0.0.1:3308"
    proxy_user: shared_proxy
    proxy_password: two
    rds_host: db-2
    rds_region: eu-west-1
    rds_db_user: db_user_2
    ca_bundle: ` + caPath + `
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for duplicate proxy_user, got nil")
	}
	if !strings.Contains(err.Error(), "proxy_user") {
		t.Fatalf("expected proxy_user validation error, got: %v", err)
	}
}

func TestValidateRuntimeRejectsNonLoopback(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "ca.pem")
	if err := os.WriteFile(caPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	p := Profile{
		Name:          "p1",
		ListenAddr:    "0.0.0.0:3307",
		ProxyUser:     "local_proxy_1",
		ProxyPassword: "secret",
		RDSHost:       "db.example",
		RDSPort:       3306,
		RDSRegion:     "eu-west-1",
		RDSDBUser:     "db_user_1",
		CABundle:      caPath,
		MaxConns:      20,
	}
	err := p.ValidateRuntime(false)
	if err == nil {
		t.Fatal("expected non-loopback validation error")
	}
	if !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectProfileAmbiguous(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Profiles: []Profile{
			{Name: "p1"},
			{Name: "p2"},
		},
	}
	_, err := SelectProfile(cfg, "")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "multiple profiles configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateProfileMaxConnsLimit(t *testing.T) {
	t.Parallel()

	p := Profile{
		Name:          "p",
		ListenAddr:    "127.0.0.1:3307",
		MaxConns:      MaxConnsHardLimit() + 1,
		ProxyUser:     "local_proxy_1",
		ProxyPassword: "pw",
		RDSHost:       "db",
		RDSRegion:     "eu-west-1",
		RDSDBUser:     "db_user_1",
		CABundle:      "/tmp/ca.pem",
	}
	if err := validateProfile(p); err == nil {
		t.Fatal("expected max_conns validation error")
	}
}

func TestResolveConfigPathFallsBackToExecutableDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cwd := filepath.Join(tmp, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	exeDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatalf("mkdir exeDir: %v", err)
	}
	exePath := filepath.Join(exeDir, "rds-iam-proxy")
	if err := os.WriteFile(exePath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write exe: %v", err)
	}
	exeCfg := filepath.Join(exeDir, "config.yaml")
	if err := os.WriteFile(exeCfg, []byte("profiles: []"), 0o644); err != nil {
		t.Fatalf("write exe config: %v", err)
	}

	resolved, err := resolveConfigPathDetailed(
		"",
		func() (string, error) { return cwd, nil },
		func() (string, error) { return exePath, nil },
		func() (string, error) { return filepath.Join(tmp, "home"), nil },
	)
	if err != nil {
		t.Fatalf("resolveConfigPath returned error: %v", err)
	}
	if !samePath(resolved.Path, exeCfg) {
		t.Fatalf("expected %s, got %s", exeCfg, resolved.Path)
	}
	if resolved.Source != "executable directory" {
		t.Fatalf("expected executable directory source, got %s", resolved.Source)
	}
}

func TestResolveConfigPathPrefersCwdOverExecutableDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cwd := filepath.Join(tmp, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	cwdCfg := filepath.Join(cwd, "config.yaml")
	if err := os.WriteFile(cwdCfg, []byte("profiles: []"), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}

	exeDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatalf("mkdir exeDir: %v", err)
	}
	exePath := filepath.Join(exeDir, "rds-iam-proxy")
	if err := os.WriteFile(exePath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write exe: %v", err)
	}
	exeCfg := filepath.Join(exeDir, "config.yaml")
	if err := os.WriteFile(exeCfg, []byte("profiles: []"), 0o644); err != nil {
		t.Fatalf("write exe config: %v", err)
	}

	resolved, err := resolveConfigPathDetailed(
		"",
		func() (string, error) { return cwd, nil },
		func() (string, error) { return exePath, nil },
		func() (string, error) { return filepath.Join(tmp, "home"), nil },
	)
	if err != nil {
		t.Fatalf("resolveConfigPath returned error: %v", err)
	}
	if !samePath(resolved.Path, cwdCfg) {
		t.Fatalf("expected cwd config %s, got %s", cwdCfg, resolved.Path)
	}
}

func TestResolveConfigPathFindsParentDirectoryConfig(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "project")
	child := filepath.Join(projectRoot, "a", "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	parentCfg := filepath.Join(projectRoot, "a", "config.yaml")
	if err := os.WriteFile(parentCfg, []byte("profiles: []"), 0o644); err != nil {
		t.Fatalf("write parent config: %v", err)
	}

	resolved, err := resolveConfigPathDetailed(
		"",
		func() (string, error) { return child, nil },
		func() (string, error) { return filepath.Join(tmp, "bin", "rds-iam-proxy"), nil },
		func() (string, error) { return filepath.Join(tmp, "home"), nil },
	)
	if err != nil {
		t.Fatalf("resolveConfigPathDetailed returned error: %v", err)
	}
	if !samePath(resolved.Path, parentCfg) {
		t.Fatalf("expected parent config %s, got %s", parentCfg, resolved.Path)
	}
	if !strings.Contains(resolved.Source, "parent directory") {
		t.Fatalf("expected parent directory source, got %s", resolved.Source)
	}
}

func TestResolveConfigPathFallsBackToExecutableParentDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	cwd := filepath.Join(tmp, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	exeDir := filepath.Join(tmp, "downloads", "darwin-arm64")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatalf("mkdir exeDir: %v", err)
	}
	exePath := filepath.Join(exeDir, "rds-iam-proxy")
	if err := os.WriteFile(exePath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write exe: %v", err)
	}
	parentCfg := filepath.Join(filepath.Dir(exeDir), "config.yaml")
	if err := os.WriteFile(parentCfg, []byte("profiles: []"), 0o644); err != nil {
		t.Fatalf("write parent config: %v", err)
	}

	resolved, err := resolveConfigPathDetailed(
		"",
		func() (string, error) { return cwd, nil },
		func() (string, error) { return exePath, nil },
		func() (string, error) { return filepath.Join(tmp, "home"), nil },
	)
	if err != nil {
		t.Fatalf("resolveConfigPathDetailed returned error: %v", err)
	}
	if !samePath(resolved.Path, parentCfg) {
		t.Fatalf("expected executable parent config %s, got %s", parentCfg, resolved.Path)
	}
	if !strings.Contains(resolved.Source, "executable parent directory") {
		t.Fatalf("expected executable parent directory source, got %s", resolved.Source)
	}
}
