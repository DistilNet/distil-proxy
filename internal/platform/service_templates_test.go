package platform

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLaunchdPlistContainsForegroundStart(t *testing.T) {
	home := "/tmp/home"
	plist := LaunchdPlist(home)

	for _, want := range []string{
		filepath.Join(home, ".distil-proxy/bin/distil-proxy"),
		"start",
		"--foreground",
		filepath.Join(home, ".distil-proxy/logs/daemon.log"),
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("expected plist to contain %q", want)
		}
	}
}

func TestSystemdUserUnitContainsForegroundStart(t *testing.T) {
	home := "/tmp/home"
	unit := SystemdUserUnit(home)

	expectedExec := strconv.Quote(filepath.Join(home, ".distil-proxy/bin/distil-proxy")) + " start --foreground"
	if !strings.Contains(unit, expectedExec) {
		t.Fatal("expected systemd unit to include foreground start command")
	}
	if !strings.Contains(unit, "Restart=always") {
		t.Fatal("expected systemd unit to include restart policy")
	}
}

func TestSystemdUserUnitQuotesPathWithWhitespace(t *testing.T) {
	home := "/tmp/home with spaces"
	unit := SystemdUserUnit(home)
	expectedExec := strconv.Quote(filepath.Join(home, ".distil-proxy/bin/distil-proxy")) + " start --foreground"

	if !strings.Contains(unit, expectedExec) {
		t.Fatalf("expected quoted ExecStart path, got:\n%s", unit)
	}
}
