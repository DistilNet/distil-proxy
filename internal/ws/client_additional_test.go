package ws

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/exec-io/distil-proxy/internal/fetch"
	"github.com/exec-io/distil-proxy/internal/jobs"
)

type staticFetcher struct {
	res fetch.Result
	err error
}

func (s staticFetcher) Fetch(_ context.Context, _ fetch.Request) (fetch.Result, error) {
	return s.res, s.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()

	serverConnCh := make(chan *websocket.Conn, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		serverConnCh <- conn
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	clientConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close(websocket.StatusNormalClosure, "test done")
	})

	var serverConn *websocket.Conn
	select {
	case serverConn = <-serverConnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server websocket")
	}
	t.Cleanup(func() {
		_ = serverConn.Close(websocket.StatusNormalClosure, "test done")
	})

	return clientConn, serverConn
}

func TestHandleMessageBranches(t *testing.T) {
	heartbeatCh := make(chan time.Time, 1)
	c := &Client{
		cfg: ClientConfig{
			Logger: discardLogger(),
			Hooks: Hooks{
				OnHeartbeat: func(at time.Time) {
					heartbeatCh <- at
				},
			},
		},
	}

	if err := c.handleMessage(context.Background(), nil, []byte("{bad")); err == nil {
		t.Fatal("expected decode websocket envelope error")
	}
	if err := c.handleMessage(context.Background(), nil, []byte(`{"type":"fetch","id":1}`)); err == nil {
		t.Fatal("expected decode fetch request error")
	}
	if err := c.handleMessage(context.Background(), nil, []byte(`{"type":"fetch","id":"","url":"https://example.com"}`)); err == nil {
		t.Fatal("expected missing id error")
	}
	if err := c.handleMessage(context.Background(), nil, []byte(`{"type":"unknown"}`)); err != nil {
		t.Fatalf("unexpected unknown-type error: %v", err)
	}
	if err := c.handleMessage(context.Background(), nil, []byte(`{"type":"pong"}`)); err != nil {
		t.Fatalf("unexpected pong error: %v", err)
	}

	select {
	case <-heartbeatCh:
	case <-time.After(1 * time.Second):
		t.Fatal("expected heartbeat hook call")
	}
}

