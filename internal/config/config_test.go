package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	home := "/tmp/example"
	paths := DefaultPaths(home)

	if paths.RootDir != filepath.Join(home, DirName) {
		t.Fatalf("unexpected root dir: %s", paths.RootDir)
	}
	if paths.ConfigFile != filepath.Join(home, DirName, ConfigFileName) {
		t.Fatalf("unexpected config file: %s", paths.ConfigFile)
	}
	if paths.PIDFile != filepath.Join(home, DirName, PIDFileName) {
		t.Fatalf("unexpected pid file: %s", paths.PIDFile)
	}
	if paths.StatusFile != filepath.Join(home, DirName, StatusFileName) {
		t.Fatalf("unexpected status file: %s", paths.StatusFile)
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	cfg := Config{APIKey: "dk_test"}
	cfg.ApplyDefaults()

	if cfg.Server != DefaultServerURL {
		t.Fatalf("expected default server, got %q", cfg.Server)
	}
	if cfg.TimeoutMS != DefaultTimeoutMS {
		t.Fatalf("expected default timeout, got %d", cfg.TimeoutMS)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Fatalf("expected default log level, got %q", cfg.LogLevel)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid",
			cfg: Config{
				APIKey:    "dk_abc123",
				Server:    DefaultServerURL,
				TimeoutMS: 1000,
				LogLevel:  "info",
			},
			wantErr: false,
		},
		{
			name: "invalid key prefix",
			cfg: Config{
				APIKey:    "dpk_abc123",
				Server:    DefaultServerURL,
				TimeoutMS: 1000,
				LogLevel:  "info",
			},
			wantErr: true,
		},
		{
			name: "missing server",
			cfg: Config{
				APIKey:    "dk_abc123",
				TimeoutMS: 1000,
				LogLevel:  "info",
			},
			wantErr: true,
		},
		{
			name: "invalid timeout",
			cfg: Config{
				APIKey:    "dk_abc123",
				Server:    DefaultServerURL,
				TimeoutMS: 0,
				LogLevel:  "info",
			},
			wantErr: true,
		},
		{
			name: "invalid log level",
			cfg: Config{
				APIKey:    "dk_abc123",
				Server:    DefaultServerURL,
				TimeoutMS: 1000,
				LogLevel:  "verbose",
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	cfg := Config{APIKey: "dk_roundtrip"}
	if err := Save(paths, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := Load(paths)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if loaded.APIKey != cfg.APIKey {
		t.Fatalf("expected api key %q, got %q", cfg.APIKey, loaded.APIKey)
	}
	if loaded.Server != DefaultServerURL {
		t.Fatalf("expected default server %q, got %q", DefaultServerURL, loaded.Server)
	}
	if loaded.TimeoutMS != DefaultTimeoutMS {
		t.Fatalf("expected default timeout %d, got %d", DefaultTimeoutMS, loaded.TimeoutMS)
	}
	if loaded.LogLevel != DefaultLogLevel {
		t.Fatalf("expected default log level %q, got %q", DefaultLogLevel, loaded.LogLevel)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(paths.ConfigFile)
		if err != nil {
			t.Fatalf("stat config file: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("expected config file mode 0600, got %o", perm)
		}
	}
}

func TestEnsureStateDirs(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	if err := EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure state dirs: %v", err)
	}

	for _, dir := range []string{paths.RootDir, paths.BinDir, paths.LogsDir} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("expected directory %s to exist", dir)
		}
	}
}

func TestLoadConfigNotFound(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	_, err := Load(paths)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound, got %v", err)
	}
}

func TestLoadDisallowUnknownFields(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	if err := EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure state dirs: %v", err)
	}

	payload := []byte(`{"api_key":"dk_abc","unknown":true}`)
	if err := os.WriteFile(paths.ConfigFile, payload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(paths)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestLoadLegacyProxyKeyFails(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	if err := EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure state dirs: %v", err)
	}

	payload := []byte(`{"proxy_key":"dpk_abc123","server":"wss://proxy.distil.net/ws"}`)
	if err := os.WriteFile(paths.ConfigFile, payload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(paths)
	if err == nil {
		t.Fatal("expected validation error for legacy proxy_key")
	}
}

func TestLoadRejectsTrailingTopLevelJSON(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	if err := EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure state dirs: %v", err)
	}

	payload := []byte(`{"api_key":"dk_abc"}{"api_key":"dk_extra"}`)
	if err := os.WriteFile(paths.ConfigFile, payload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(paths)
	if err == nil {
		t.Fatal("expected decode error for trailing JSON")
	}
	if !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("expected trailing data error, got %v", err)
	}
}

func TestDetectPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths, err := DetectPaths()
	if err != nil {
		t.Fatalf("detect paths: %v", err)
	}
	if paths.HomeDir != home {
		t.Fatalf("expected home dir %s, got %s", home, paths.HomeDir)
	}
}

func TestDetectPathsHomeError(t *testing.T) {
	orig := userHomeDir
	userHomeDir = func() (string, error) {
		return "", errors.New("boom")
	}
	defer func() { userHomeDir = orig }()

	_, err := DetectPaths()
	if err == nil || !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("expected detect paths error, got %v", err)
	}
}

func TestValidateAPIKey(t *testing.T) {
	if err := ValidateAPIKey("dk_valid"); err != nil {
		t.Fatalf("expected valid key, got %v", err)
	}
	if err := ValidateAPIKey("dk_"); err == nil {
		t.Fatal("expected error for short key")
	}
	if err := ValidateAPIKey("bad"); err == nil {
		t.Fatal("expected error for invalid prefix")
	}
}

func TestConfigValidateMissingLogLevel(t *testing.T) {
	cfg := Config{
		APIKey:    "dk_test",
		Server:    DefaultServerURL,
		TimeoutMS: 1000,
		LogLevel:  "",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestEnsureStateDirsFailsWhenRootIsFile(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	if err := os.WriteFile(paths.RootDir, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write root collision file: %v", err)
	}

	if err := EnsureStateDirs(paths); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadReadError(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	if err := EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure state dirs: %v", err)
	}
	if err := os.Mkdir(paths.ConfigFile, 0o700); err != nil {
		t.Fatalf("mkdir config path: %v", err)
	}

	_, err := Load(paths)
	if err == nil {
		t.Fatal("expected read error")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveWriteTempConfigError(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	paths.ConfigFile = filepath.Join(paths.RootDir, "missing", "config.json")

	err := Save(paths, Config{APIKey: "dk_test"})
	if err == nil {
		t.Fatal("expected save error")
	}
	if !strings.Contains(err.Error(), "write temp config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveReplaceConfigError(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	paths.ConfigFile = paths.RootDir

	err := Save(paths, Config{APIKey: "dk_test"})
	if err == nil {
		t.Fatal("expected save error")
	}
	if !strings.Contains(err.Error(), "replace config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveClearsLegacyProxyKey(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)

	if err := Save(paths, Config{APIKey: "dk_test", LegacyProxyKey: "dpk_old"}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	content, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if strings.Contains(string(content), "proxy_key") {
		t.Fatalf("expected proxy_key omitted, got %s", string(content))
	}
}

func TestSaveValidationAndDirErrors(t *testing.T) {
	paths := DefaultPaths(t.TempDir())

	if err := Save(paths, Config{APIKey: "bad"}); err == nil {
		t.Fatal("expected validation error")
	}

	paths = DefaultPaths(t.TempDir())
	if err := os.WriteFile(paths.RootDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("write root collision: %v", err)
	}
	if err := Save(paths, Config{APIKey: "dk_test"}); err == nil {
		t.Fatal("expected ensure state dirs error")
	}
}

func TestSaveEncodeAndChmodErrors(t *testing.T) {
	t.Run("encode-error", func(t *testing.T) {
		paths := DefaultPaths(t.TempDir())
		origMarshal := jsonMarshalFunc
		jsonMarshalFunc = func(any, string, string) ([]byte, error) {
			return nil, errors.New("marshal failed")
		}
		defer func() { jsonMarshalFunc = origMarshal }()

		err := Save(paths, Config{APIKey: "dk_test"})
		if err == nil || !strings.Contains(err.Error(), "encode config") {
			t.Fatalf("expected encode error, got %v", err)
		}
	})

	t.Run("chmod-error", func(t *testing.T) {
		paths := DefaultPaths(t.TempDir())
		origChmod := chmodFileFunc
		chmodFileFunc = func(string, os.FileMode) error {
			return errors.New("chmod failed")
		}
		defer func() { chmodFileFunc = origChmod }()

		err := Save(paths, Config{APIKey: "dk_test"})
		if err == nil || !strings.Contains(err.Error(), "chmod config") {
			t.Fatalf("expected chmod error, got %v", err)
		}
	})
}
