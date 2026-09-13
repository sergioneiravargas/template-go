package auth

import (
	"context"
	"net/http"

	"github.com/sergioneiravargas/template-go/internal/platform/httpfetch"
)

type fakeFetcher struct {
	GetFunc func(ctx context.Context, url string, opts ...httpfetch.Option) (*httpfetch.Response, error)
	DoFunc  func(ctx context.Context, req *http.Request, opts ...httpfetch.Option) (*httpfetch.Response, error)
}

func (f *fakeFetcher) Get(ctx context.Context, url string, opts ...httpfetch.Option) (*httpfetch.Response, error) {
	return f.GetFunc(ctx, url, opts...)
}

func (f *fakeFetcher) Do(ctx context.Context, req *http.Request, opts ...httpfetch.Option) (*httpfetch.Response, error) {
	return f.DoFunc(ctx, req, opts...)
}
