package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckAndUpgradeSuccess(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "distil-proxy")
	tempPath := filepath.Join(root, "distil-proxy.new")
	backupPath := filepath.Join(root, "distil-proxy.bak")
	statePath := filepath.Join(root, "upgrade.json")

	oldBinary := []byte("old-binary")
	newBinary := []byte("new-binary")
	if err := os.WriteFile(binaryPath, oldBinary, 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}

	checksum := sha256.Sum256(newBinary)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(ReleaseInfo{
				Version:        "1.2.0",
				DownloadURL:    server.URL + "/binary",
				ChecksumSHA256: hex.EncodeToString(checksum[:]),
			})
		case "/binary":
			_, _ = w.Write(newBinary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := NewManager(ManagerConfig{
		Enabled:        true,
		APIKey:         "dk_test",
		CurrentVersion: "1.1.0",
		BinaryPath:     binaryPath,
		TempBinaryPath: tempPath,
		BackupPath:     backupPath,
		StatePath:      statePath,
		EndpointURL:    server.URL + "/version",
		OS:             "darwin",
		Arch:           "arm64",
	})

	result, err := manager.CheckAndUpgrade(context.Background())
	if err != nil {
		t.Fatalf("check and upgrade: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected upgrade to be applied")
	}

	installed, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(installed) != string(newBinary) {
		t.Fatalf("unexpected installed binary: %s", string(installed))
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup binary: %v", err)
	}
	if string(backup) != string(oldBinary) {
		t.Fatalf("unexpected backup binary: %s", string(backup))
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected state file to exist: %v", err)
	}
}

func TestCheckAndUpgradeChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "distil-proxy")
	tempPath := filepath.Join(root, "distil-proxy.new")
	backupPath := filepath.Join(root, "distil-proxy.bak")
	statePath := filepath.Join(root, "upgrade.json")

	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(ReleaseInfo{
				Version:        "1.2.0",
				DownloadURL:    server.URL + "/binary",
				ChecksumSHA256: "badchecksum",
			})
		case "/binary":
			_, _ = w.Write([]byte("new-binary"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := NewManager(ManagerConfig{
		Enabled:        true,
		CurrentVersion: "1.1.0",
		BinaryPath:     binaryPath,
		TempBinaryPath: tempPath,
		BackupPath:     backupPath,
		StatePath:      statePath,
		EndpointURL:    server.URL + "/version",
	})

	_, err := manager.CheckAndUpgrade(context.Background())
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestHandleStartupMarksFirstRunAndRollback(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "distil-proxy")
	backupPath := filepath.Join(root, "distil-proxy.bak")
	statePath := filepath.Join(root, "upgrade.json")

	if err := os.WriteFile(binaryPath, []byte("new"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	manager := NewManager(ManagerConfig{
		Enabled:        true,
		CurrentVersion: "1.2.0",
		BinaryPath:     binaryPath,
		BackupPath:     backupPath,
		StatePath:      statePath,
		RollbackWindow: 10 * time.Second,
	})
	manager.now = func() time.Time { return time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC) }

	if err := manager.saveState(UpgradeState{
		UpgradedAt:  manager.now(),
		FromVersion: "1.1.0",
		ToVersion:   "1.2.0",
		StartedOnce: false,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rolledBack, err := manager.HandleStartup()
	if err != nil {
		t.Fatalf("handle startup first run: %v", err)
	}
	if rolledBack {
		t.Fatal("expected first run not to rollback")
	}

	state, err := manager.loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !state.StartedOnce {
		t.Fatal("expected started_once=true after first startup")
	}

	manager.now = func() time.Time { return time.Date(2026, 2, 27, 12, 0, 5, 0, time.UTC) }
	rolledBack, err = manager.HandleStartup()
	if err != nil {
		t.Fatalf("handle startup rollback: %v", err)
	}
	if !rolledBack {
		t.Fatal("expected rollback on rapid restart")
	}

	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read rolled back binary: %v", err)
	}
	if string(content) != "old" {
		t.Fatalf("expected rolled back binary content 'old', got %q", string(content))
	}
}

func TestHandleStartupSkipsRollbackAfterCleanShutdown(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "distil-proxy")
	backupPath := filepath.Join(root, "distil-proxy.bak")
	statePath := filepath.Join(root, "upgrade.json")

	if err := os.WriteFile(binaryPath, []byte("new"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	manager := NewManager(ManagerConfig{
		Enabled:        true,
		CurrentVersion: "1.2.0",
		BinaryPath:     binaryPath,
		BackupPath:     backupPath,
		StatePath:      statePath,
		RollbackWindow: 10 * time.Second,
	})
	manager.now = func() time.Time { return time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC) }

	if err := manager.saveState(UpgradeState{
		UpgradedAt:  manager.now(),
		FromVersion: "1.1.0",
		ToVersion:   "1.2.0",
		StartedOnce: false,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rolledBack, err := manager.HandleStartup()
	if err != nil {
		t.Fatalf("handle startup first run: %v", err)
	}
	if rolledBack {
		t.Fatal("expected first run not to rollback")
	}
	if err := manager.MarkCleanShutdown(); err != nil {
		t.Fatalf("mark clean shutdown: %v", err)
	}

	manager.now = func() time.Time { return time.Date(2026, 2, 27, 12, 0, 5, 0, time.UTC) }
	rolledBack, err = manager.HandleStartup()
	if err != nil {
		t.Fatalf("handle startup after clean shutdown: %v", err)
	}
	if rolledBack {
		t.Fatal("did not expect rollback after clean shutdown")
	}

	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary after clean restart: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("expected binary content 'new' after clean restart, got %q", string(content))
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected upgrade state cleared after clean restart, err=%v", err)
	}
}
