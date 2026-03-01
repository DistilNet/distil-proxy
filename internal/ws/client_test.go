package ws

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/exec-io/distil-proxy/internal/fetch"
)

type stubFetcher struct {
	mu      sync.Mutex
	lastReq fetch.Request
}

func (s *stubFetcher) Fetch(_ context.Context, req fetch.Request) (fetch.Result, error) {
	s.mu.Lock()
	s.lastReq = req
	s.mu.Unlock()

	return fetch.Result{
		Status:    http.StatusOK,
		Headers:   map[string]string{"Content-Type": "text/plain"},
		Body:      "ok",
		FinalURL:  req.URL + "/final",
		ElapsedMS: 12,
	}, nil
}

func TestClientRunHandlesFetchAndHeaders(t *testing.T) {
	fetcher := &stubFetcher{}
	resultCh := make(chan FetchResult, 1)
	doneCh := make(chan struct{}, 1)

	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderDistilKey) != "dk_test_123" {
			t.Errorf("expected %s header", HeaderDistilKey)
		}
		if r.Header.Get(HeaderDistilVersion) != DefaultProtocolVersion {
			t.Errorf("expected %s header", HeaderDistilVersion)
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		if err := wsjson.Write(r.Context(), conn, FetchRequest{
			Type:       "fetch",
			ID:         "job_1",
			URL:        "https://example.com",
			Method:     "GET",
			BodyBase64: base64.StdEncoding.EncodeToString([]byte("payload")),
			TimeoutMS:  1000,
		}); err != nil {
			t.Errorf("write fetch request: %v", err)
			return
		}

		var res FetchResult
		if err := wsjson.Read(r.Context(), conn, &res); err != nil {
			t.Errorf("read fetch result: %v", err)
			return
		}
		resultCh <- res
		doneCh <- struct{}{}
	}))
	defer ts.Close()
	serverURL = "ws" + strings.TrimPrefix(ts.URL, "http")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := NewClient(ClientConfig{
		ServerURL:         serverURL,
		APIKey:            "dk_test_123",
		ProtocolVersion:   DefaultProtocolVersion,
		DefaultTimeoutMS:  1000,
		Fetcher:           fetcher,
		Logger:            slog.Default(),
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()

	select {
	case <-doneCh:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for websocket exchange")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("client run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for client shutdown")
	}

	select {
	case res := <-resultCh:
		if res.Type != "result" {
			t.Fatalf("expected type=result, got %s", res.Type)
		}
		if res.ID != "job_1" {
			t.Fatalf("expected id=job_1, got %s", res.ID)
		}
		if res.Status != 200 || res.Body != "ok" {
			t.Fatalf("unexpected result payload: %+v", res)
		}
		if res.FinalURL != "https://example.com/final" {
			t.Fatalf("expected final_url to round-trip, got %q", res.FinalURL)
		}
	default:
		t.Fatal("expected fetch result")
	}

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	if string(fetcher.lastReq.Body) != "payload" {
		t.Fatalf("expected forwarded body payload, got %q", string(fetcher.lastReq.Body))
	}
}

func TestNewClientRejectsInvalidKey(t *testing.T) {
	_, err := NewClient(ClientConfig{ServerURL: "wss://example.com/ws", APIKey: "dpk_bad"})
	if err == nil {
		t.Fatal("expected invalid api key error")
	}
}

func TestClientReconnectsAfterDisconnect(t *testing.T) {
	fetcher := &stubFetcher{}
	var connections atomic.Int32
	doneCh := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		connNum := connections.Add(1)
		if connNum == 1 {
			_ = conn.Close(websocket.StatusNormalClosure, "first connection closed")
			return
		}

		if err := wsjson.Write(r.Context(), conn, FetchRequest{Type: "fetch", ID: "job_reconnect", URL: "https://example.com"}); err != nil {
			t.Errorf("write fetch request: %v", err)
			return
		}

		var res FetchResult
		if err := wsjson.Read(r.Context(), conn, &res); err != nil {
			t.Errorf("read fetch result: %v", err)
			return
		}
		if res.ID != "job_reconnect" {
			t.Errorf("unexpected result id: %s", res.ID)
		}
		doneCh <- struct{}{}
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := NewClient(ClientConfig{
		ServerURL:         "ws" + strings.TrimPrefix(ts.URL, "http"),
		APIKey:            "dk_reconnect_123",
		ProtocolVersion:   DefaultProtocolVersion,
		Fetcher:           fetcher,
		Logger:            slog.Default(),
		HeartbeatInterval: time.Hour,
		InitialReconnect:  10 * time.Millisecond,
		MaxReconnectWait:  50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- client.Run(ctx) }()

	select {
	case <-doneCh:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for reconnect flow")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("client returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for client stop")
	}

	if got := connections.Load(); got < 2 {
		t.Fatalf("expected at least 2 connections, got %d", got)
	}
}

func TestClientReconnectBackoffResetsAfterCleanClose(t *testing.T) {
	var connections atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		connections.Add(1)
		_ = conn.Close(websocket.StatusNormalClosure, "rotate")
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Millisecond)
	defer cancel()

	client, err := NewClient(ClientConfig{
		ServerURL:         "ws" + strings.TrimPrefix(ts.URL, "http"),
		APIKey:            "dk_backoff_reset_123",
		ProtocolVersion:   DefaultProtocolVersion,
		Logger:            slog.Default(),
		HeartbeatInterval: time.Hour,
		InitialReconnect:  15 * time.Millisecond,
		MaxReconnectWait:  120 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- client.Run(ctx) }()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("client run returned error: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for client to stop")
	}

	if got := connections.Load(); got < 7 {
		t.Fatalf("expected frequent reconnects after clean closes, got %d connections", got)
	}
}
