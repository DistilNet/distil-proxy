package observability

import "sync"

// Metrics captures lightweight daemon runtime counters for local diagnostics.
type Metrics struct {
	mu sync.Mutex

	ConnectAttempts int64
	Reconnects      int64
	JobsSuccess     int64
	JobsError       int64
	totalLatencyMS  int64
	LatencyLE100MS  int64
	LatencyLE500MS  int64
	LatencyLE1000MS int64
	LatencyGT1000MS int64
}

// Snapshot is a read-only view of metrics.
type Snapshot struct {
	ConnectAttempts int64
	Reconnects      int64
	JobsSuccess     int64
	JobsError       int64
	AvgLatencyMS    int64
	LatencyLE100MS  int64
	LatencyLE500MS  int64
	LatencyLE1000MS int64
	LatencyGT1000MS int64
}

// IncConnectAttempts increments successful websocket connection count.
func (m *Metrics) IncConnectAttempts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ConnectAttempts++
}

// IncReconnects increments reconnect attempt count.
func (m *Metrics) IncReconnects() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Reconnects++
}

// RecordJob records outcome and latency for a completed job.
func (m *Metrics) RecordJob(success bool, latencyMS int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if success {
		m.JobsSuccess++
		m.totalLatencyMS += latencyMS
		switch {
		case latencyMS <= 100:
			m.LatencyLE100MS++
		case latencyMS <= 500:
			m.LatencyLE500MS++
		case latencyMS <= 1000:
			m.LatencyLE1000MS++
		default:
			m.LatencyGT1000MS++
		}
	} else {
		m.JobsError++
	}
}

// Snapshot returns a thread-safe metrics copy.
func (m *Metrics) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	avg := int64(0)
	if m.JobsSuccess > 0 {
		avg = m.totalLatencyMS / m.JobsSuccess
	}

	return Snapshot{
		ConnectAttempts: m.ConnectAttempts,
		Reconnects:      m.Reconnects,
		JobsSuccess:     m.JobsSuccess,
		JobsError:       m.JobsError,
		AvgLatencyMS:    avg,
		LatencyLE100MS:  m.LatencyLE100MS,
		LatencyLE500MS:  m.LatencyLE500MS,
		LatencyLE1000MS: m.LatencyLE1000MS,
		LatencyGT1000MS: m.LatencyGT1000MS,
	}
}
