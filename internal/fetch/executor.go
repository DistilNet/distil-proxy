package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultMaxBodyBytes = 1_000_000
)

var (
	ErrResponseTooLarge = errors.New("response exceeds max body size")
)

// Request defines an outbound fetch request from the websocket job payload.
type Request struct {
	URL       string
	Method    string
	Headers   map[string]string
	Body      []byte
	TimeoutMS int
}

// Result defines a fetch response payload.
type Result struct {
	Status    int
	Headers   map[string]string
	Body      string
	ElapsedMS int64
}

// Executor performs network fetches.
type Executor interface {
	Fetch(ctx context.Context, req Request) (Result, error)
}

// HTTPExecutor fetches content via net/http with bounded response size.
type HTTPExecutor struct {
	Client       *http.Client
	MaxBodyBytes int64
}

// NewHTTPExecutor constructs an HTTP executor with safe defaults.
func NewHTTPExecutor(maxBodyBytes int64) *HTTPExecutor {
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	return &HTTPExecutor{
		Client:       &http.Client{},
		MaxBodyBytes: maxBodyBytes,
	}
}

// Fetch executes a single HTTP request.
func (e *HTTPExecutor) Fetch(ctx context.Context, req Request) (Result, error) {
	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodGet
	}

	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	client := e.Client
	if client == nil {
		client = &http.Client{}
	}

	started := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	limit := e.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Result{}, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > limit {
		return Result{}, ErrResponseTooLarge
	}

	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		headers[key] = strings.Join(values, ",")
	}

	return Result{
		Status:    resp.StatusCode,
		Headers:   headers,
		Body:      string(body),
		ElapsedMS: time.Since(started).Milliseconds(),
	}, nil
}
