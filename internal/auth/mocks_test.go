package auth

import (
	"context"

	"github.com/sergioneiravargas/template-go/internal/platform/mailer"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

type fakeUserRepository struct {
	CreateUserFunc                  func(ctx context.Context, user *User) error
	GetUserFunc                     func(ctx context.Context, id string) (*User, error)
	GetUserByEmailFunc              func(ctx context.Context, email string) (*User, error)
	CreateRefreshTokenFunc          func(ctx context.Context, token *RefreshToken) error
	GetRefreshTokenByHashFunc       func(ctx context.Context, tokenHash string) (*RefreshToken, error)
	RotateRefreshTokenFunc          func(ctx context.Context, oldID string, next *RefreshToken) error
	RevokeRefreshTokenFamilyFunc    func(ctx context.Context, familyID string) error
	CreatePasswordResetTokenFunc    func(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error
	GetPasswordResetTokenByHashFunc func(ctx context.Context, tokenHash string) (*PasswordResetToken, error)
	ConsumePasswordResetTokenFunc   func(ctx context.Context, token *PasswordResetToken, passwordHash string) error
}

var _ UserRepository = (*fakeUserRepository)(nil)

func (f *fakeUserRepository) CreateUser(ctx context.Context, user *User) error {
	return f.CreateUserFunc(ctx, user)
}

func (f *fakeUserRepository) GetUser(ctx context.Context, id string) (*User, error) {
	return f.GetUserFunc(ctx, id)
}

func (f *fakeUserRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return f.GetUserByEmailFunc(ctx, email)
}

func (f *fakeUserRepository) CreateRefreshToken(ctx context.Context, token *RefreshToken) error {
	return f.CreateRefreshTokenFunc(ctx, token)
}

func (f *fakeUserRepository) GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	return f.GetRefreshTokenByHashFunc(ctx, tokenHash)
}

func (f *fakeUserRepository) RotateRefreshToken(ctx context.Context, oldID string, next *RefreshToken) error {
	return f.RotateRefreshTokenFunc(ctx, oldID, next)
}

func (f *fakeUserRepository) RevokeRefreshTokenFamily(ctx context.Context, familyID string) error {
	return f.RevokeRefreshTokenFamilyFunc(ctx, familyID)
}

func (f *fakeUserRepository) CreatePasswordResetToken(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error {
	return f.CreatePasswordResetTokenFunc(ctx, token, queueMessages...)
}

func (f *fakeUserRepository) GetPasswordResetTokenByHash(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
	return f.GetPasswordResetTokenByHashFunc(ctx, tokenHash)
}

func (f *fakeUserRepository) ConsumePasswordResetToken(ctx context.Context, token *PasswordResetToken, passwordHash string) error {
	return f.ConsumePasswordResetTokenFunc(ctx, token, passwordHash)
}

type fakeMailer struct {
	SendFunc func(ctx context.Context, email mailer.Email) error
}

var _ Mailer = (*fakeMailer)(nil)

func (f *fakeMailer) Send(ctx context.Context, email mailer.Email) error {
	return f.SendFunc(ctx, email)
}
