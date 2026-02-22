package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultListenAddr = "127.0.0.1:3307"
	defaultRDSPort    = 3306
	defaultMaxConns   = 20
	maxConnsHardLimit = 200
)

type Config struct {
	Profiles []Profile `yaml:"profiles"`
}

type Profile struct {
	Name          string `yaml:"name"`
	ListenAddr    string `yaml:"listen_addr"`
	MaxConns      int    `yaml:"max_conns"`
	ProxyUser     string `yaml:"proxy_user"`
	ProxyPassword string `yaml:"proxy_password"`
	RDSHost       string `yaml:"rds_host"`
	RDSPort       int    `yaml:"rds_port"`
	RDSRegion     string `yaml:"rds_region"`
	RDSDBUser     string `yaml:"rds_db_user"`
	AWSProfile    string `yaml:"aws_profile"`
	DefaultDB     string `yaml:"default_db"`
	CABundle      string `yaml:"ca_bundle"`
}

type ConfigResolution struct {
	Path    string
	Source  string
	Checked []string
}

func ResolveConfigPath(flagPath string) (string, error) {
	res, err := ResolveConfigPathDetailed(flagPath)
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

func ResolveConfigPathDetailed(flagPath string) (ConfigResolution, error) {
	return resolveConfigPathDetailed(flagPath, os.Getwd, os.Executable, os.UserHomeDir)
}

func resolveConfigPathDetailed(
	flagPath string,
	getwd func() (string, error),
	executablePath func() (string, error),
	homeDir func() (string, error),
) (ConfigResolution, error) {
	resolution := ConfigResolution{
		Checked: make([]string, 0, 16),
	}

	if flagPath != "" {
		absPath, err := filepath.Abs(flagPath)
		if err != nil {
			return ConfigResolution{}, err
		}
		resolution.Path = absPath
		resolution.Source = "flag --config"
		resolution.Checked = append(resolution.Checked, absPath)
		return resolution, nil
	}

	if getwd != nil {
		wd, err := getwd()
		if err == nil && wd != "" {
			for _, dir := range cwdAndSingleParent(wd) {
				p := filepath.Join(dir, "config.yaml")
				resolution.Checked = append(resolution.Checked, p)
				if fileExists(p) {
					resolution.Path = p
					if samePath(dir, wd) {
						resolution.Source = "current working directory"
					} else {
						resolution.Source = fmt.Sprintf("parent directory (%s)", dir)
					}
					return resolution, nil
				}
			}
		}
	}

	if executablePath != nil {
		exePath, err := executablePath()
		if err == nil && exePath != "" {
			exeDir := filepath.Dir(exePath)
			for _, dir := range cwdAndSingleParent(exeDir) {
				p := filepath.Join(dir, "config.yaml")
				resolution.Checked = append(resolution.Checked, p)
				if fileExists(p) {
					resolution.Path = p
					if samePath(dir, exeDir) {
						resolution.Source = "executable directory"
					} else {
						resolution.Source = fmt.Sprintf("executable parent directory (%s)", dir)
					}
					return resolution, nil
				}
			}
		}
	}

	home := ""
	if homeDir != nil {
		home, _ = homeDir()
	}
	if home != "" {
		p := filepath.Join(home, ".config", "rds-iam-proxy", "config.yaml")
		resolution.Checked = append(resolution.Checked, p)
		if fileExists(p) {
			resolution.Path = p
			resolution.Source = "home config"
			return resolution, nil
		}
	}

	return ConfigResolution{}, fmt.Errorf(
		"config file not found; checked: %s; use --config <path> or create config.yaml in cwd/cwd-parent, executable-dir/executable-parent, or ~/.config/rds-iam-proxy/config.yaml",
		strings.Join(resolution.Checked, ", "),
	)
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if len(cfg.Profiles) == 0 {
		return nil, errors.New("config has no profiles")
	}

	baseDir := filepath.Dir(path)
	for i := range cfg.Profiles {
		applyDefaults(&cfg.Profiles[i])
		resolveRelativePaths(&cfg.Profiles[i], baseDir)
		if err := validateProfile(cfg.Profiles[i]); err != nil {
			return nil, fmt.Errorf("profile %q: %w", cfg.Profiles[i].Name, err)
		}
	}
	if err := validateUniqueUsernames(cfg.Profiles); err != nil {
		return nil, err
	}

	return cfg, nil
}

func SelectProfile(cfg *Config, selected string) (*Profile, error) {
	if selected != "" {
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == selected {
				return &cfg.Profiles[i], nil
			}
		}
		return nil, fmt.Errorf("profile %q not found", selected)
	}

	if len(cfg.Profiles) == 1 {
		return &cfg.Profiles[0], nil
	}

	names := make([]string, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		names = append(names, p.Name)
	}
	return nil, fmt.Errorf("multiple profiles configured; pass --profile <name>. available: %s", strings.Join(names, ", "))
}

