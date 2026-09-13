package httpfetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
)

func TestGet_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("body = %q", resp.Body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
}

func TestGet_RetriesOn5xxThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := Get(context.Background(), srv.URL, WithBaseBackoff(1*time.Millisecond))
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if string(resp.Body) != "ok" {
		t.Errorf("body = %q", resp.Body)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("expected 3 hits, got %d", got)
	}
}

func TestGet_DoesNotRetryOn4xx(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Get(context.Background(), srv.URL, WithBaseBackoff(1*time.Millisecond))
	if err == nil {
		t.Fatalf("expected error on 404")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 hit (no retry on 4xx), got %d", got)
	}
}

func TestGet_RetriesOn429(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, err := Get(context.Background(), srv.URL, WithBaseBackoff(1*time.Millisecond))
	if err != nil {
		t.Fatalf("expected success after 429 retry, got: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 hits, got %d", got)
	}
}

func TestGet_HonorsRetryAfter(t *testing.T) {
	var hits atomic.Int32
	var firstAt, secondAt time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			firstAt = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		secondAt = time.Now()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if _, err := Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gap := secondAt.Sub(firstAt)
	if gap < 900*time.Millisecond {
		t.Errorf("expected ~1s gap from Retry-After, got %v", gap)
	}
}

func TestGet_ContextCancellationStopsRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := Get(ctx, srv.URL, WithBaseBackoff(200*time.Millisecond), WithMaxAttempts(10))
	if err == nil {
		t.Fatal("expected error from cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected context error, got: %v", err)
	}
}

func TestGet_ExhaustsAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := Get(context.Background(), srv.URL,
		WithBaseBackoff(1*time.Millisecond),
		WithMaxAttempts(3),
	)
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("expected 3 hits, got %d", got)
	}
}

// countingTransport wraps response bodies in a counting reader so tests can
// observe how many bytes the client actually consumed from the response.
type countingTransport struct {
	base  http.RoundTripper
	count *atomic.Int64
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	resp.Body = &countingReadCloser{ReadCloser: resp.Body, count: t.count}
	return resp, nil
}

type countingReadCloser struct {
	io.ReadCloser
	count *atomic.Int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.count.Add(int64(n))
	return n, err
}

func newCountingClient() (*http.Client, *atomic.Int64) {
	count := new(atomic.Int64)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true // keep counts deterministic; no gzip wrapping
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &countingTransport{base: tr, count: count},
	}, count
}

func TestGet_DrainLimitCapsBytesRead(t *testing.T) {
	const bodySize = 100 * 1024
	largeBody := bytes.Repeat([]byte("x"), bodySize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(bodySize))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(largeBody)
	}))
	defer srv.Close()

	client, count := newCountingClient()

	_, err := Get(context.Background(), srv.URL,
		WithClient(client),
		WithMaxAttempts(1),
		WithDrainLimit(1024),
	)
	if err == nil {
		t.Fatal("expected error on 500")
	}

	got := count.Load()
	if got >= int64(bodySize) {
		t.Errorf("drained %d bytes of a %d-byte body; limit was 1024", got, bodySize)
	}
	// Generous upper bound: transport may buffer a bit past the LimitReader cut-off.
	if got > 8*1024 {
		t.Errorf("drained %d bytes, expected near the 1024 limit", got)
	}
}

func TestGet_DrainLimitZeroIsUnbounded(t *testing.T) {
	const bodySize = 16 * 1024
	largeBody := bytes.Repeat([]byte("y"), bodySize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(bodySize))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(largeBody)
	}))
	defer srv.Close()

	client, count := newCountingClient()

	_, err := Get(context.Background(), srv.URL,
		WithClient(client),
		WithMaxAttempts(1),
		WithDrainLimit(0),
	)
	if err == nil {
		t.Fatal("expected error on 500")
	}

	if got := count.Load(); got != int64(bodySize) {
		t.Errorf("unbounded drain read %d bytes, expected full body %d", got, bodySize)
	}
}

func TestGet_DefaultDrainLimit(t *testing.T) {
	const bodySize = 100 * 1024
	largeBody := bytes.Repeat([]byte("z"), bodySize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(bodySize))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(largeBody)
	}))
	defer srv.Close()

	client, count := newCountingClient()

	_, err := Get(context.Background(), srv.URL,
		WithClient(client),
		WithMaxAttempts(1),
		// No WithDrainLimit — uses defaultDrainLimit (4096).
	)
	if err == nil {
		t.Fatal("expected error on 500")
	}

	got := count.Load()
	if got >= int64(bodySize) {
		t.Errorf("drained %d bytes of a %d-byte body; default cap should prevent full drain", got, bodySize)
	}
	if got > 16*1024 {
		t.Errorf("drained %d bytes, expected near the 4096 default", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"3", 3 * time.Second},
		{strconv.Itoa(60), 60 * time.Second},
		{"not-a-number", 0},
	}
	for _, tc := range tests {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func Get(ctx context.Context, url string, opts ...Option) (*Response, error) {
	logger := log.NewLogger("test", log.NewHandler(io.Discard, "dev"))
	return NewClient(logger).Get(ctx, url, opts...)
}

func Do(ctx context.Context, req *http.Request, opts ...Option) (*Response, error) {
	logger := log.NewLogger("test", log.NewHandler(io.Discard, "dev"))
	return NewClient(logger).Do(ctx, req, opts...)
}

func TestClient_Struct(t *testing.T) {
	logger := log.NewLogger("test-struct", log.NewHandler(io.Discard, "dev"))
	client := NewClient(logger)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}
