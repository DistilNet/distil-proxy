package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorReadCloser struct{}

func (errorReadCloser) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed")
}

func (errorReadCloser) Close() error {
	return nil
}

func TestHTTPExecutorFetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if got := r.Header.Get("X-Test"); got != "yes" {
			t.Fatalf("expected header X-Test=yes, got %q", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer ts.Close()

	executor := NewHTTPExecutor(1024)
	res, err := executor.Fetch(context.Background(), Request{
		URL:     ts.URL,
		Headers: map[string]string{"X-Test": "yes"},
	})
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	if res.Status != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.Status)
	}
	if res.Body != "hello" {
		t.Fatalf("expected body hello, got %q", res.Body)
	}
	if res.Headers["Content-Type"] != "text/plain" {
		t.Fatalf("expected content-type text/plain, got %q", res.Headers["Content-Type"])
	}
}

func TestHTTPExecutorFetchSendsRequestBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if string(body) != "payload-body" {
			t.Fatalf("expected payload-body, got %q", string(body))
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	executor := NewHTTPExecutor(1024)
	res, err := executor.Fetch(context.Background(), Request{
		URL:    ts.URL,
		Method: http.MethodPost,
		Body:   []byte("payload-body"),
	})
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if res.Body != "ok" {
		t.Fatalf("expected body ok, got %q", res.Body)
	}
}

func TestHTTPExecutorFetchResponseTooLarge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer ts.Close()

	executor := NewHTTPExecutor(5)
	_, err := executor.Fetch(context.Background(), Request{URL: ts.URL})
	if err == nil {
		t.Fatal("expected error")
	}
	if err != ErrResponseTooLarge {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}
}

func TestNewHTTPExecutorUsesDefaultLimit(t *testing.T) {
	executor := NewHTTPExecutor(0)
	if executor.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Fatalf("expected max body %d, got %d", DefaultMaxBodyBytes, executor.MaxBodyBytes)
	}
}

func TestHTTPExecutorFetchBuildRequestError(t *testing.T) {
	executor := NewHTTPExecutor(1024)
	_, err := executor.Fetch(context.Background(), Request{URL: "://bad-url"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPExecutorFetchClientDoError(t *testing.T) {
	executor := &HTTPExecutor{
		Client: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("network down")
			}),
		},
		MaxBodyBytes: 1024,
	}

	_, err := executor.Fetch(context.Background(), Request{URL: "https://example.com"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPExecutorFetchReadBodyError(t *testing.T) {
	executor := &HTTPExecutor{
		Client: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       errorReadCloser{},
				}, nil
			}),
		},
		MaxBodyBytes: 1024,
	}

	_, err := executor.Fetch(context.Background(), Request{URL: "https://example.com"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPExecutorFetchDefaultsMethodAndNilClient(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()

	executor := &HTTPExecutor{
		Client:       nil,
		MaxBodyBytes: 0,
	}

	res, err := executor.Fetch(context.Background(), Request{URL: ts.URL})
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected default method GET, got %s", gotMethod)
	}
	if res.Body != "ok" {
		t.Fatalf("expected body ok, got %q", res.Body)
	}
}
