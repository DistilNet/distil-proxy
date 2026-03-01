package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewManagerDefaultsAndCheckInterval(t *testing.T) {
	m := NewManager(ManagerConfig{})
	if m.cfg.EndpointURL != DefaultVersionEndpoint {
		t.Fatalf("expected default endpoint %q, got %q", DefaultVersionEndpoint, m.cfg.EndpointURL)
	}
	if m.cfg.RollbackWindow != DefaultRollbackWindow {
		t.Fatalf("expected default rollback window %v, got %v", DefaultRollbackWindow, m.cfg.RollbackWindow)
	}
	if m.CheckInterval() != 6*time.Hour {
		t.Fatalf("expected default check interval 6h, got %v", m.CheckInterval())
	}

	custom := NewManager(ManagerConfig{CheckInterval: 45 * time.Minute})
	if custom.CheckInterval() != 45*time.Minute {
		t.Fatalf("expected custom check interval 45m, got %v", custom.CheckInterval())
	}
}

func TestHandleStartupAdditionalPaths(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "distil-proxy")
	backupPath := filepath.Join(root, "distil-proxy.bak")
	statePath := filepath.Join(root, "upgrade.json")
	if err := os.WriteFile(binaryPath, []byte("current"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte("backup"), 0o755); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	manager := NewManager(ManagerConfig{
		CurrentVersion: "1.2.0",
		BinaryPath:     binaryPath,
		BackupPath:     backupPath,
		StatePath:      statePath,
		RollbackWindow: 10 * time.Second,
	})
	manager.now = func() time.Time { return time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC) }

	t.Run("missing-state-file", func(t *testing.T) {
		rolledBack, err := manager.HandleStartup()
		if err != nil {
			t.Fatalf("handle startup: %v", err)
		}
		if rolledBack {
			t.Fatal("did not expect rollback for missing state")
		}
	})

	t.Run("version-mismatch-clears-state", func(t *testing.T) {
		if err := manager.saveState(UpgradeState{
			ToVersion:   "9.9.9",
			StartedOnce: true,
		}); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		rolledBack, err := manager.HandleStartup()
		if err != nil {
			t.Fatalf("handle startup mismatch: %v", err)
		}
		if rolledBack {
			t.Fatal("did not expect rollback for mismatched state version")
		}
		if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected state file removed after mismatch, err=%v", err)
		}
	})

	t.Run("post-window-clears-state-and-backup", func(t *testing.T) {
		if err := os.WriteFile(backupPath, []byte("backup"), 0o755); err != nil {
			t.Fatalf("write backup: %v", err)
		}
		if err := manager.saveState(UpgradeState{
			ToVersion:   "1.2.0",
			StartedOnce: true,
			StartedAt:   manager.now().Add(-2 * manager.cfg.RollbackWindow),
		}); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		rolledBack, err := manager.HandleStartup()
		if err != nil {
			t.Fatalf("handle startup post-window: %v", err)
		}
		if rolledBack {
			t.Fatal("did not expect rollback after rollback window elapsed")
		}
		if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected state file removed, err=%v", err)
		}
		if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected backup removed, err=%v", err)
		}
	})
}

func TestFetchLatestAdditionalPaths(t *testing.T) {
	t.Run("invalid-endpoint-url", func(t *testing.T) {
		m := NewManager(ManagerConfig{EndpointURL: "://bad"})
		if _, err := m.fetchLatest(context.Background()); err == nil {
			t.Fatal("expected invalid endpoint URL error")
		}
	})

	t.Run("http-do-error", func(t *testing.T) {
		m := NewManager(ManagerConfig{
			EndpointURL: "https://example.com/version",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			})},
		})
		if _, err := m.fetchLatest(context.Background()); err == nil || !strings.Contains(err.Error(), "dial failed") {
			t.Fatalf("expected dial failure, got %v", err)
		}
	})

	t.Run("non-200", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusBadGateway)
		}))
		defer ts.Close()
		m := NewManager(ManagerConfig{EndpointURL: ts.URL})
		if _, err := m.fetchLatest(context.Background()); err == nil || !strings.Contains(err.Error(), "returned 502") {
			t.Fatalf("expected non-200 error, got %v", err)
		}
	})

	t.Run("invalid-json", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{invalid"))
		}))
		defer ts.Close()
		m := NewManager(ManagerConfig{EndpointURL: ts.URL})
		if _, err := m.fetchLatest(context.Background()); err == nil {
			t.Fatal("expected decode error")
		}
	})

	t.Run("invalid-payload", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.2.0"})
		}))
		defer ts.Close()
		m := NewManager(ManagerConfig{EndpointURL: ts.URL})
		if _, err := m.fetchLatest(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid release payload") {
			t.Fatalf("expected invalid payload error, got %v", err)
		}
	})

	t.Run("success-and-query-normalization", func(t *testing.T) {
		var gotAPIKey, gotOS, gotArch string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAPIKey = r.Header.Get("X-Distil-Key")
			gotOS = r.URL.Query().Get("os")
			gotArch = r.URL.Query().Get("arch")
			_ = json.NewEncoder(w).Encode(ReleaseInfo{
				Version:        "1.2.0",
				DownloadURL:    "https://example.com/bin",
				ChecksumSHA256: strings.Repeat("a", 64),
			})
		}))
		defer ts.Close()

		m := NewManager(ManagerConfig{
			EndpointURL: ts.URL,
			APIKey:      "dk_test",
			OS:          " DARWIN ",
			Arch:        "x86_64",
		})
		info, err := m.fetchLatest(context.Background())
		if err != nil {
			t.Fatalf("fetch latest: %v", err)
		}
		if info.Version != "1.2.0" {
			t.Fatalf("unexpected release info: %+v", info)
		}
		if gotAPIKey != "dk_test" {
			t.Fatalf("expected API key header, got %q", gotAPIKey)
		}
		if gotOS != "darwin" || gotArch != "amd64" {
			t.Fatalf("expected normalized os/arch, got os=%q arch=%q", gotOS, gotArch)
		}
	})
}

