package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/distilnet/distil-proxy/internal/fetch"
	"github.com/distilnet/distil-proxy/internal/jobs"
)

const (
	defaultConnectTimeout   = 10 * time.Second
	defaultHeartbeat        = 30 * time.Second
	defaultMaxReconnectWait = 60 * time.Second
	defaultWriteTimeout     = 5 * time.Second
)

// Hooks allows daemon runtime to observe websocket lifecycle events.
type Hooks struct {
	OnStateChange func(state string)
	OnHeartbeat   func(at time.Time)
	OnJobResult   func(success bool, durationMS int64)
	OnError       func(err error)
}

// ClientConfig configures websocket runtime behavior.
type ClientConfig struct {
	ServerURL         string
	APIKey            string
	ProtocolVersion   string
	DefaultTimeoutMS  int
	HeartbeatInterval time.Duration
	InitialReconnect  time.Duration
	MaxReconnectWait  time.Duration
	Fetcher           fetch.Executor
	JobRegistry       *jobs.Registry
	Logger            *slog.Logger
	Hooks             Hooks
}

// Client executes websocket job processing with reconnect behavior.
type Client struct {
	cfg     ClientConfig
	writeMu sync.Mutex
}

// NewClient validates and normalizes websocket runtime settings.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.ServerURL == "" {
		return nil, errors.New("server url is required")
	}
	if err := validateAPIKey(cfg.APIKey); err != nil {
		return nil, err
	}
	if cfg.ProtocolVersion == "" {
		cfg.ProtocolVersion = DefaultProtocolVersion
	}
	if cfg.DefaultTimeoutMS <= 0 {
		cfg.DefaultTimeoutMS = 30000
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = defaultHeartbeat
	}
	if cfg.InitialReconnect <= 0 {
		cfg.InitialReconnect = time.Second
	}
	if cfg.MaxReconnectWait <= 0 {
		cfg.MaxReconnectWait = defaultMaxReconnectWait
	}
	if cfg.Fetcher == nil {
		cfg.Fetcher = fetch.NewHTTPExecutor(fetch.DefaultMaxBodyBytes)
	}
	if cfg.JobRegistry == nil {
		cfg.JobRegistry = jobs.NewRegistry()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Client{cfg: cfg}, nil
}

// Run starts the websocket loop with exponential reconnect.
func (c *Client) Run(ctx context.Context) error {
	defer c.cfg.JobRegistry.CancelAll()

	backoff := c.cfg.InitialReconnect

	for {
		err := c.runSession(ctx)
		if ctx.Err() != nil {
			c.emitState("stopped")
			return nil
		}
		if err != nil {
			c.emitError(err)
			c.emitState("reconnecting")
		} else {
			// Reset reconnect delay after clean sessions so exponential backoff
			// only applies to consecutive failures.
			backoff = c.cfg.InitialReconnect
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.emitState("stopped")
			return nil
		case <-timer.C:
		}

		if err != nil {
			backoff *= 2
			if backoff > c.cfg.MaxReconnectWait {
				backoff = c.cfg.MaxReconnectWait
			}
		}
	}
}

func (c *Client) runSession(ctx context.Context) error {
	headers := http.Header{}
	headers.Set(HeaderDistilKey, c.cfg.APIKey)
	headers.Set(HeaderDistilVersion, c.cfg.ProtocolVersion)

	dialCtx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, c.cfg.ServerURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutdown")

	c.emitState("connected")
	c.cfg.Logger.Info("websocket connected")

	sessionCtx, cancelSession := context.WithCancel(ctx)
	heartbeatErr := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		c.heartbeatLoop(sessionCtx, conn, heartbeatErr)
	}()
	defer func() {
		cancelSession()
		<-heartbeatDone
	}()

	for {
		select {
		case <-sessionCtx.Done():
			return nil
		case err := <-heartbeatErr:
			if err != nil && !isCloseError(err) {
				return err
			}
			return nil
		default:
		}

		_, payload, err := conn.Read(sessionCtx)
		if err != nil {
			if isCloseError(err) {
				return nil
			}
			return fmt.Errorf("read websocket message: %w", err)
		}

		if err := c.handleMessage(sessionCtx, conn, payload); err != nil {
			return err
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context, conn *websocket.Conn, errs chan<- error) {
	ticker := time.NewTicker(c.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg := PingMessage{Type: "ping"}
			if err := c.writeJSON(ctx, conn, msg); err != nil {
				_ = conn.Close(websocket.StatusInternalError, "heartbeat failed")
				errs <- fmt.Errorf("write heartbeat ping: %w", err)
				return
			}
		}
	}
}

