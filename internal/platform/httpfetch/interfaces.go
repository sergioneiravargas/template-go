package httpfetch

import (
	"context"
	"net/http"
)

type Fetcher interface {
	Get(ctx context.Context, url string, opts ...Option) (*Response, error)
	Do(ctx context.Context, req *http.Request, opts ...Option) (*Response, error)
}
