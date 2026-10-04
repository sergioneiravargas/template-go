package mailer

import "context"

// Mailer sends transactional email. Implementations must be safe for
// concurrent use.
type Mailer interface {
	Send(ctx context.Context, email Email) error
}

var _ Mailer = (*SES)(nil)
