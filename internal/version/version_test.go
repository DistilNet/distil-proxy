package version

import "testing"

func TestDefaultInfoString(t *testing.T) {
	info := DefaultInfo()
	out := info.String()
	if out == "" {
		t.Fatal("expected non-empty version string")
	}
}
