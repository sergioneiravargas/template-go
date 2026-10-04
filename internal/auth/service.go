package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"html"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sergioneiravargas/template-go/internal/platform/mailer"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

var (
	ErrUserNotFound              = errors.New("user not found")
	ErrInvalidCredentials        = errors.New("invalid credentials")
	ErrEmailAlreadyExists        = errors.New("email already exists")
	ErrInvalidRefreshToken       = errors.New("invalid refresh token")
	ErrRefreshTokenRotated       = errors.New("refresh token already rotated")
	ErrInvalidPasswordResetToken = errors.New("invalid password reset token")
	ErrPasswordResetTokenUsed    = errors.New("password reset token already used")
)

// Service for auth operations
type Service struct {
	conf          Conf
	repository    UserRepository
	mailer        Mailer
	userInfoCache UserInfoCache
	passwordSem   chan struct{}
}

// Service option
type ServiceOption func(*Service)

// Service option to set the user info cache
func ServiceWithUserInfoCache(cache UserInfoCache) ServiceOption {
	return func(s *Service) {
		s.userInfoCache = cache
	}
}

// Creates a new auth service
func NewService(
	conf Conf,
	repository UserRepository,
	mailer Mailer,
	opts ...ServiceOption,
) *Service {
	if repository == nil {
		panic("repository is required")
	}
	if mailer == nil {
		panic("mailer is required")
	}

	service := Service{
		conf:        conf,
		repository:  repository,
		mailer:      mailer,
		passwordSem: newPasswordSem(conf.PasswordHashMaxConcurrency),
	}

	for _, opt := range opts {
		opt(&service)
	}

	return &service
}

