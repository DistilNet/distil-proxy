package app

import (
	"context"
	"testing"
)

func TestRunUnknownCommandReturnsError(t *testing.T) {
	err := Run(context.Background(), []string{"unknown-cmd"})
	if err == nil {
		t.Fatal("expected unknown command error")
	}
}

func TestRunVersionCommandSucceeds(t *testing.T) {
	if err := Run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("expected version command to succeed: %v", err)
	}
}
