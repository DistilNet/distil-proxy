package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultVersionEndpoint = "https://distil.net/api/v1/proxy/version"
	DefaultRollbackWindow  = 10 * time.Second
)

type ManagerConfig struct {
	Enabled        bool
	APIKey         string
	CurrentVersion string
	BinaryPath     string
	TempBinaryPath string
	BackupPath     string
	StatePath      string
	EndpointURL    string
	CheckInterval  time.Duration
	RollbackWindow time.Duration
	OS             string
	Arch           string
	HTTPClient     *http.Client
}

type Manager struct {
	cfg ManagerConfig
	now func() time.Time
}

type ReleaseInfo struct {
	Version        string `json:"version"`
	DownloadURL    string `json:"download_url"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

type UpgradeState struct {
	UpgradedAt    time.Time `json:"upgraded_at"`
	FromVersion   string    `json:"from_version"`
	ToVersion     string    `json:"to_version"`
	StartedOnce   bool      `json:"started_once"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	CleanShutdown bool      `json:"clean_shutdown,omitempty"`
}

type CheckResult struct {
	AvailableVersion string
	Applied          bool
}

func NewManager(cfg ManagerConfig) *Manager {
	if cfg.EndpointURL == "" {
		cfg.EndpointURL = DefaultVersionEndpoint
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.RollbackWindow <= 0 {
		cfg.RollbackWindow = DefaultRollbackWindow
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 6 * time.Hour
	}
	return &Manager{
		cfg: cfg,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (m *Manager) CheckInterval() time.Duration {
	return m.cfg.CheckInterval
}

func (m *Manager) HandleStartup() (bool, error) {
	state, err := m.loadState()
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	if normalizeVersion(state.ToVersion) != normalizeVersion(m.cfg.CurrentVersion) {
		_ = m.clearState()
		return false, nil
	}

	if !state.StartedOnce {
		state.StartedOnce = true
		state.StartedAt = m.now()
		state.CleanShutdown = false
		return false, m.saveState(state)
	}

	if state.CleanShutdown {
		_ = m.clearState()
		_ = os.Remove(m.cfg.BackupPath)
		return false, nil
	}

	if m.now().Sub(state.StartedAt) < m.cfg.RollbackWindow {
		if err := replaceFile(m.cfg.BackupPath, m.cfg.BinaryPath); err != nil {
			return false, fmt.Errorf("rollback replace failed: %w", err)
		}
		_ = m.clearState()
		return true, nil
	}

	_ = m.clearState()
	_ = os.Remove(m.cfg.BackupPath)
	return false, nil
}

func (m *Manager) CheckAndUpgrade(ctx context.Context) (CheckResult, error) {
	release, err := m.fetchLatest(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	needsUpgrade, err := m.needsUpgrade(release)
	if err != nil {
		return CheckResult{}, err
	}
	if !needsUpgrade {
		return CheckResult{}, nil
	}

	result := CheckResult{AvailableVersion: release.Version}
	if !m.cfg.Enabled {
		return result, nil
	}

	if err := downloadToFile(ctx, m.cfg.HTTPClient, release.DownloadURL, m.cfg.TempBinaryPath); err != nil {
		return result, err
	}
	checksum, err := fileSHA256(m.cfg.TempBinaryPath)
	if err != nil {
		return result, err
	}
	if !strings.EqualFold(checksum, strings.TrimSpace(release.ChecksumSHA256)) {
		return result, fmt.Errorf("checksum mismatch")
	}

	if err := copyFile(m.cfg.BinaryPath, m.cfg.BackupPath); err != nil {
		return result, err
	}

	if err := m.saveState(UpgradeState{
		UpgradedAt:  m.now(),
		FromVersion: m.cfg.CurrentVersion,
		ToVersion:   release.Version,
		StartedOnce: false,
	}); err != nil {
		return result, err
	}

	if err := replaceFile(m.cfg.TempBinaryPath, m.cfg.BinaryPath); err != nil {
		return result, err
	}
	if err := os.Chmod(m.cfg.BinaryPath, 0o755); err != nil {
		return result, err
	}

	result.Applied = true
	return result, nil
}

func (m *Manager) needsUpgrade(release ReleaseInfo) (bool, error) {
	if isNewerVersion(m.cfg.CurrentVersion, release.Version) {
		return true, nil
	}

	if normalizeVersion(m.cfg.CurrentVersion) != normalizeVersion(release.Version) {
		return false, nil
	}

	installedChecksum, err := fileSHA256(m.cfg.BinaryPath)
	if err != nil {
		return false, fmt.Errorf("checksum installed binary: %w", err)
	}

	return !strings.EqualFold(installedChecksum, strings.TrimSpace(release.ChecksumSHA256)), nil
}

func (m *Manager) fetchLatest(ctx context.Context) (ReleaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.EndpointURL, nil)
	if err != nil {
		return ReleaseInfo{}, err
	}
	query := req.URL.Query()
	query.Set("os", normalizeOS(m.cfg.OS))
	query.Set("arch", normalizeArch(m.cfg.Arch))
	req.URL.RawQuery = query.Encode()
	if strings.TrimSpace(m.cfg.APIKey) != "" {
		req.Header.Set("X-Distil-Key", m.cfg.APIKey)
	}

	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return ReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{}, fmt.Errorf("version endpoint returned %d", resp.StatusCode)
	}

	var info ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return ReleaseInfo{}, err
	}
	if strings.TrimSpace(info.Version) == "" || strings.TrimSpace(info.DownloadURL) == "" || strings.TrimSpace(info.ChecksumSHA256) == "" {
		return ReleaseInfo{}, fmt.Errorf("invalid release payload")
	}
	return info, nil
}

func (m *Manager) loadState() (UpgradeState, error) {
	data, err := os.ReadFile(m.cfg.StatePath)
	if err != nil {
		return UpgradeState{}, err
	}
	var state UpgradeState
	if err := json.Unmarshal(data, &state); err != nil {
		return UpgradeState{}, err
	}
	return state, nil
}

func (m *Manager) saveState(state UpgradeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := m.cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return replaceFile(tmp, m.cfg.StatePath)
}

func (m *Manager) MarkCleanShutdown() error {
	state, err := m.loadState()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !state.StartedOnce {
		return nil
	}
	if normalizeVersion(state.ToVersion) != normalizeVersion(m.cfg.CurrentVersion) {
		return nil
	}
	if state.CleanShutdown {
		return nil
	}
	state.CleanShutdown = true
	return m.saveState(state)
}

func (m *Manager) clearState() error {
	if err := os.Remove(m.cfg.StatePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func normalizeOS(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeArch(value string) string {
	arch := strings.ToLower(strings.TrimSpace(value))
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func downloadToFile(ctx context.Context, client *http.Client, url string, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %d", resp.StatusCode)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, path)
}

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	tmp := dst + ".tmp"
	target, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, dst)
}

func replaceFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !shouldRetryReplace(err) {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func shouldRetryReplace(err error) bool {
	if os.IsNotExist(err) {
		return false
	}
	return errors.Is(err, syscall.EEXIST) ||
		errors.Is(err, syscall.ENOTEMPTY) ||
		errors.Is(err, syscall.EISDIR) ||
		errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.EPERM)
}