func (c *Client) handleMessage(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("decode websocket envelope: %w", err)
	}

	switch env.Type {
	case "fetch":
		var req FetchRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return fmt.Errorf("decode fetch request: %w", err)
		}
		if req.ID == "" {
			return errors.New("fetch request missing id")
		}
		return c.handleFetch(ctx, conn, req)
	case "ping":
		return c.writeJSON(ctx, conn, PongMessage{Type: "pong"})
	case "pong":
		c.emitHeartbeat(time.Now().UTC())
		return nil
	default:
		c.cfg.Logger.Warn("unknown websocket message type", "type", env.Type)
		return nil
	}
}

func (c *Client) handleFetch(ctx context.Context, conn *websocket.Conn, req FetchRequest) error {
	timeoutMS := req.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = c.cfg.DefaultTimeoutMS
	}

	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	if err := c.cfg.JobRegistry.Start(req.ID, cancel); err != nil {
		if err := c.writeJSON(ctx, conn, FetchError{
			Type:    "fetch_error",
			ID:      req.ID,
			Error:   "duplicate_job_id",
			Message: "Job is already in progress",
		}); err != nil {
			return fmt.Errorf("write duplicate fetch error: %w", err)
		}
		c.emitJobResult(false, 0)
		return nil
	}
	defer c.cfg.JobRegistry.Finish(req.ID)

	requestBody, err := decodeRequestBody(req.BodyBase64)
	if err != nil {
		_ = c.writeJSON(ctx, conn, FetchError{
			Type:    "fetch_error",
			ID:      req.ID,
			Error:   "invalid_request_body",
			Message: "request body encoding is invalid",
		})
		c.emitJobResult(false, 0)
		return nil
	}

	res, err := c.cfg.Fetcher.Fetch(fetchCtx, fetch.Request{
		URL:       req.URL,
		Method:    req.Method,
		Headers:   req.Headers,
		Body:      requestBody,
		TimeoutMS: timeoutMS,
	})
	if err != nil {
		code, msg := mapFetchError(err, timeoutMS)
		if err := c.writeJSON(ctx, conn, FetchError{
			Type:    "fetch_error",
			ID:      req.ID,
			Error:   code,
			Message: msg,
		}); err != nil {
			return fmt.Errorf("write fetch error: %w", err)
		}
		c.emitJobResult(false, 0)
		return nil
	}

	if err := c.writeJSON(ctx, conn, FetchResult{
		Type:      "result",
		ID:        req.ID,
		Status:    res.Status,
		Headers:   res.Headers,
		Body:      res.Body,
		FinalURL:  res.FinalURL,
		ElapsedMS: res.ElapsedMS,
	}); err != nil {
		return fmt.Errorf("write fetch result: %w", err)
	}

	c.emitJobResult(true, res.ElapsedMS)
	return nil
}

func decodeRequestBody(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	return body, nil
}

func (c *Client) writeJSON(ctx context.Context, conn *websocket.Conn, payload any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	writeCtx, cancel := context.WithTimeout(ctx, defaultWriteTimeout)
	defer cancel()

	if err := wsjson.Write(writeCtx, conn, payload); err != nil {
		return err
	}
	return nil
}

func (c *Client) emitState(state string) {
	if c.cfg.Hooks.OnStateChange != nil {
		c.cfg.Hooks.OnStateChange(state)
	}
}

func (c *Client) emitHeartbeat(at time.Time) {
	if c.cfg.Hooks.OnHeartbeat != nil {
		c.cfg.Hooks.OnHeartbeat(at)
	}
}

func (c *Client) emitJobResult(success bool, durationMS int64) {
	if c.cfg.Hooks.OnJobResult != nil {
		c.cfg.Hooks.OnJobResult(success, durationMS)
	}
}

func (c *Client) emitError(err error) {
	if c.cfg.Hooks.OnError != nil {
		c.cfg.Hooks.OnError(err)
	}
	c.cfg.Logger.Error("websocket error", "error", err)
}

func isCloseError(err error) bool {
	status := websocket.CloseStatus(err)
	if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
		return true
	}
	return errors.Is(err, context.Canceled)
}

func validateAPIKey(key string) error {
	if strings.HasPrefix(key, "dk_") && len(key) > len("dk_") {
		return nil
	}
	if strings.HasPrefix(key, "dpk_") && len(key) > len("dpk_") {
		return nil
	}
	return errors.New("api key must start with dk_ or dpk_")
}

func mapFetchError(err error, timeoutMS int) (string, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "daemon_timeout", fmt.Sprintf("Daemon did not respond within %dms", timeoutMS)
	case errors.Is(err, fetch.ErrResponseTooLarge):
		return "response_too_large", "Response exceeded maximum allowed size"
	default:
		return "fetch_failed", err.Error()
	}
}
