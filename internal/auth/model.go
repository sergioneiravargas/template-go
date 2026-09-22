package auth

import (
	"crypto/rsa"
	"errors"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/validation"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour

	passwordMinLen = 8
	passwordMaxLen = 512
)

// Auth service configuration
type Conf struct {
	PEMCertificate PEMCertificate
}

// RSA key pair used to sign and verify locally issued tokens
type PEMCertificate struct {
	Private *rsa.PrivateKey
	Public  *rsa.PublicKey
}

// Registered user account
type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	PasswordHash  string    `json:"-"`
	GivenName     string    `json:"given_name"`
	FamilyName    string    `json:"family_name"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Stored refresh token; only the hash of the opaque token is persisted
type RefreshToken struct {
	ID         string
	UserID     string
	TokenHash  string
	FamilyID   string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *string
}

// Token pair returned by login and refresh
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type RegisterInput struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

func (i RegisterInput) Validate() error {
	if err := validation.ValidateEmail(i.Email); err != nil {
		return err
	}
	if len(i.Password) < passwordMinLen || len(i.Password) > passwordMaxLen {
		return errors.New("password must be between 8 and 512 characters")
	}

	return nil
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (i LoginInput) Validate() error {
	if err := validation.ValidateEmail(i.Email); err != nil {
		return err
	}
	if i.Password == "" {
		return errors.New("password cannot be empty")
	}

	return nil
}

type RefreshInput struct {
	RefreshToken string `json:"refresh_token"`
}

func (i RefreshInput) Validate() error {
	if i.RefreshToken == "" {
		return errors.New("refresh token cannot be empty")
	}

	return nil
}

type LogoutInput struct {
	RefreshToken string `json:"refresh_token"`
}

func (i LogoutInput) Validate() error {
	if i.RefreshToken == "" {
		return errors.New("refresh token cannot be empty")
	}

	return nil
}
