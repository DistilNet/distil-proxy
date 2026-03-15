package platform

import (
	"os/exec"
	"strings"
	"testing"
)

func TestOpenBrowser(t *testing.T) {
	withPlatformGlobalsReset(t)

	var got []string
	execCommandFunc = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("sh", "-c", "true")
	}

	cases := []struct {
		goos string
		want []string
	}{
		{goos: "darwin", want: []string{"open", "https://distil.net/account"}},
		{goos: "linux", want: []string{"xdg-open", "https://distil.net/account"}},
		{goos: "windows", want: []string{"rundll32", "url.dll,FileProtocolHandler", "https://distil.net/account"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.goos, func(t *testing.T) {
			got = nil
			platformGOOS = tc.goos
			if err := OpenBrowser("https://distil.net/account"); err != nil {
				t.Fatalf("open browser: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("unexpected command: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestOpenBrowserErrors(t *testing.T) {
	t.Run("unsupported-os", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "plan9"
		if err := OpenBrowser("https://distil.net/account"); err == nil {
			t.Fatal("expected unsupported os error")
		}
	})

	t.Run("command-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "linux"
		execCommandFunc = func(string, ...string) *exec.Cmd {
			return exec.Command("sh", "-c", "false")
		}
		err := OpenBrowser("https://distil.net/account")
		if err == nil || !strings.Contains(err.Error(), "open browser") {
			t.Fatalf("expected open browser error, got %v", err)
		}
	})

	t.Run("spawn-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "linux"
		execCommandFunc = func(string, ...string) *exec.Cmd {
			return exec.Command("/definitely/missing-browser")
		}
		err := OpenBrowser("https://distil.net/account")
		if err == nil || !strings.Contains(err.Error(), "open browser") {
			t.Fatalf("expected spawn error, got %v", err)
		}
	})
}
