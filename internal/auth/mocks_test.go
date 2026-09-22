package auth

import "context"

type fakeUserRepository struct {
	CreateUserFunc               func(ctx context.Context, user *User) error
	GetUserFunc                  func(ctx context.Context, id string) (*User, error)
	GetUserByEmailFunc           func(ctx context.Context, email string) (*User, error)
	CreateRefreshTokenFunc       func(ctx context.Context, token *RefreshToken) error
	GetRefreshTokenByHashFunc    func(ctx context.Context, tokenHash string) (*RefreshToken, error)
	RotateRefreshTokenFunc       func(ctx context.Context, oldID string, next *RefreshToken) error
	RevokeRefreshTokenFamilyFunc func(ctx context.Context, familyID string) error
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
