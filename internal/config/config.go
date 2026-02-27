package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	userHomeDir     = os.UserHomeDir
	jsonMarshalFunc = json.MarshalIndent
	writeFileFunc   = os.WriteFile
	renameFileFunc  = os.Rename
	chmodFileFunc   = os.Chmod
)

const (
	DirName        = ".distil-proxy"
	ConfigFileName = "config.json"
	BinDirName     = "bin"
	LogsDirName    = "logs"
	LogFileName    = "daemon.log"
	PIDFileName    = "distil-proxy.pid"
	StatusFileName = "status.json"

	DefaultServerURL = "wss://proxy.distil.net/ws"
	DefaultTimeoutMS = 30000
	DefaultLogLevel  = "info"
)

// Config defines persisted daemon configuration.
type Config struct {
	APIKey         string `json:"api_key"`
	LegacyProxyKey string `json:"proxy_key,omitempty"`
	Server         string `json:"server,omitempty"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
	LogLevel       string `json:"log_level,omitempty"`
}

// Paths stores resolved runtime/config file locations.
type Paths struct {
	HomeDir    string
	RootDir    string
	ConfigFile string
	BinDir     string
	LogsDir    string
	LogFile    string
	PIDFile    string
	StatusFile string
}

// ErrConfigNotFound indicates config file is not present yet.
var ErrConfigNotFound = errors.New("config file not found")

// DefaultPaths returns canonical path layout under the supplied home directory.
func DefaultPaths(homeDir string) Paths {
	rootDir := filepath.Join(homeDir, DirName)

	return Paths{
		HomeDir:    homeDir,
		RootDir:    rootDir,
		ConfigFile: filepath.Join(rootDir, ConfigFileName),
		BinDir:     filepath.Join(rootDir, BinDirName),
		LogsDir:    filepath.Join(rootDir, LogsDirName),
		LogFile:    filepath.Join(rootDir, LogsDirName, LogFileName),
		PIDFile:    filepath.Join(rootDir, PIDFileName),
		StatusFile: filepath.Join(rootDir, StatusFileName),
	}
}

// DetectPaths resolves paths using the current user home directory.
func DetectPaths() (Paths, error) {
	home, err := userHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return DefaultPaths(home), nil
}

// ApplyDefaults fills optional config values with defaults.
func (c *Config) ApplyDefaults() {
	if strings.TrimSpace(c.Server) == "" {
		c.Server = DefaultServerURL
	}
	if c.TimeoutMS <= 0 {
		c.TimeoutMS = DefaultTimeoutMS
	}
	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = DefaultLogLevel
	}
}

// Validate enforces required values and simple semantic constraints.
func (c Config) Validate() error {
	if strings.TrimSpace(c.APIKey) == "" && strings.TrimSpace(c.LegacyProxyKey) != "" {
		return errors.New("proxy_key is no longer supported; use api_key with dk_ prefix")
	}
	if err := ValidateAPIKey(c.APIKey); err != nil {
		return err
	}
	if strings.TrimSpace(c.Server) == "" {
		return errors.New("server is required")
	}
	if c.TimeoutMS <= 0 {
		return errors.New("timeout_ms must be > 0")
	}
	if strings.TrimSpace(c.LogLevel) == "" {
		return errors.New("log_level is required")
	}

	return nil
}

// ValidateAPIKey validates the public API key format.
func ValidateAPIKey(key string) error {
	if !strings.HasPrefix(key, "dk_") || len(key) <= len("dk_") {
		return errors.New("api_key must start with dk_")
	}

	return nil
}

// EnsureStateDirs creates runtime directories required by the daemon.
func EnsureStateDirs(paths Paths) error {
	for _, dir := range []string{paths.RootDir, paths.BinDir, paths.LogsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	return nil
}

// Load reads config from disk, applies defaults, and validates the result.
func Load(paths Paths) (Config, error) {
	data, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, ErrConfigNotFound
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// Save persists config atomically and enforces 0600 permissions.
func Save(paths Paths, cfg Config) error {
	cfg.ApplyDefaults()
	cfg.LegacyProxyKey = ""
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if err := EnsureStateDirs(paths); err != nil {
		return err
	}

	payload, err := jsonMarshalFunc(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	payload = append(payload, '\n')

	tmp := paths.ConfigFile + ".tmp"
	if err := writeFileFunc(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := renameFileFunc(tmp, paths.ConfigFile); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if err := chmodFileFunc(paths.ConfigFile, 0o600); err != nil {
		return fmt.Errorf("chmod config: %w", err)
	}

	return nil
}