func TestHandleMessagePingWritesPong(t *testing.T) {
	clientConn, serverConn := newWSPair(t)
	c := &Client{cfg: ClientConfig{Logger: discardLogger()}}

	if err := c.handleMessage(context.Background(), clientConn, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("handle ping: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var pong PongMessage
	if err := wsjson.Read(ctx, serverConn, &pong); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if pong.Type != "pong" {
		t.Fatalf("expected pong, got %+v", pong)
	}
}

func TestHandleFetchDuplicateID(t *testing.T) {
	clientConn, serverConn := newWSPair(t)
	reg := jobs.NewRegistry()
	if err := reg.Start("job-dup", func() {}); err != nil {
		t.Fatalf("start job in registry: %v", err)
	}
	defer reg.Finish("job-dup")

	var gotSuccess bool
	c := &Client{
		cfg: ClientConfig{
			DefaultTimeoutMS: 1000,
			Fetcher:          staticFetcher{},
			JobRegistry:      reg,
			Logger:           discardLogger(),
			Hooks: Hooks{
				OnJobResult: func(success bool, _ int64) {
					gotSuccess = success
				},
			},
		},
	}

	if err := c.handleFetch(context.Background(), clientConn, FetchRequest{ID: "job-dup", URL: "https://example.com"}); err != nil {
		t.Fatalf("handle fetch duplicate: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var ferr FetchError
	if err := wsjson.Read(ctx, serverConn, &ferr); err != nil {
		t.Fatalf("read fetch error: %v", err)
	}
	if ferr.Error != "duplicate_job_id" {
		t.Fatalf("expected duplicate_job_id, got %+v", ferr)
	}
	if gotSuccess {
		t.Fatal("expected failed job result hook")
	}
}

func TestHandleFetchDuplicateIDWriteError(t *testing.T) {
	clientConn, _ := newWSPair(t)
	_ = clientConn.Close(websocket.StatusNormalClosure, "force write error")

	reg := jobs.NewRegistry()
	if err := reg.Start("job-dup", func() {}); err != nil {
		t.Fatalf("start job in registry: %v", err)
	}
	defer reg.Finish("job-dup")

	jobResultCalled := false
	c := &Client{
		cfg: ClientConfig{
			DefaultTimeoutMS: 1000,
			Fetcher:          staticFetcher{},
			JobRegistry:      reg,
			Logger:           discardLogger(),
			Hooks: Hooks{
				OnJobResult: func(bool, int64) { jobResultCalled = true },
			},
		},
	}

	err := c.handleFetch(context.Background(), clientConn, FetchRequest{ID: "job-dup", URL: "https://example.com"})
	if err == nil || !strings.Contains(err.Error(), "write duplicate fetch error") {
		t.Fatalf("expected duplicate fetch write error, got %v", err)
	}
	if jobResultCalled {
		t.Fatal("expected no job result emission when duplicate fetch error cannot be sent")
	}
}

func TestHandleFetchMapsFetcherErrors(t *testing.T) {
	cases := []struct {
		name      string
		fetchErr  error
		wantCode  string
		wantInMsg string
	}{
		{
			name:      "timeout",
			fetchErr:  context.DeadlineExceeded,
			wantCode:  "daemon_timeout",
			wantInMsg: "250ms",
		},
		{
			name:      "too-large",
			fetchErr:  fetch.ErrResponseTooLarge,
			wantCode:  "response_too_large",
			wantInMsg: "maximum allowed size",
		},
		{
			name:      "generic",
			fetchErr:  errors.New("network failed"),
			wantCode:  "fetch_failed",
			wantInMsg: "network failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := newWSPair(t)
			c := &Client{
				cfg: ClientConfig{
					DefaultTimeoutMS: 250,
					Fetcher:          staticFetcher{err: tc.fetchErr},
					JobRegistry:      jobs.NewRegistry(),
					Logger:           discardLogger(),
				},
			}

			if err := c.handleFetch(context.Background(), clientConn, FetchRequest{ID: "job-" + tc.name, URL: "https://example.com"}); err != nil {
				t.Fatalf("handle fetch: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var ferr FetchError
			if err := wsjson.Read(ctx, serverConn, &ferr); err != nil {
				t.Fatalf("read fetch error: %v", err)
			}
			if ferr.Error != tc.wantCode {
				t.Fatalf("expected code %s, got %+v", tc.wantCode, ferr)
			}
			if !strings.Contains(ferr.Message, tc.wantInMsg) {
				t.Fatalf("expected message to contain %q, got %q", tc.wantInMsg, ferr.Message)
			}
		})
	}
}

func TestHandleFetchInvalidRequestBodyEncoding(t *testing.T) {
	clientConn, serverConn := newWSPair(t)
	c := &Client{
		cfg: ClientConfig{
			DefaultTimeoutMS: 250,
			Fetcher:          staticFetcher{},
			JobRegistry:      jobs.NewRegistry(),
			Logger:           discardLogger(),
		},
	}

	if err := c.handleFetch(context.Background(), clientConn, FetchRequest{
		ID:         "job-invalid-body",
		URL:        "https://example.com",
		BodyBase64: "@@@not-base64@@@",
	}); err != nil {
		t.Fatalf("handle fetch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var ferr FetchError
	if err := wsjson.Read(ctx, serverConn, &ferr); err != nil {
		t.Fatalf("read fetch error: %v", err)
	}
	if ferr.Error != "invalid_request_body" {
		t.Fatalf("expected invalid_request_body, got %+v", ferr)
	}
}

func TestHandleFetchWriteResultError(t *testing.T) {
	clientConn, _ := newWSPair(t)
	c := &Client{
		cfg: ClientConfig{
			DefaultTimeoutMS: 250,
			Fetcher: staticFetcher{res: fetch.Result{
				Status:    200,
				Body:      "ok",
				FinalURL:  "https://example.com/final",
				ElapsedMS: 1,
			}},
			JobRegistry: jobs.NewRegistry(),
			Logger:      discardLogger(),
		},
	}

	_ = clientConn.Close(websocket.StatusNormalClosure, "close before write")
	err := c.handleFetch(context.Background(), clientConn, FetchRequest{
		ID:  "job-write-error",
		URL: "https://example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "write fetch result") {
		t.Fatalf("expected write fetch result error, got %v", err)
	}
}

func TestHandleFetchFetcherErrorWriteFailure(t *testing.T) {
	clientConn, _ := newWSPair(t)
	_ = clientConn.Close(websocket.StatusNormalClosure, "force write error")

	jobResultCalled := false
	c := &Client{
		cfg: ClientConfig{
			DefaultTimeoutMS: 250,
			Fetcher:          staticFetcher{err: errors.New("network failed")},
			JobRegistry:      jobs.NewRegistry(),
			Logger:           discardLogger(),
			Hooks: Hooks{
				OnJobResult: func(bool, int64) { jobResultCalled = true },
			},
		},
	}

	err := c.handleFetch(context.Background(), clientConn, FetchRequest{ID: "job-write-fail", URL: "https://example.com"})
	if err == nil || !strings.Contains(err.Error(), "write fetch error") {
		t.Fatalf("expected fetch_error write failure, got %v", err)
	}
	if jobResultCalled {
		t.Fatal("expected no job result emission when fetch_error cannot be delivered")
	}
}

func TestHeartbeatLoopSendsPing(t *testing.T) {
	clientConn, serverConn := newWSPair(t)
	c := &Client{
		cfg: ClientConfig{
			HeartbeatInterval: 10 * time.Millisecond,
			Logger:            discardLogger(),
		},
	}

	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.heartbeatLoop(ctx, clientConn, errCh)

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	var ping PingMessage
	if err := wsjson.Read(readCtx, serverConn, &ping); err != nil {
		t.Fatalf("read ping: %v", err)
	}
	if ping.Type != "ping" {
		t.Fatalf("expected ping, got %+v", ping)
	}
}

func TestWriteJSONError(t *testing.T) {
	clientConn, _ := newWSPair(t)
	c := &Client{cfg: ClientConfig{Logger: discardLogger()}}

	_ = clientConn.Close(websocket.StatusNormalClosure, "close now")
	if err := c.writeJSON(context.Background(), clientConn, PongMessage{Type: "pong"}); err == nil {
		t.Fatal("expected write error")
	}
}

func TestEmitAndHelperFunctions(t *testing.T) {
	var state, errCalled, jobCalled bool
	var beat time.Time
	c := &Client{
		cfg: ClientConfig{
			Logger: discardLogger(),
			Hooks: Hooks{
				OnStateChange: func(string) { state = true },
				OnHeartbeat:   func(at time.Time) { beat = at },
				OnJobResult:   func(bool, int64) { jobCalled = true },
				OnError:       func(error) { errCalled = true },
			},
		},
	}

	c.emitState("connected")
	c.emitHeartbeat(time.Now().UTC())
	c.emitJobResult(true, 5)
	c.emitError(errors.New("boom"))

	if !state || beat.IsZero() || !jobCalled || !errCalled {
		t.Fatal("expected all hooks to be called")
	}
}

func TestUtilityFunctions(t *testing.T) {
	if err := validateAPIKey("dk_key"); err != nil {
		t.Fatalf("expected valid key, got %v", err)
	}
	if err := validateAPIKey("bad"); err == nil {
		t.Fatal("expected invalid key error")
	}

	if !isCloseError(websocket.CloseError{Code: websocket.StatusNormalClosure}) {
		t.Fatal("expected normal closure to be close error")
	}
	if !isCloseError(websocket.CloseError{Code: websocket.StatusGoingAway}) {
		t.Fatal("expected going away to be close error")
	}
	if !isCloseError(context.Canceled) {
		t.Fatal("expected context canceled to be close error")
	}
	if isCloseError(errors.New("nope")) {
		t.Fatal("unexpected close classification")
	}

	code, msg := mapFetchError(context.DeadlineExceeded, 123)
	if code != "daemon_timeout" || !strings.Contains(msg, "123ms") {
		t.Fatalf("unexpected timeout mapping: %s %s", code, msg)
	}
	code, _ = mapFetchError(fetch.ErrResponseTooLarge, 123)
	if code != "response_too_large" {
		t.Fatalf("unexpected too-large mapping: %s", code)
	}
	code, msg = mapFetchError(errors.New("x"), 123)
	if code != "fetch_failed" || msg != "x" {
		t.Fatalf("unexpected generic mapping: %s %s", code, msg)
	}

	body, err := decodeRequestBody(base64.StdEncoding.EncodeToString([]byte("abc")))
	if err != nil || string(body) != "abc" {
		t.Fatalf("expected decoded body abc, got body=%q err=%v", string(body), err)
	}

	body, err = decodeRequestBody("")
	if err != nil || len(body) != 0 {
		t.Fatalf("expected empty decoded body, got body=%q err=%v", string(body), err)
	}

	if _, err := decodeRequestBody("***"); err == nil {
		t.Fatal("expected invalid body decode error")
	}
}

func TestRunEmitsReconnectAndErrorOnDialFailure(t *testing.T) {
	var sawReconnect, sawStopped, sawErr bool
	client, err := NewClient(ClientConfig{
		ServerURL:         "ws://127.0.0.1:1/ws",
		APIKey:            "dk_test",
		InitialReconnect:  10 * time.Millisecond,
		MaxReconnectWait:  20 * time.Millisecond,
		HeartbeatInterval: time.Hour,
		Logger:            discardLogger(),
		Hooks: Hooks{
			OnStateChange: func(state string) {
				if state == "reconnecting" {
					sawReconnect = true
				}
				if state == "stopped" {
					sawStopped = true
				}
			},
			OnError: func(error) {
				sawErr = true
			},
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !sawReconnect || !sawStopped || !sawErr {
		t.Fatalf("expected reconnect/stopped/error hooks: reconnect=%t stopped=%t err=%t", sawReconnect, sawStopped, sawErr)
	}
}

func TestNewClientDefaultsAndMissingServer(t *testing.T) {
	if _, err := NewClient(ClientConfig{APIKey: "dk_test"}); err == nil {
		t.Fatal("expected missing server url error")
	}

	client, err := NewClient(ClientConfig{
		ServerURL: "wss://proxy.distil.net/ws",
		APIKey:    "dk_test",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if client.cfg.ProtocolVersion != DefaultProtocolVersion {
		t.Fatalf("expected default protocol version, got %s", client.cfg.ProtocolVersion)
	}
	if client.cfg.DefaultTimeoutMS != 30000 {
		t.Fatalf("expected default timeout, got %d", client.cfg.DefaultTimeoutMS)
	}
	if client.cfg.HeartbeatInterval != defaultHeartbeat {
		t.Fatalf("expected default heartbeat interval, got %s", client.cfg.HeartbeatInterval)
	}
	if client.cfg.InitialReconnect != time.Second {
		t.Fatalf("expected default reconnect interval, got %s", client.cfg.InitialReconnect)
	}
	if client.cfg.MaxReconnectWait != defaultMaxReconnectWait {
		t.Fatalf("expected default reconnect max, got %s", client.cfg.MaxReconnectWait)
	}
	if client.cfg.Fetcher == nil || client.cfg.JobRegistry == nil || client.cfg.Logger == nil {
		t.Fatal("expected default fetcher/job registry/logger")
	}
}

func TestRunSessionDialAndReadErrors(t *testing.T) {
	c := &Client{
		cfg: ClientConfig{
			ServerURL: "ws://127.0.0.1:1/ws",
			APIKey:    "dk_test",
			Logger:    discardLogger(),
		},
	}
	if err := c.runSession(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusInternalError, "boom")
	}))
	defer ts.Close()

	c = &Client{
		cfg: ClientConfig{
			ServerURL:         "ws" + strings.TrimPrefix(ts.URL, "http"),
			APIKey:            "dk_test",
			Logger:            discardLogger(),
			HeartbeatInterval: time.Hour,
		},
	}
	err := c.runSession(context.Background())
	if err == nil {
		t.Fatal("expected read error")
	}
	if !strings.Contains(err.Error(), "read websocket message") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func countHeartbeatLoopGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "(*Client).heartbeatLoop")
}

func TestRunSessionCancelsHeartbeatLoopOnCleanClose(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "rotate")
	}))
	defer ts.Close()

	c := &Client{
		cfg: ClientConfig{
			ServerURL:         "ws" + strings.TrimPrefix(ts.URL, "http"),
			APIKey:            "dk_test",
			Logger:            discardLogger(),
			HeartbeatInterval: time.Hour,
		},
	}

	baseline := countHeartbeatLoopGoroutines()
	for i := 0; i < 3; i++ {
		if err := c.runSession(context.Background()); err != nil {
			t.Fatalf("run session %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if countHeartbeatLoopGoroutines() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected heartbeat loop goroutines to return to baseline=%d, got=%d", baseline, countHeartbeatLoopGoroutines())
}

func TestHeartbeatLoopWriteError(t *testing.T) {
	clientConn, _ := newWSPair(t)
	c := &Client{
		cfg: ClientConfig{
			HeartbeatInterval: 5 * time.Millisecond,
			Logger:            discardLogger(),
		},
	}

	_ = clientConn.Close(websocket.StatusNormalClosure, "closed")

	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.heartbeatLoop(ctx, clientConn, errCh)

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "write heartbeat ping") {
			t.Fatalf("unexpected heartbeat error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("expected heartbeat write error")
	}
}
