package mailer

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
)

type fakeSESAPI struct {
	SendEmailFunc func(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

func (f *fakeSESAPI) SendEmail(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	return f.SendEmailFunc(ctx, params, optFns...)
}

func TestNewSESValidatesConf(t *testing.T) {
	tests := []struct {
		name string
		conf Conf
	}{
		{name: "missing region", conf: Conf{Sender: "no-reply@example.com"}},
		{name: "missing sender", conf: Conf{AWSRegion: "us-east-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSES(context.Background(), tt.conf); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestSESSend(t *testing.T) {
	conf := Conf{AWSRegion: "us-east-1", Sender: "template-go <no-reply@example.com>"}

	t.Run("sends the email through the SES API", func(t *testing.T) {
		var captured *sesv2.SendEmailInput
		ses := &SES{
			conf: conf,
			api: &fakeSESAPI{
				SendEmailFunc: func(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
					captured = params
					return &sesv2.SendEmailOutput{}, nil
				},
			},
		}

		err := ses.Send(context.Background(), Email{
			To:       []string{"user@example.com"},
			Subject:  "Subject",
			TextBody: "Text body",
			HTMLBody: "<p>HTML body</p>",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if captured == nil {
			t.Fatal("expected the SES API to be called")
		}
		if got := *captured.FromEmailAddress; got != conf.Sender {
			t.Errorf("unexpected sender: %s", got)
		}
		if got := captured.Destination.ToAddresses; len(got) != 1 || got[0] != "user@example.com" {
			t.Errorf("unexpected recipients: %v", got)
		}
		if got := *captured.Content.Simple.Subject.Data; got != "Subject" {
			t.Errorf("unexpected subject: %s", got)
		}
		if got := *captured.Content.Simple.Body.Text.Data; got != "Text body" {
			t.Errorf("unexpected text body: %s", got)
		}
		if got := *captured.Content.Simple.Body.Html.Data; got != "<p>HTML body</p>" {
			t.Errorf("unexpected html body: %s", got)
		}
	})

	t.Run("omits the html part when empty", func(t *testing.T) {
		var captured *sesv2.SendEmailInput
		ses := &SES{
			conf: conf,
			api: &fakeSESAPI{
				SendEmailFunc: func(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
					captured = params
					return &sesv2.SendEmailOutput{}, nil
				},
			},
		}

		err := ses.Send(context.Background(), Email{
			To:       []string{"user@example.com"},
			Subject:  "Subject",
			TextBody: "Text body",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if captured.Content.Simple.Body.Html != nil {
			t.Error("expected no html part")
		}
	})

	t.Run("wraps SES API errors", func(t *testing.T) {
		apiErr := errors.New("throttled")
		ses := &SES{
			conf: conf,
			api: &fakeSESAPI{
				SendEmailFunc: func(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
					return nil, apiErr
				},
			},
		}

		err := ses.Send(context.Background(), Email{
			To:       []string{"user@example.com"},
			Subject:  "Subject",
			TextBody: "Text body",
		})
		if !errors.Is(err, apiErr) {
			t.Fatalf("expected the API error to be wrapped, got: %v", err)
		}
	})

	t.Run("rejects invalid emails without calling the API", func(t *testing.T) {
		tests := []struct {
			name  string
			email Email
		}{
			{name: "missing recipient", email: Email{Subject: "s", TextBody: "t"}},
			{name: "missing subject", email: Email{To: []string{"user@example.com"}, TextBody: "t"}},
			{name: "missing text body", email: Email{To: []string{"user@example.com"}, Subject: "s"}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				called := false
				ses := &SES{
					conf: conf,
					api: &fakeSESAPI{
						SendEmailFunc: func(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
							called = true
							return &sesv2.SendEmailOutput{}, nil
						},
					},
				}

				if err := ses.Send(context.Background(), tt.email); err == nil {
					t.Fatal("expected an error, got nil")
				}
				if called {
					t.Error("expected the SES API not to be called")
				}
			})
		}
	})
}