func TestStateAndFileHelperPaths(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "upgrade.json")
	m := NewManager(ManagerConfig{StatePath: statePath})

	state := UpgradeState{ToVersion: "1.2.0", StartedOnce: true}
	if err := m.saveState(state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if _, err := os.Stat(statePath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected state tmp file to be replaced, err=%v", err)
	}
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if stateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 state permissions, got %o", stateInfo.Mode().Perm())
	}
	loaded, err := m.loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.ToVersion != "1.2.0" || !loaded.StartedOnce {
		t.Fatalf("unexpected loaded state: %+v", loaded)
	}
	if err := m.clearState(); err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected cleared state file, err=%v", err)
	}

	if err := os.WriteFile(statePath, []byte("{bad"), 0o600); err != nil {
		t.Fatalf("write bad state: %v", err)
	}
	if _, err := m.loadState(); err == nil {
		t.Fatal("expected invalid json state load error")
	}
	if err := m.saveState(UpgradeState{ToVersion: "1.3.0", StartedOnce: false}); err != nil {
		t.Fatalf("replace bad state: %v", err)
	}
	loaded, err = m.loadState()
	if err != nil {
		t.Fatalf("load replaced state: %v", err)
	}
	if loaded.ToVersion != "1.3.0" || loaded.StartedOnce {
		t.Fatalf("unexpected replaced state: %+v", loaded)
	}
	if _, err := os.Stat(statePath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no leftover tmp state file, err=%v", err)
	}

	contentPath := filepath.Join(root, "content.bin")
	if err := os.WriteFile(contentPath, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write content file: %v", err)
	}
	sum, err := fileSHA256(contentPath)
	if err != nil {
		t.Fatalf("sha256: %v", err)
	}
	expected := sha256.Sum256([]byte("abc"))
	if sum != hex.EncodeToString(expected[:]) {
		t.Fatalf("unexpected checksum %q", sum)
	}
	if _, err := fileSHA256(filepath.Join(root, "missing")); err == nil {
		t.Fatal("expected checksum error for missing file")
	}

	if got := normalizeArch(" aarch64 "); got != "arm64" {
		t.Fatalf("expected arm64, got %q", got)
	}
	if got := normalizeArch("armv7"); got != "armv7" {
		t.Fatalf("expected passthrough arch, got %q", got)
	}
}

