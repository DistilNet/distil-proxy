package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/daemon"
	"github.com/distilnet/distil-proxy/internal/ws"
)

func TestDaemonRoundtripWithLocalWebsocketEndpoint(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "e2e-ok")
	}))
	defer httpServer.Close()

	resultCh := make(chan ws.FetchResult, 1)
	errCh := make(chan error, 1)

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(ws.HeaderDistilKey); got != "dk_e2e_local" {
			t.Errorf("expected %s header, got %q", ws.HeaderDistilKey, got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		if err := wsjson.Write(r.Context(), conn, ws.FetchRequest{
			Type:      "fetch",
			ID:        "job-e2e-1",
			URL:       httpServer.URL,
			Method:    http.MethodGet,
			TimeoutMS: 5000,
		}); err != nil {
			t.Errorf("write fetch request: %v", err)
			return
		}

		var res ws.FetchResult
		if err := wsjson.Read(r.Context(), conn, &res); err != nil {
			t.Errorf("read fetch result: %v", err)
			return
		}
		resultCh <- res
	}))
	defer wsServer.Close()

	paths := config.DefaultPaths(t.TempDir())
	cfg := config.Config{
		APIKey:    "dk_e2e_local",
		Server:    "ws" + strings.TrimPrefix(wsServer.URL, "http"),
		TimeoutMS: 5000,
		LogLevel:  "info",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		errCh <- daemon.StartForeground(ctx, paths, cfg, io.Discard)
	}()

	select {
	case res := <-resultCh:
		if res.Type != "result" {
			t.Fatalf("expected result type, got %q", res.Type)
		}
		if res.ID != "job-e2e-1" {
			t.Fatalf("expected job id job-e2e-1, got %q", res.ID)
		}
		if res.Status != http.StatusOK || res.Body != "e2e-ok" {
			t.Fatalf("unexpected fetch result: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for e2e fetch roundtrip")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for daemon shutdown")
	}
}
