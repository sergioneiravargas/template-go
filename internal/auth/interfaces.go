package auth

import "context"

// Persistence for user accounts and refresh tokens. Get* methods return
// (nil, nil) when no row matches.
type UserRepository interface {
	CreateUser(ctx context.Context, user *User) error
	GetUser(ctx context.Context, id string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	CreateRefreshToken(ctx context.Context, token *RefreshToken) error
	GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error)
	RotateRefreshToken(ctx context.Context, oldID string, next *RefreshToken) error
	RevokeRefreshTokenFamily(ctx context.Context, familyID string) error
}

var _ UserRepository = (*Repository)(nil)

// Cache for resolved user information, keyed by user ID
type UserInfoCache interface {
	Get(key string) (value *UserInfo, found bool)
	Set(key string, value *UserInfo)
	Unset(key string)
}
