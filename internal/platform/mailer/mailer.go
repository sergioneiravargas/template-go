package mailer

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Mailer configuration
type Conf struct {
	AWSRegion string
	// Sender is the From address, optionally with a display name
	// ("template-go <no-reply@example.com>"); it must be verified in SES.
	Sender string
}

// Transactional email; HTMLBody is optional.
type Email struct {
	To       []string
	Subject  string
	TextBody string
	HTMLBody string
}

func (e Email) validate() error {
	if len(e.To) == 0 {
		return errors.New("email recipient is required")
	}
	if e.Subject == "" {
		return errors.New("email subject is required")
	}
	if e.TextBody == "" {
		return errors.New("email text body is required")
	}

	return nil
}

// sesAPI is the slice of the SES v2 client consumed by SES, so tests can
// fake the AWS API without credentials.
type sesAPI interface {
	SendEmail(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// SES sends email through AWS Simple Email Service
type SES struct {
	conf Conf
	api  sesAPI
}

// Builds the SES mailer with the default AWS credentials chain (environment
// keys locally, instance role in AWS). Credentials are resolved lazily on the
// first send, so construction needs no AWS access.
func NewSES(ctx context.Context, conf Conf) (*SES, error) {
	if conf.AWSRegion == "" {
		return nil, errors.New("aws region is required")
	}
	if conf.Sender == "" {
		return nil, errors.New("sender is required")
	}

	awsConf, err := config.LoadDefaultConfig(ctx, config.WithRegion(conf.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("failed to load aws config: %w", err)
	}

	return &SES{
		conf: conf,
		api:  sesv2.NewFromConfig(awsConf),
	}, nil
}

func (s *SES) Send(ctx context.Context, email Email) error {
	if err := email.validate(); err != nil {
		return err
	}

	body := types.Body{
		Text: &types.Content{Data: aws.String(email.TextBody)},
	}
	if email.HTMLBody != "" {
		body.Html = &types.Content{Data: aws.String(email.HTMLBody)}
	}

	_, err := s.api.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.conf.Sender),
		Destination:      &types.Destination{ToAddresses: email.To},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: aws.String(email.Subject)},
				Body:    &body,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}
