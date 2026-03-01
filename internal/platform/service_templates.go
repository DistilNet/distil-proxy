package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

const (
	LaunchdServicePath = "Library/LaunchAgents/net.distil.proxy.plist"
	SystemdServicePath = ".config/systemd/user/distil-proxy.service"
)

// LaunchdPlist renders a launchd plist for foreground daemon mode.
func LaunchdPlist(homeDir string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>net.distil.proxy</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>start</string>
    <string>--foreground</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, filepath.Join(homeDir, ".distil-proxy/bin/distil-proxy"), filepath.Join(homeDir, ".distil-proxy/logs/daemon.log"), filepath.Join(homeDir, ".distil-proxy/logs/daemon.log"))
}

// SystemdUserUnit renders a systemd user unit for foreground daemon mode.
func SystemdUserUnit(homeDir string) string {
	execPath := filepath.Join(homeDir, ".distil-proxy/bin/distil-proxy")
	return fmt.Sprintf(`[Unit]
Description=Distil Proxy Daemon
After=network-online.target

[Service]
ExecStart=%s start --foreground
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, strconv.Quote(execPath))
}

// InstallServiceDefinition writes and enables the local service definition.
func InstallServiceDefinition(homeDir string) error {
	switch platformGOOS {
	case "darwin":
		path := filepath.Join(homeDir, LaunchdServicePath)
		if err := mkdirAllFunc(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create launchd directory: %w", err)
		}
		if err := writeFileFunc(path, []byte(LaunchdPlist(homeDir)), 0o644); err != nil {
			return fmt.Errorf("write launchd plist: %w", err)
		}
		_ = execCommandFunc("launchctl", "unload", path).Run()
		if err := execCommandFunc("launchctl", "load", path).Run(); err != nil {
			return fmt.Errorf("load launchd service: %w", err)
		}
		return nil
	case "linux":
		path := filepath.Join(homeDir, SystemdServicePath)
		if err := mkdirAllFunc(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create systemd directory: %w", err)
		}
		if err := writeFileFunc(path, []byte(SystemdUserUnit(homeDir)), 0o644); err != nil {
			return fmt.Errorf("write systemd unit: %w", err)
		}
		if err := execCommandFunc("systemctl", "--user", "daemon-reload").Run(); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
		if err := execCommandFunc("systemctl", "--user", "enable", "--now", "distil-proxy").Run(); err != nil {
			return fmt.Errorf("enable systemd service: %w", err)
		}
		return nil
	default:
		return errors.New("service install helper is unsupported on this OS")
	}
}
