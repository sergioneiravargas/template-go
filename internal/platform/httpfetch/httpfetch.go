package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
)

// Constants matching the original defaults.
const (
	defaultMaxAttempts   = 3
	defaultTimeout       = 10 * time.Second
	defaultBaseBackoff   = 200 * time.Millisecond
	defaultMaxBackoff    = 10 * time.Second
	defaultDrainLimit    = 4096
	defaultMaxBodySize   = 10 * 1024 * 1024 // 10MB
	maxBackoffShift      = 30
	maxRetryAfterSeconds = 3600 // 1 hour
)

type Request = http.Request

// Response represents the HTTP response details.
type Response struct {
	Body   []byte
	Header http.Header
	Status int
}

// HTTPError is returned for non-2xx status codes.
type HTTPError struct {
	Status int
	URL    string
	Body   []byte
}

func (e *HTTPError) Error() string {
	// Truncate body preview to avoid huge log outputs.
	bodyPreview := string(e.Body)
	if len(bodyPreview) > 200 {
		bodyPreview = bodyPreview[:200] + "..."
	}
	bodyPreview = strings.ReplaceAll(bodyPreview, "\n", " ")
	return fmt.Sprintf("httpfetch: non-2xx status %d for %s: %s", e.Status, e.URL, bodyPreview)
}

// loggerAdapter maps retryablehttp.LeveledLogger to the framework's log.Logger.
type loggerAdapter struct {
	logger *log.Logger
}

func (a *loggerAdapter) Debug(msg string, keysAndValues ...any) {
	a.logger.Debug(msg, toMap(keysAndValues))
}

func (a *loggerAdapter) Info(msg string, keysAndValues ...any) {
	a.logger.Info(msg, toMap(keysAndValues))
}

func (a *loggerAdapter) Warn(msg string, keysAndValues ...any) {
	a.logger.Warn(msg, toMap(keysAndValues))
}

func (a *loggerAdapter) Error(msg string, keysAndValues ...any) {
	a.logger.Error(msg, toMap(keysAndValues))
}

func toMap(keysAndValues []any) log.Context {
	ctx := make(log.Context)
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 < len(keysAndValues) {
			key, ok := keysAndValues[i].(string)
			if ok {
				ctx[key] = keysAndValues[i+1]
			}
		}
	}
	return ctx
}

type config struct {
	logger      *log.Logger
	client      *http.Client
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	drainLimit  int64
	maxBodySize int64
	onRetry     func(attempt int, err error, wait time.Duration)
}

// Option modifies the configuration of the client or request.
type Option func(*config)

// WithClient configures the underlying HTTP client.
func WithClient(c *http.Client) Option {
	return func(cfg *config) { cfg.client = c }
}

// WithMaxAttempts sets the maximum number of attempts.
func WithMaxAttempts(attempts int) Option {
	return func(cfg *config) { cfg.maxAttempts = attempts }
}

// WithBaseBackoff sets the starting backoff duration.
func WithBaseBackoff(d time.Duration) Option {
	return func(cfg *config) { cfg.baseBackoff = d }
}

// WithMaxBackoff sets the ceiling for the backoff duration.
func WithMaxBackoff(d time.Duration) Option {
	return func(cfg *config) { cfg.maxBackoff = d }
}

// WithDrainLimit limits how many bytes of a failed response body are read.
func WithDrainLimit(limit int64) Option {
	return func(cfg *config) { cfg.drainLimit = limit }
}

// WithMaxBodySize restricts the size of successful response bodies.
func WithMaxBodySize(size int64) Option {
	return func(cfg *config) { cfg.maxBodySize = size }
}

// WithOnRetry registers a callback invoked before each retry sleep.
func WithOnRetry(fn func(attempt int, err error, wait time.Duration)) Option {
	return func(cfg *config) { cfg.onRetry = fn }
}

// WithLogger sets the logger used for retry logs.
func WithLogger(l *log.Logger) Option {
	return func(cfg *config) { cfg.logger = l }
}

// Client executes HTTP requests with retries on transient failures.
type Client struct {
	logger      *log.Logger
	httpClient  *http.Client
	retryClient *retryablehttp.Client
}

// NewClient creates a new Client configured with the given logger and standard settings.
func NewClient(logger *log.Logger) *Client {
	httpClient := &http.Client{
		Timeout: defaultTimeout,
	}
	retryClient := retryablehttp.NewClient()
	retryClient.HTTPClient = httpClient
	retryClient.Logger = &loggerAdapter{logger: logger}
	retryClient.RetryMax = defaultMaxAttempts - 1
	retryClient.RetryWaitMin = defaultBaseBackoff
	retryClient.RetryWaitMax = defaultMaxBackoff
	retryClient.ErrorHandler = retryablehttp.PassthroughErrorHandler

	return &Client{
		logger:      logger,
		httpClient:  httpClient,
		retryClient: retryClient,
	}
}

