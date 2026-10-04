package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"
)

// SQL repository for users and refresh tokens
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	if db == nil {
		panic("db is required")
	}

	return &Repository{db: db}
}

func (r *Repository) CreateUser(ctx context.Context, user *User) error {
	_, err := r.db.ExecContext(
		ctx,
		"INSERT INTO auth_user (id, email, password_hash, given_name, family_name, email_verified, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
		user.ID,
		user.Email,
		user.PasswordHash,
		user.GivenName,
		user.FamilyName,
		user.EmailVerified,
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		if sql.IsUniqueViolation(err) {
			return ErrEmailAlreadyExists
		}

		return fmt.Errorf("failed to create user: %w", err)
	}

	return nil
}

func (r *Repository) GetUser(ctx context.Context, id string) (*User, error) {
	row := r.db.QueryRowContext(
		ctx,
		"SELECT id, email, password_hash, given_name, family_name, email_verified, created_at, updated_at FROM auth_user WHERE id = $1",
		id,
	)

	return scanUser(row)
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := r.db.QueryRowContext(
		ctx,
		"SELECT id, email, password_hash, given_name, family_name, email_verified, created_at, updated_at FROM auth_user WHERE LOWER(email) = LOWER($1)",
		email,
	)

	return scanUser(row)
}

func scanUser(row *sql.Row) (*User, error) {
	var user User
	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.GivenName,
		&user.FamilyName,
		&user.EmailVerified,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan user: %w", err)
	}

	return &user, nil
}

func (r *Repository) CreateRefreshToken(ctx context.Context, token *RefreshToken) error {
	_, err := r.db.ExecContext(
		ctx,
		"INSERT INTO auth_refresh_token (id, user_id, token_hash, family_id, expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
		token.ID,
		token.UserID,
		token.TokenHash,
		token.FamilyID,
		token.ExpiresAt,
		token.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create refresh token: %w", err)
	}

	return nil
}

func (r *Repository) GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	row := r.db.QueryRowContext(
		ctx,
		"SELECT id, user_id, token_hash, family_id, expires_at, created_at, revoked_at, replaced_by FROM auth_refresh_token WHERE token_hash = $1",
		tokenHash,
	)

	var token RefreshToken
	var revokedAt sql.NullTime
	var replacedBy sql.NullString
	err := row.Scan(
		&token.ID,
		&token.UserID,
		&token.TokenHash,
		&token.FamilyID,
		&token.ExpiresAt,
		&token.CreatedAt,
		&revokedAt,
		&replacedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan refresh token: %w", err)
	}

	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Time
	}
	if replacedBy.Valid {
		value := replacedBy.String
		token.ReplacedBy = &value
	}

	return &token, nil
}

// Revokes the old token and creates its replacement in one transaction.
// Returns ErrRefreshTokenRotated when the old token was already revoked or
// rotated by a concurrent request.
func (r *Repository) RotateRefreshToken(ctx context.Context, oldID string, next *RefreshToken) error {
	return sql.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(
			ctx,
			"UPDATE auth_refresh_token SET revoked_at = $1, replaced_by = $2 WHERE id = $3 AND revoked_at IS NULL",
			next.CreatedAt,
			next.ID,
			oldID,
		)
		if err != nil {
			return fmt.Errorf("failed to revoke refresh token: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to check refresh token rotation: %w", err)
		}
		if affected == 0 {
			return ErrRefreshTokenRotated
		}

		_, err = tx.ExecContext(
			ctx,
			"INSERT INTO auth_refresh_token (id, user_id, token_hash, family_id, expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
			next.ID,
			next.UserID,
			next.TokenHash,
			next.FamilyID,
			next.ExpiresAt,
			next.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("failed to create refresh token: %w", err)
		}

		return nil
	})
}

// Persists the reset token and its outbox messages in one transaction so the
// reset email is only queued when the token exists.
func (r *Repository) CreatePasswordResetToken(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error {
	return sql.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(
			ctx,
			"INSERT INTO auth_password_reset_token (id, user_id, token_hash, expires_at, created_at) VALUES ($1, $2, $3, $4, $5)",
			token.ID,
			token.UserID,
			token.TokenHash,
			token.ExpiresAt,
			token.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("failed to create password reset token: %w", err)
		}

		if len(queueMessages) > 0 {
			if err := queue.CreateOutboxMessage(ctx, tx, QueueName, queueMessages...); err != nil {
				return fmt.Errorf("failed to create outbox messages: %w", err)
			}
		}

		return nil
	})
}

func (r *Repository) GetPasswordResetTokenByHash(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
	row := r.db.QueryRowContext(
		ctx,
		"SELECT id, user_id, token_hash, expires_at, created_at, used_at FROM auth_password_reset_token WHERE token_hash = $1",
		tokenHash,
	)

	var token PasswordResetToken
	var usedAt sql.NullTime
	err := row.Scan(
		&token.ID,
		&token.UserID,
		&token.TokenHash,
		&token.ExpiresAt,
		&token.CreatedAt,
		&usedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan password reset token: %w", err)
	}

	if usedAt.Valid {
		token.UsedAt = &usedAt.Time
	}

	return &token, nil
}

// Consumes the reset token and applies the new password in one transaction:
// the token (plus any other outstanding token for the user) is marked used,
// the password hash is replaced, and every refresh token family is revoked.
// Returns ErrPasswordResetTokenUsed when the token was already consumed by a
// concurrent request.
func (r *Repository) ConsumePasswordResetToken(ctx context.Context, token *PasswordResetToken, passwordHash string) error {
	return sql.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(
			ctx,
			"UPDATE auth_password_reset_token SET used_at = NOW() WHERE id = $1 AND used_at IS NULL",
			token.ID,
		)
		if err != nil {
			return fmt.Errorf("failed to consume password reset token: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to check password reset token consumption: %w", err)
		}
		if affected == 0 {
			return ErrPasswordResetTokenUsed
		}

		_, err = tx.ExecContext(
			ctx,
			"UPDATE auth_password_reset_token SET used_at = NOW() WHERE user_id = $1 AND used_at IS NULL",
			token.UserID,
		)
		if err != nil {
			return fmt.Errorf("failed to invalidate outstanding password reset tokens: %w", err)
		}

		_, err = tx.ExecContext(
			ctx,
			"UPDATE auth_user SET password_hash = $1, updated_at = NOW() WHERE id = $2",
			passwordHash,
			token.UserID,
		)
		if err != nil {
			return fmt.Errorf("failed to update user password: %w", err)
		}

		_, err = tx.ExecContext(
			ctx,
			"UPDATE auth_refresh_token SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL",
			token.UserID,
		)
		if err != nil {
			return fmt.Errorf("failed to revoke refresh tokens: %w", err)
		}

		return nil
	})
}

func (r *Repository) RevokeRefreshTokenFamily(ctx context.Context, familyID string) error {
	_, err := r.db.ExecContext(
		ctx,
		"UPDATE auth_refresh_token SET revoked_at = NOW() WHERE family_id = $1 AND revoked_at IS NULL",
		familyID,
	)
	if err != nil {
		return fmt.Errorf("failed to revoke refresh token family: %w", err)
	}

	return nil
}
