package main

import (
	"os"
	"testing"
)

func TestMainVersionCommand(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	os.Args = []string{"distil-proxy", "version"}
	main()
}
