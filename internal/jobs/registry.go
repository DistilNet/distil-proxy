package jobs

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrJobExists = errors.New("job already exists")

// Registry tracks in-flight jobs and supports bulk cancellation.
type Registry struct {
	mu   sync.Mutex
	jobs map[string]entry
}

type entry struct {
	startedAt time.Time
	cancel    context.CancelFunc
}

// NewRegistry creates an empty in-flight job registry.
func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]entry)}
}

// Start registers a running job.
func (r *Registry) Start(jobID string, cancel context.CancelFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.jobs[jobID]; exists {
		return ErrJobExists
	}
	r.jobs[jobID] = entry{startedAt: time.Now().UTC(), cancel: cancel}
	return nil
}

// Finish removes a job from the active set.
func (r *Registry) Finish(jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, jobID)
}

// ActiveCount returns number of currently running jobs.
func (r *Registry) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

// CancelAll aborts all active jobs and clears the registry.
func (r *Registry) CancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
	r.jobs = make(map[string]entry)
}