func TestDownloadAndCopyReplaceHelpers(t *testing.T) {
	t.Run("download-invalid-url", func(t *testing.T) {
		if err := downloadToFile(context.Background(), http.DefaultClient, "://bad", filepath.Join(t.TempDir(), "bin")); err == nil {
			t.Fatal("expected invalid URL error")
		}
	})

	t.Run("download-client-error", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		})}
		if err := downloadToFile(context.Background(), client, "https://example.com/bin", filepath.Join(t.TempDir(), "bin")); err == nil {
			t.Fatal("expected client error")
		}
	})

	t.Run("download-non-200", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad", http.StatusTeapot)
		}))
		defer ts.Close()
		if err := downloadToFile(context.Background(), http.DefaultClient, ts.URL, filepath.Join(t.TempDir(), "bin")); err == nil || !strings.Contains(err.Error(), "download failed: 418") {
			t.Fatalf("expected non-200 download error, got %v", err)
		}
	})

	t.Run("download-open-file-error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("binary"))
		}))
		defer ts.Close()
		dst := filepath.Join(t.TempDir(), "missing", "dir", "bin")
		if err := downloadToFile(context.Background(), http.DefaultClient, ts.URL, dst); err == nil {
			t.Fatal("expected open file error")
		}
	})

	t.Run("download-success", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("binary"))
		}))
		defer ts.Close()
		dst := filepath.Join(t.TempDir(), "bin")
		if err := downloadToFile(context.Background(), http.DefaultClient, ts.URL, dst); err != nil {
			t.Fatalf("download success expected, got %v", err)
		}
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read downloaded file: %v", err)
		}
		if string(data) != "binary" {
			t.Fatalf("unexpected downloaded content %q", string(data))
		}
	})

	t.Run("copy-and-replace-branches", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src.bin")
		dst := filepath.Join(root, "dst.bin")
		if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
			t.Fatalf("write src: %v", err)
		}
		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copy file: %v", err)
		}
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(data) != "payload" {
			t.Fatalf("unexpected dst payload %q", string(data))
		}
		if err := copyFile(filepath.Join(root, "missing"), dst); err == nil {
			t.Fatal("expected copy error for missing src")
		}

		src2 := filepath.Join(root, "replace-src")
		if err := os.WriteFile(src2, []byte("new"), 0o600); err != nil {
			t.Fatalf("write replace src: %v", err)
		}
		dirDst := filepath.Join(root, "replace-dst")
		if err := os.Mkdir(dirDst, 0o700); err != nil {
			t.Fatalf("mkdir replace dst dir: %v", err)
		}
		if err := replaceFile(src2, dirDst); err != nil {
			t.Fatalf("expected replace fallback success, got %v", err)
		}
		replaced, err := os.ReadFile(dirDst)
		if err != nil {
			t.Fatalf("read replaced file: %v", err)
		}
		if string(replaced) != "new" {
			t.Fatalf("unexpected replaced content %q", string(replaced))
		}

		if err := replaceFile(filepath.Join(root, "missing-src"), filepath.Join(root, "missing-dst")); err == nil {
			t.Fatal("expected replace error for missing src")
		}
	})
}

func TestCheckAndUpgradeAdditionalPaths(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "distil-proxy")
	tempPath := filepath.Join(root, "distil-proxy.new")
	backupPath := filepath.Join(root, "distil-proxy.bak")
	statePath := filepath.Join(root, "upgrade.json")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}

	t.Run("no-new-version", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ReleaseInfo{
				Version:        "1.1.0",
				DownloadURL:    "https://example.com/bin",
				ChecksumSHA256: strings.Repeat("a", 64),
			})
		}))
		defer ts.Close()

		m := NewManager(ManagerConfig{
			Enabled:        true,
			CurrentVersion: "1.1.0",
			BinaryPath:     binaryPath,
			TempBinaryPath: tempPath,
			BackupPath:     backupPath,
			StatePath:      statePath,
			EndpointURL:    ts.URL,
		})
		result, err := m.CheckAndUpgrade(context.Background())
		if err != nil {
			t.Fatalf("check and upgrade: %v", err)
		}
		if result.Applied || result.AvailableVersion != "" {
			t.Fatalf("expected no upgrade result, got %+v", result)
		}
	})

	t.Run("disabled-reports-availability", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ReleaseInfo{
				Version:        "1.2.0",
				DownloadURL:    "https://example.com/bin",
				ChecksumSHA256: strings.Repeat("a", 64),
			})
		}))
		defer ts.Close()

		m := NewManager(ManagerConfig{
			Enabled:        false,
			CurrentVersion: "1.1.0",
			BinaryPath:     binaryPath,
			TempBinaryPath: tempPath,
			BackupPath:     backupPath,
			StatePath:      statePath,
			EndpointURL:    ts.URL,
		})
		result, err := m.CheckAndUpgrade(context.Background())
		if err != nil {
			t.Fatalf("check and upgrade disabled: %v", err)
		}
		if result.AvailableVersion != "1.2.0" || result.Applied {
			t.Fatalf("expected available-only result, got %+v", result)
		}
	})

	t.Run("download-failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ReleaseInfo{
				Version:        "1.2.0",
				DownloadURL:    "://bad-url",
				ChecksumSHA256: strings.Repeat("a", 64),
			})
		}))
		defer ts.Close()

		m := NewManager(ManagerConfig{
			Enabled:        true,
			CurrentVersion: "1.1.0",
			BinaryPath:     binaryPath,
			TempBinaryPath: tempPath,
			BackupPath:     backupPath,
			StatePath:      statePath,
			EndpointURL:    ts.URL,
		})
		if _, err := m.CheckAndUpgrade(context.Background()); err == nil {
			t.Fatal("expected download failure")
		}
	})
}