func (p Profile) Address() string {
	return net.JoinHostPort(p.RDSHost, fmt.Sprintf("%d", p.RDSPort))
}

func (p Profile) ValidateRuntime(allowDevEmptyPassword bool) error {
	if p.ProxyPassword == "" && !allowDevEmptyPassword {
		return errors.New("proxy_password is empty")
	}
	if p.ProxyPassword == "change-me" || p.ProxyPassword == "change-me-too" {
		return errors.New("proxy_password must not use example default value")
	}
	if !isLoopbackAddr(p.ListenAddr) {
		return fmt.Errorf("listen_addr %q is not loopback", p.ListenAddr)
	}
	if _, err := os.Stat(p.CABundle); err != nil {
		return fmt.Errorf("ca_bundle not readable: %w", err)
	}
	return nil
}

func applyDefaults(p *Profile) {
	if p.ListenAddr == "" {
		p.ListenAddr = defaultListenAddr
	}
	if p.RDSPort == 0 {
		p.RDSPort = defaultRDSPort
	}
	if p.MaxConns == 0 {
		p.MaxConns = defaultMaxConns
	}
}

func resolveRelativePaths(p *Profile, baseDir string) {
	if p.CABundle != "" && !filepath.IsAbs(p.CABundle) {
		p.CABundle = filepath.Join(baseDir, p.CABundle)
	}
}

func validateProfile(p Profile) error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.ProxyUser == "" {
		return errors.New("proxy_user is required")
	}
	if p.MaxConns < 1 {
		return errors.New("max_conns must be >= 1")
	}
	if p.MaxConns > maxConnsHardLimit {
		return fmt.Errorf("max_conns must be <= %d", maxConnsHardLimit)
	}
	if p.RDSHost == "" {
		return errors.New("rds_host is required")
	}
	if p.RDSRegion == "" {
		return errors.New("rds_region is required")
	}
	if p.RDSDBUser == "" {
		return errors.New("rds_db_user is required")
	}
	if p.ProxyUser == p.RDSDBUser {
		return errors.New("proxy_user and rds_db_user must be different")
	}
	if p.CABundle == "" {
		return errors.New("ca_bundle is required")
	}
	if _, _, err := net.SplitHostPort(p.ListenAddr); err != nil {
		return fmt.Errorf("invalid listen_addr: %w", err)
	}
	return nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cwdAndSingleParent(start string) []string {
	current := filepath.Clean(start)
	parent := filepath.Dir(current)
	if samePath(parent, current) {
		return []string{current}
	}
	return []string{current, parent}
}

func samePath(a, b string) bool {
	ac := filepath.Clean(a)
	bc := filepath.Clean(b)
	if ac == bc {
		return true
	}
	ar, errA := filepath.EvalSymlinks(ac)
	if errA != nil {
		ar = ac
	}
	br, errB := filepath.EvalSymlinks(bc)
	if errB != nil {
		br = bc
	}
	return filepath.Clean(ar) == filepath.Clean(br)
}

func MaxConnsHardLimit() int {
	return maxConnsHardLimit
}

func validateUniqueUsernames(profiles []Profile) error {
	if len(profiles) < 2 {
		return nil
	}

	proxyUsers := make(map[string]string, len(profiles))

	for _, p := range profiles {
		if prev, ok := proxyUsers[p.ProxyUser]; ok {
			return fmt.Errorf("proxy_user %q is reused by profiles %q and %q; use unique proxy_user values per profile", p.ProxyUser, prev, p.Name)
		}
		proxyUsers[p.ProxyUser] = p.Name
	}

	return nil
}