// Get fetches a URL with retries on transient failures.
func (c *Client) Get(ctx context.Context, url string, opts ...Option) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(ctx, req, opts...)
}

// Do executes an HTTP request with retries on transient failures.
func (c *Client) Do(ctx context.Context, req *Request, opts ...Option) (*Response, error) {
	cfg := &config{
		logger:      c.logger,
		client:      c.httpClient,
		maxAttempts: c.retryClient.RetryMax + 1,
		baseBackoff: c.retryClient.RetryWaitMin,
		maxBackoff:  c.retryClient.RetryWaitMax,
		drainLimit:  defaultDrainLimit,
		maxBodySize: defaultMaxBodySize,
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.maxAttempts < 1 {
		cfg.maxAttempts = 1
	}
	if cfg.maxAttempts > 1 && req.Body != nil && req.GetBody == nil {
		return nil, errors.New("httpfetch: request has Body but no GetBody; cannot retry safely")
	}

	var successBody []byte
	var lastErr error
	var nonRetryable bool

	// Build a per-request client instead of copying c.retryClient by value
	// (retryablehttp.Client holds internal sync state that must not be copied).
	rc := &retryablehttp.Client{
		HTTPClient:   cfg.client,
		Logger:       &loggerAdapter{logger: cfg.logger},
		RetryMax:     cfg.maxAttempts - 1,
		RetryWaitMin: cfg.baseBackoff,
		RetryWaitMax: cfg.maxBackoff,
		ErrorHandler: c.retryClient.ErrorHandler,
	}

	rc.CheckRetry = func(cctx context.Context, resp *http.Response, err error) (bool, error) {
		if cctx.Err() != nil {
			lastErr = cctx.Err()
			return false, cctx.Err()
		}
		if err != nil {
			lastErr = fmt.Errorf("httpfetch: request error: %w", err)
			return true, nil
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			body, rerr := readBody(resp.Body, cfg.maxBodySize)
			if rerr != nil {
				lastErr = fmt.Errorf("httpfetch: reading body: %w", rerr)
				return true, nil
			}
			successBody = body
			return false, nil
		}

		// Non-2xx response. Create HTTPError.
		var body []byte
		if cfg.drainLimit > 0 {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, cfg.drainLimit))
		} else {
			body, _ = io.ReadAll(resp.Body)
		}
		herr := &HTTPError{Status: resp.StatusCode, URL: req.URL.String(), Body: body}
		lastErr = herr

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if retryable {
			return true, nil
		}

		// Non-retryable non-2xx status (e.g. 404)
		nonRetryable = true
		drainAndClose(resp.Body, cfg.drainLimit)
		return false, herr
	}

	rc.Backoff = func(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
		var delay time.Duration
		if resp != nil {
			delay = parseRetryAfter(resp.Header.Get("Retry-After"))
		}

		var wait time.Duration
		if delay > 0 {
			if delay > max {
				wait = max
			} else {
				wait = delay
			}
		} else {
			shift := attemptNum
			if shift > maxBackoffShift {
				shift = maxBackoffShift
			}
			upper := min << shift
			if upper <= 0 || upper > max {
				upper = max
			}
			if upper > 0 {
				wait = time.Duration(rand.Int64N(int64(upper)))
			}
		}

		if cfg.onRetry != nil {
			cfg.onRetry(attemptNum+1, lastErr, wait)
		}

		return wait
	}

	// Prepare retryable request
	reqClone := req.Clone(ctx)
	retryReq, err := retryablehttp.FromRequest(reqClone)
	if err != nil {
		return nil, err
	}

	resp, err := rc.Do(retryReq)
	if resp != nil {
		resp.Body.Close()
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if nonRetryable {
		return nil, err
	}

	if err != nil {
		return nil, fmt.Errorf("httpfetch: after %d attempts: %w", cfg.maxAttempts, lastErr)
	}

	if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return nil, fmt.Errorf("httpfetch: after %d attempts: %w", cfg.maxAttempts, lastErr)
	}

	return &Response{
		Body:   successBody,
		Header: resp.Header.Clone(),
		Status: resp.StatusCode,
	}, nil
}

// readBody reads from r up to maxBytes. Returns error if payload exceeds limit.
func readBody(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(r)
	}
	// Read up to maxBytes + 1 to detect overflow
	lr := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response body size exceeds limit of %d bytes", maxBytes)
	}
	return data, nil
}

func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil {
		if secs < 0 {
			return 0
		}
		if secs > maxRetryAfterSeconds {
			return maxRetryAfterSeconds * time.Second
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		if delay := time.Until(t); delay > 0 {
			if delay > maxRetryAfterSeconds*time.Second {
				return maxRetryAfterSeconds * time.Second
			}
			return delay
		}
	}
	return 0
}

func drainAndClose(rc io.ReadCloser, limit int64) {
	if rc == nil {
		return
	}
	if limit > 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(rc, limit))
	} else {
		_, _ = io.Copy(io.Discard, rc)
	}
	_ = rc.Close()
}
