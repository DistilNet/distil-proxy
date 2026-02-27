package platform

import (
	"path/filepath"
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

	if !strings.Contains(unit, filepath.Join(home, ".distil-proxy/bin/distil-proxy")+" start --foreground") {
		t.Fatal("expected systemd unit to include foreground start command")
	}
	if !strings.Contains(unit, "Restart=always") {
		t.Fatal("expected systemd unit to include restart policy")
	}
}
