package config

import (
	"os"
	"testing"
)

func FuzzLoadConfig(f *testing.F) {
	f.Add([]byte(`{"api_key":"dk_abc123","server":"wss://proxy.distil.net/ws"}`))
	f.Add([]byte(`{"api_key":"dk_bad","timeout_ms":-1}`))
	f.Add([]byte(`not-json`))

	f.Fuzz(func(t *testing.T, payload []byte) {
		home := t.TempDir()
		paths := DefaultPaths(home)
		if err := EnsureStateDirs(paths); err != nil {
			t.Fatalf("ensure dirs: %v", err)
		}
		if err := os.WriteFile(paths.ConfigFile, payload, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}

		_, _ = Load(paths)
	})
}
