package auth

import (
	"context"

	"github.com/sergioneiravargas/template-go/internal/platform/mailer"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

// Persistence for user accounts, refresh tokens and password reset tokens.
// Get* methods return (nil, nil) when no row matches.
type UserRepository interface {
	CreateUser(ctx context.Context, user *User) error
	GetUser(ctx context.Context, id string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	CreateRefreshToken(ctx context.Context, token *RefreshToken) error
	GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error)
	RotateRefreshToken(ctx context.Context, oldID string, next *RefreshToken) error
	RevokeRefreshTokenFamily(ctx context.Context, familyID string) error
	CreatePasswordResetToken(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error
	GetPasswordResetTokenByHash(ctx context.Context, tokenHash string) (*PasswordResetToken, error)
	ConsumePasswordResetToken(ctx context.Context, token *PasswordResetToken, passwordHash string) error
}

// Mailer sends the slice's transactional email through the platform mailer.
type Mailer interface {
	Send(ctx context.Context, email mailer.Email) error
}

var (
	_ UserRepository = (*Repository)(nil)
	_ Mailer         = (*mailer.SES)(nil)
)

// Cache for resolved user information, keyed by user ID
type UserInfoCache interface {
	Get(key string) (value *UserInfo, found bool)
	Set(key string, value *UserInfo)
	Unset(key string)
}