var dummyPasswordHash = sync.OnceValue(func() string {
	hash, err := HashPassword("dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return hash
})

// Creates a new user account with an argon2id-hashed password
func (s *Service) Register(ctx context.Context, input RegisterInput) (*User, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	hash, err := s.hashPassword(ctx, input.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	now := time.Now()
	user := &User{
		ID:           uuid.NewString(),
		Email:        input.Email,
		PasswordHash: hash,
		GivenName:    input.GivenName,
		FamilyName:   input.FamilyName,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.repository.CreateUser(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
}

// Verifies the credentials and issues a new token pair
func (s *Service) Login(ctx context.Context, input LoginInput) (*TokenPair, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	user, err := s.repository.GetUserByEmail(ctx, input.Email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		// Burn the same hashing cost as a real comparison so response timing
		// does not reveal whether the account exists
		s.verifyPassword(ctx, dummyPasswordHash(), input.Password)
		return nil, ErrInvalidCredentials
	}

	match, err := s.verifyPassword(ctx, user.PasswordHash, input.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to verify password: %w", err)
	}
	if !match {
		return nil, ErrInvalidCredentials
	}

	return s.issueTokenPair(ctx, user.ID, uuid.NewString())
}

// Rotates the given refresh token and issues a new token pair. Reuse of a
// token that was already rotated or revoked kills its whole family.
func (s *Service) Refresh(ctx context.Context, input RefreshInput) (*TokenPair, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	refreshToken, err := s.repository.GetRefreshTokenByHash(ctx, HashRefreshToken(input.RefreshToken))
	if err != nil {
		return nil, err
	}
	if refreshToken == nil {
		return nil, ErrInvalidRefreshToken
	}

	now := time.Now()
	if refreshToken.RevokedAt != nil || refreshToken.ReplacedBy != nil {
		if err := s.repository.RevokeRefreshTokenFamily(ctx, refreshToken.FamilyID); err != nil {
			return nil, err
		}
		return nil, ErrInvalidRefreshToken
	}
	if refreshToken.ExpiresAt.Before(now) {
		return nil, ErrInvalidRefreshToken
	}

	user, err := s.repository.GetUser(ctx, refreshToken.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrInvalidRefreshToken
	}

	refreshTokenString, tokenHash, err := GenerateRefreshToken()
	if err != nil {
		return nil, err
	}
	next := &RefreshToken{
		ID:        uuid.NewString(),
		UserID:    refreshToken.UserID,
		TokenHash: tokenHash,
		FamilyID:  refreshToken.FamilyID,
		ExpiresAt: now.Add(RefreshTokenTTL),
		CreatedAt: now,
	}
	if err := s.repository.RotateRefreshToken(ctx, refreshToken.ID, next); err != nil {
		if errors.Is(err, ErrRefreshTokenRotated) {
			if revokeErr := s.repository.RevokeRefreshTokenFamily(ctx, refreshToken.FamilyID); revokeErr != nil {
				return nil, revokeErr
			}
			return nil, ErrInvalidRefreshToken
		}
		return nil, err
	}

	accessToken, err := s.generateAccessToken(refreshToken.UserID, now)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenString,
		ExpiresIn:    int64(AccessTokenTTL.Seconds()),
	}, nil
}

// Revokes the refresh token's family; unknown tokens are a no-op
func (s *Service) Logout(ctx context.Context, input LogoutInput) error {
	if err := input.Validate(); err != nil {
		return err
	}

	refreshToken, err := s.repository.GetRefreshTokenByHash(ctx, HashRefreshToken(input.RefreshToken))
	if err != nil {
		return err
	}
	if refreshToken == nil {
		return nil
	}

	return s.repository.RevokeRefreshTokenFamily(ctx, refreshToken.FamilyID)
}

// Issues a password reset token and queues the reset email through the
// outbox. Unknown emails return nil so the endpoint does not reveal whether
// an account exists.
func (s *Service) ForgotPassword(ctx context.Context, input ForgotPasswordInput) error {
	if err := input.Validate(); err != nil {
		return err
	}

	user, err := s.repository.GetUserByEmail(ctx, input.Email)
	if err != nil {
		return err
	}
	if user == nil {
		return nil
	}

	token, tokenHash, err := GeneratePasswordResetToken()
	if err != nil {
		return err
	}

	now := time.Now()
	row := &PasswordResetToken{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: now.Add(PasswordResetTokenTTL),
		CreatedAt: now,
	}

	// The raw token rides in the queue message because only its hash is
	// persisted; the worker cannot rebuild the email link from stored state.
	message, err := queue.NewMessage(MessageNamePasswordResetRequested, MessagePasswordResetRequested{
		UserID: user.ID,
		Token:  token,
	})
	if err != nil {
		return fmt.Errorf("failed to create queue message: %w", err)
	}

	return s.repository.CreatePasswordResetToken(ctx, row, message)
}

// Consumes the reset token, replaces the password and revokes every active
// session of the user
func (s *Service) ResetPassword(ctx context.Context, input ResetPasswordInput) error {
	if err := input.Validate(); err != nil {
		return err
	}

	row, err := s.repository.GetPasswordResetTokenByHash(ctx, HashPasswordResetToken(input.Token))
	if err != nil {
		return err
	}
	if row == nil || row.UsedAt != nil || row.ExpiresAt.Before(time.Now()) {
		return ErrInvalidPasswordResetToken
	}

	hash, err := s.hashPassword(ctx, input.Password)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	if err := s.repository.ConsumePasswordResetToken(ctx, row, hash); err != nil {
		if errors.Is(err, ErrPasswordResetTokenUsed) {
			return ErrInvalidPasswordResetToken
		}
		return err
	}

	return nil
}

// Sends the password reset email for a queued request; called by the queue
// handler in the worker
func (s *Service) SendPasswordResetEmail(ctx context.Context, input SendPasswordResetEmailInput) error {
	if err := input.Validate(); err != nil {
		return err
	}

	user, err := s.repository.GetUser(ctx, input.UserID)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}

	link := fmt.Sprintf("%s?token=%s", s.conf.PasswordResetURL, url.QueryEscape(input.Token))
	greeting := "Hello,"
	if user.GivenName != "" {
		greeting = fmt.Sprintf("Hello %s,", user.GivenName)
	}

	// The expiry wording must match PasswordResetTokenTTL.
	email := mailer.Email{
		To:      []string{user.Email},
		Subject: "Reset your password",
		TextBody: fmt.Sprintf(
			"%s\n\nWe received a request to reset your password. Open the link below to choose a new one; it expires in 1 hour.\n\n%s\n\nIf you did not request this, you can ignore this email.",
			greeting,
			link,
		),
		HTMLBody: fmt.Sprintf(
			"<p>%s</p><p>We received a request to reset your password. Open the link below to choose a new one; it expires in 1 hour.</p><p><a href=\"%s\">Reset your password</a></p><p>If you did not request this, you can ignore this email.</p>",
			html.EscapeString(greeting),
			link,
		),
	}

	if err := s.mailer.Send(ctx, email); err != nil {
		return fmt.Errorf("failed to send password reset email: %w", err)
	}

	return nil
}

func (s *Service) issueTokenPair(ctx context.Context, userID, familyID string) (*TokenPair, error) {
	refreshTokenString, tokenHash, err := GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	refreshToken := &RefreshToken{
		ID:        uuid.NewString(),
		UserID:    userID,
		TokenHash: tokenHash,
		FamilyID:  familyID,
		ExpiresAt: now.Add(RefreshTokenTTL),
		CreatedAt: now,
	}
	if err := s.repository.CreateRefreshToken(ctx, refreshToken); err != nil {
		return nil, err
	}

	accessToken, err := s.generateAccessToken(userID, now)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenString,
		ExpiresIn:    int64(AccessTokenTTL.Seconds()),
	}, nil
}

func (s *Service) generateAccessToken(userID string, now time.Time) (string, error) {
	return GenerateToken(MapClaims{
		"sub": userID,
		"iat": now.Unix(),
		"exp": now.Add(AccessTokenTTL).Unix(),
	}, s.conf.PEMCertificate.Private)
}

// Validates the given token against the local PEM public key
func (s *Service) ValidateToken(token string) error {
	return ValidateTokenWithPEM(token, s.conf.PEMCertificate.Public)
}

// Retrieves the claims from the given token
func (s *Service) TokenClaims(token string) (MapClaims, error) {
	return TokenClaimsFromPEM(token, s.conf.PEMCertificate.Public)
}

// Retrieves the user information for the given access token: claims sub,
// then the cache, then the user repository
func (s *Service) UserInfo(
	ctx context.Context,
	token string,
) (*UserInfo, error) {
	claims, err := s.TokenClaims(token)
	if err != nil {
		return nil, err
	}

	userID, valid := claims["sub"].(string)
	if !valid {
		return nil, ErrInvalidTokenClaims
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, ErrUserNotFound
	}

	if s.userInfoCache != nil {
		if userInfo, found := s.userInfoCache.Get(userID); found {
			return userInfo, nil
		}
	}

	user, err := s.repository.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}

	userInfo := &UserInfo{
		ID:            user.ID,
		GivenName:     user.GivenName,
		FamilyName:    user.FamilyName,
		Email:         user.Email,
		EmailVerified: user.EmailVerified,
	}
	if s.userInfoCache != nil {
		s.userInfoCache.Set(userID, userInfo)
	}

	return userInfo, nil
}

func (s *Service) GenerateToken(claims MapClaims) (string, error) {
	return GenerateToken(claims, s.conf.PEMCertificate.Private)
}

func ValidateTokenWithPEM(token string, key *rsa.PublicKey) error {
	parsedToken, err := ParseTokenWithPEM(token, key)
	if err != nil {
		return err
	}

	if !parsedToken.Valid {
		return ErrInvalidToken
	}

	return nil
}

func TokenClaimsFromPEM(token string, key *rsa.PublicKey) (MapClaims, error) {
	parsedToken, err := ParseTokenWithPEM(token, key)
	if err != nil {
		return nil, err
	}

	if !parsedToken.Valid {
		return nil, ErrInvalidToken
	}

	claims, valid := parsedToken.Claims.(MapClaims)
	if !valid {
		return nil, ErrInvalidTokenClaims
	}

	return claims, nil
}
