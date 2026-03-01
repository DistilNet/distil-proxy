package observability

import "testing"

func TestMetricsSnapshot(t *testing.T) {
	m := &Metrics{}
	m.IncConnectAttempts()
	m.IncReconnects()
	m.RecordJob(true, 10)
	m.RecordJob(true, 300)
	m.RecordJob(true, 1200)
	m.RecordJob(false, 0)

	s := m.Snapshot()
	if s.ConnectAttempts != 1 {
		t.Fatalf("expected 1 connect attempt, got %d", s.ConnectAttempts)
	}
	if s.Reconnects != 1 {
		t.Fatalf("expected 1 reconnect, got %d", s.Reconnects)
	}
	if s.JobsSuccess != 3 {
		t.Fatalf("expected 3 successful jobs, got %d", s.JobsSuccess)
	}
	if s.JobsError != 1 {
		t.Fatalf("expected 1 failed job, got %d", s.JobsError)
	}
	if s.AvgLatencyMS != 503 {
		t.Fatalf("expected avg latency 503ms, got %d", s.AvgLatencyMS)
	}
	if s.LatencyLE100MS != 1 || s.LatencyLE500MS != 1 || s.LatencyLE1000MS != 0 || s.LatencyGT1000MS != 1 {
		t.Fatalf("unexpected latency buckets: %+v", s)
	}
}
