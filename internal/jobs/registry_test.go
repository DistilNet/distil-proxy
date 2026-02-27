package jobs

import (
	"context"
	"testing"
)

func TestRegistryStartFinish(t *testing.T) {
	r := NewRegistry()
	cancelCalled := false
	cancel := func() { cancelCalled = true }

	if err := r.Start("job_1", cancel); err != nil {
		t.Fatalf("start job: %v", err)
	}
	if got := r.ActiveCount(); got != 1 {
		t.Fatalf("expected 1 active job, got %d", got)
	}
	if err := r.Start("job_1", cancel); err == nil {
		t.Fatal("expected duplicate job error")
	}

	r.Finish("job_1")
	if got := r.ActiveCount(); got != 0 {
		t.Fatalf("expected 0 active jobs, got %d", got)
	}

	r.CancelAll()
	if cancelCalled {
		t.Fatal("cancel should not run after finish")
	}
}

func TestRegistryCancelAll(t *testing.T) {
	r := NewRegistry()
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	if err := r.Start("job_1", cancel1); err != nil {
		t.Fatalf("start job_1: %v", err)
	}
	if err := r.Start("job_2", cancel2); err != nil {
		t.Fatalf("start job_2: %v", err)
	}

	r.CancelAll()
	if got := r.ActiveCount(); got != 0 {
		t.Fatalf("expected 0 active jobs, got %d", got)
	}

	select {
	case <-ctx1.Done():
	default:
		t.Fatal("expected ctx1 to be canceled")
	}
	select {
	case <-ctx2.Done():
	default:
		t.Fatal("expected ctx2 to be canceled")
	}
}
